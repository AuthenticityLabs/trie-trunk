package main

// This is a test publisher around the real PostgreSQL and PIL ingress/history
// APIs. It adds no production subscription protocol or materialization service.
import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"trie-trunk/go/server/cache"
)

type streamDistribution struct {
	P50 float64 `json:"p50_us"`
	P95 float64 `json:"p95_us"`
	P99 float64 `json:"p99_us"`
}

func distribution(xs []float64) streamDistribution {
	if len(xs) == 0 {
		return streamDistribution{}
	}
	ys := append([]float64(nil), xs...)
	sort.Float64s(ys)
	return streamDistribution{ys[len(ys)/2], ys[min(len(ys)-1, len(ys)*95/100)], ys[min(len(ys)-1, len(ys)*99/100)]}
}
func micros(d time.Duration) float64 { return float64(d) / float64(time.Microsecond) }

type streamResult struct {
	DurableBeforePublish bool               `json:"durable_before_publish"`
	Variant              string             `json:"variant"`
	Round                int                `json:"round"`
	Subscribers          int                `json:"subscribers"`
	TextBytes            int                `json:"description_bytes"`
	Updates              int                `json:"updates"`
	CommitToApply        streamDistribution `json:"commit_to_apply"`
	WriteToApply         streamDistribution `json:"write_to_apply"`
	PublishToApply       streamDistribution `json:"publish_to_apply"`
	Ingress              streamDistribution `json:"ingress"`
	OriginWrite          streamDistribution `json:"origin_write"`
	AllApplied           streamDistribution `json:"commit_to_all_applied"`
	StreamBytes          float64            `json:"body_bytes_per_update_per_subscriber"`
	ReplicationBytes     float64            `json:"replication_body_bytes_per_update"`
	IngressRequests      int                `json:"ingress_requests"`
	GoBytes              float64            `json:"go_bytes_per_update"`
	GoAllocs             float64            `json:"go_allocs_per_update"`
	GoCPUus              float64            `json:"combined_go_cpu_us_per_update"`
	PGResultBytes        float64            `json:"pg_result_payload_bytes_per_update"`
}
type reconnectResult struct {
	Variant   string             `json:"variant"`
	Gap       int                `json:"missing_revisions"`
	TextBytes int                `json:"description_bytes"`
	Samples   int                `json:"samples"`
	Latency   streamDistribution `json:"request_to_applied"`
	BodyBytes int                `json:"body_bytes"`
}
type subscriptionReport struct {
	Representation   string            `json:"representation"`
	Go               string            `json:"go"`
	PostgreSQL       string            `json:"postgresql"`
	PostgresSettings map[string]string `json:"postgres_settings"`
	Updates          int               `json:"updates"`
	Rounds           int               `json:"rounds"`
	Results          []streamResult    `json:"results"`
	Reconnect        []reconnectResult `json:"reconnect"`
}

func integers(s string) ([]int, error) {
	var out []int
	for _, x := range strings.Split(s, ",") {
		n, e := strconv.Atoi(x)
		if e != nil || n < 1 {
			return nil, fmt.Errorf("invalid positive integer %q", x)
		}
		out = append(out, n)
	}
	return out, nil
}
func cpuTime() int64 {
	var r syscall.Rusage
	if e := syscall.Getrusage(syscall.RUSAGE_SELF, &r); e != nil {
		panic(e)
	}
	return r.Utime.Sec*1e6 + int64(r.Utime.Usec) + r.Stime.Sec*1e6 + int64(r.Stime.Usec)
}

// Native PIL frames are already self-delimiting. Reuse a decode buffer and
// consume each frame synchronously before reading the next. No transport wrapper.
func consumeUpdates(l *cache.Layout, r io.Reader, isJSON bool, visit func([]byte) error) error {
	input := bufio.NewReaderSize(r, 4096)
	if isJSON {
		decoder := json.NewDecoder(input)
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			if err := visit(raw); err != nil {
				return err
			}
		}
	}
	var raw []byte
	for {
		// Only EOF before a frame begins is clean stream completion. ReadFrame can
		// return EOF at an internal boundary too; that is truncation, not completion.
		if _, err := input.Peek(1); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var err error
		raw, err = l.ReadFrame(input, raw)
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}

		if err = visit(raw); err != nil {
			return err
		}
	}
}

type streamAck struct {
	subscriber, revision int
	applied              time.Time
	err                  error
}
type streamClients struct {
	queues    []chan []byte
	server    *httptest.Server
	client    *http.Client
	responses []*http.Response
	acks      chan streamAck
	wg        sync.WaitGroup
	done      chan struct{}
}

func openStreamClients(l *cache.Layout, initial Object, expected []Object, count int, isJSON bool) (*streamClients, error) {
	c := &streamClients{acks: make(chan streamAck, count*8), done: make(chan struct{})}
	for i := 0; i < count; i++ {
		c.queues = append(c.queues, make(chan []byte, 8))
	}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, e := strconv.Atoi(r.URL.Query().Get("subscriber"))
		if e != nil || i < 0 || i >= len(c.queues) {
			http.Error(w, "subscriber", 400)
			return
		}
		w.Header().Set("Content-Type", "application/pil")
		if isJSON {
			w.Header().Set("Content-Type", "application/x-ndjson")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		f := w.(http.Flusher)
		f.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case raw, ok := <-c.queues[i]:
				if !ok {
					return
				}
				if _, e = w.Write(raw); e != nil {
					return
				}
				f.Flush()
			}
		}
	}))
	transport := &http.Transport{MaxConnsPerHost: count + 2, MaxIdleConnsPerHost: count + 2, Proxy: nil}
	c.client = &http.Client{Transport: transport}
	for i := 0; i < count; i++ {
		response, e := c.client.Get(c.server.URL + "/?subscriber=" + strconv.Itoa(i))
		if e != nil {
			c.close()
			return nil, e
		}
		c.responses = append(c.responses, response)
		c.wg.Add(1)
		go func(i int, r *http.Response) {
			defer c.wg.Done()
			state := initial
			last := 0
			var frame cache.Frame
			e := consumeUpdates(l, r.Body, isJSON, func(payload []byte) error {
				if isJSON {
					if err := json.Unmarshal(payload, &state); err != nil {
						return err
					}
				} else {
					if err := apply(l, payload, &frame, &state); err != nil {
						return err
					}
				}
				revision := int(state.Revision)
				if revision != last+1 || revision >= len(expected) {
					return fmt.Errorf("subscriber %d ordering %d after %d", i, revision, last)
				}

				applied := time.Now()
				var bad error
				if !reflect.DeepEqual(state, expected[revision]) {
					bad = fmt.Errorf("subscriber %d wrong state at %d", i, revision)
				}
				select {
				case c.acks <- streamAck{i, revision, applied, bad}:
				case <-c.done:
					return io.EOF
				}
				last = revision
				return bad
			})
			if e != nil && !strings.Contains(e.Error(), "closed") {
				select {
				case c.acks <- streamAck{i, -1, time.Now(), e}:
				default:
				}
			}
		}(i, response)
	}
	return c, nil
}
func (c *streamClients) close() {
	close(c.done)
	for _, r := range c.responses {
		r.Body.Close()
	}
	if c.server != nil {
		c.server.Close()
	}
	c.wg.Wait()
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
}
func (c *streamClients) publish(raw []byte) error {
	for _, q := range c.queues {
		select {
		case q <- raw:
		default:
			return fmt.Errorf("subscriber queue stalled")
		}
	}
	return nil
}

const streamTable = "subscription_bench_objects"

func resetStreamObject(pg *pgxpool.Pool, o Object) error {
	_, e := pg.Exec(ctx, "INSERT INTO "+streamTable+" ("+columns+") VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(id) DO UPDATE SET revision_id=EXCLUDED.revision_id,state=EXCLUDED.state,amount=EXCLUDED.amount,attempts=EXCLUDED.attempts,active=EXCLUDED.active,customer_id=EXCLUDED.customer_id,currency=EXCLUDED.currency,email=EXCLUDED.email,note=EXCLUDED.note,description=EXCLUDED.description", o.ID, o.Revision, o.State, o.Amount, o.Attempts, o.Active, o.CustomerID, o.Currency, o.Email, o.Note, o.Description)
	return e
}
func updateStreamObject(pg *pgxpool.Pool, o Object, full bool) (Object, int, error) {
	var sql string
	var args []any
	switch o.Revision % 3 {
	case 0:
		sql = "state=$3,amount=$4"
		args = []any{o.ID, o.Revision, o.State, o.Amount}
	case 1:
		sql = "attempts=$3,active=$4"
		args = []any{o.ID, o.Revision, o.Attempts, o.Active}
	case 2:
		sql = "note=$3,state=$4"
		args = []any{o.ID, o.Revision, o.Note, o.State}
	}
	returning := "id,revision_id"
	if full {
		returning = columns
	}
	rows, e := pg.Query(ctx, "UPDATE "+streamTable+" SET revision_id=$2,"+sql+" WHERE id=$1 RETURNING "+returning, args...)
	if e != nil {
		return Object{}, 0, e
	}
	defer rows.Close()
	if !rows.Next() {
		if e = rows.Err(); e == nil {
			e = fmt.Errorf("missing object")
		}
		return Object{}, 0, e
	}
	size := 0
	for _, b := range rows.RawValues() {
		size += len(b)
	}
	var got Object
	if full {
		e = rows.Scan(&got.ID, &got.Revision, &got.State, &got.Amount, &got.Attempts, &got.Active, &got.CustomerID, &got.Currency, &got.Email, &got.Note, &got.Description)
	} else {
		e = rows.Scan(&got.ID, &got.Revision)
	}
	rows.Close()
	if e == nil {
		e = rows.Err()
	}
	return got, size, e
}
func postPIL(client *http.Client, url string, l *cache.Layout, frames [][]byte) error {
	body := frames[0]
	if len(frames) > 1 {
		body = bytes.Join(frames, nil)
	}
	r, e := http.NewRequest("POST", url+"/ingest?tap=benchmark/payment/transactions.v1/write", bytes.NewReader(body))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", "application/pil")
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("PIL-Features", "adaptive-integers")
	r.Header.Set("Definition-Version", fmt.Sprintf("%x", l.Digest))
	response, e := client.Do(r)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(response.Body)
	if e != nil {
		return e
	}
	if response.StatusCode != 200 || response.Header.Get("Trunk-Acknowledgement") != "durable-staging" || response.Header.Get("Trunk-Received") != strconv.Itoa(len(frames)) {
		return fmt.Errorf("ingress %d: %s", response.StatusCode, raw)
	}
	return nil
}

func subscriptionCase(pg *pgxpool.Pool, root, variant string, round, subscribers, textBytes, updates int, durableBeforePublish bool) (result streamResult, reconnect []reconnectResult, err error) {
	waitForDurable := durableBeforePublish || variant == "trunk-durable"
	result = streamResult{DurableBeforePublish: waitForDurable, Variant: variant, Round: round, Subscribers: subscribers, TextBytes: textBytes, Updates: updates}
	initial := fixture(1, 0, textBytes)
	expected := make([]Object, updates+1)
	expected[0] = initial
	for i := 1; i <= updates; i++ {
		expected[i] = expected[i-1]
		mutate(&expected[i], i)
	}
	if err = resetStreamObject(pg, initial); err != nil {
		return
	}
	l := layout()
	isTrunk := strings.HasPrefix(variant, "trunk-")
	var ep *cache.Endpoint
	var ingress *httptest.Server
	ingressClient := &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 2}, Timeout: 30 * time.Second}
	defer ingressClient.CloseIdleConnections()
	if isTrunk {
		ep, err = cache.OpenEndpoint(cache.Config{Root: root, Cache: "benchmark", Tenant: "benchmark", Layout: l, ObjectIndex: true, Now: func() time.Time { return start.Add(10 * time.Minute) }})
		if err != nil {
			return
		}
		defer ep.Close()
		ingress = httptest.NewServer(ep)
		defer ingress.Close()
		if err = postPIL(ingressClient, ingress.URL, l, [][]byte{encode(l, initial, 1, false)}); err != nil {
			return
		}
	}
	clients, e := openStreamClients(l, initial, expected, subscribers, variant == "pg-json")
	if e != nil {
		err = e
		return
	}
	defer clients.close()
	// The first 16 updates warm SQL statements, connections, parser buffers and caches.
	warm := 16
	if updates < 32 {
		warm = 0
	}
	started, committed, published := make([]time.Time, updates+1), make([]time.Time, updates+1), make([]time.Time, updates+1)
	if isTrunk && !waitForDurable {
		if variant != "trunk-direct" {
			return result, nil, fmt.Errorf("early publication supports trunk-direct only")
		}
		if err = setStagedObserver(func(frames [][]byte) error {
			var f cache.Frame
			for _, raw := range frames {
				if e := l.Scan(raw, &f); e != nil {
					return e
				}
				revision := int(number(f.Values[2]))
				if revision < 1 || revision >= len(published) {
					return fmt.Errorf("invalid publication revision")
				}
				published[revision] = time.Now()
				if e := clients.publish(raw); e != nil {
					return e
				}
			}
			return nil
		}); err != nil {
			return
		}
		defer setStagedObserver(nil)
	}
	type ingressOutcome struct {
		elapsed float64
		err     error
	}
	var pendingIngress chan ingressOutcome
	// Never release sender-owned frames or tear down the endpoint while persistence
	// is pending, even when subscriber validation fails.
	defer func() {
		if pendingIngress != nil {
			out := <-pendingIngress
			if err == nil {
				err = out.err
			}
		}
	}()
	var cta, wta, pta, all, ing, origin []float64
	totalBytes, replicationBytes, pgBytes, requests := 0, 0, 0, 0
	var before, after runtime.MemStats
	var cpuStart int64
	batchSize := 1
	if variant == "trunk-batch8" {
		batchSize = 8
	}
	for first := 1; first <= updates; first += batchSize {
		if first == warm+1 {
			runtime.GC()
			runtime.ReadMemStats(&before)
			cpuStart = cpuTime()
		}
		last := min(updates, first+batchSize-1)
		frames := make([][]byte, 0, last-first+1)
		objects := make([]Object, 0, last-first+1)
		for revision := first; revision <= last; revision++ {
			started[revision] = time.Now()
			got, size, e := updateStreamObject(pg, expected[revision], !isTrunk)
			committed[revision] = time.Now()
			if e != nil {
				err = e
				return
			}
			if got.ID != 1 || got.Revision != int32(revision) {
				err = fmt.Errorf("wrong PG acknowledgement")
				return
			}
			if revision > warm {
				origin = append(origin, micros(committed[revision].Sub(started[revision])))
				pgBytes += size
			}
			if isTrunk {
				raw := encode(l, expected[revision], 1, false)
				frames = append(frames, raw)
				if revision > warm {
					replicationBytes += len(raw)
				}
			} else {
				objects = append(objects, got)
			}
		}
		if isTrunk {
			begin := time.Now()
			if !waitForDurable {
				pendingIngress = make(chan ingressOutcome, 1)
				done := pendingIngress
				go func() {
					e := postPIL(ingressClient, ingress.URL, l, frames)
					done <- ingressOutcome{micros(time.Since(begin)), e}
				}()
			} else {
				err = postPIL(ingressClient, ingress.URL, l, frames)
				if err != nil {
					return
				}
				if first > warm {
					requests++
					ing = append(ing, micros(time.Since(begin)))
				}
			}
		}

		for revision := first; revision <= last; revision++ {
			if isTrunk && !waitForDurable {
				if revision > warm {
					totalBytes += len(frames[revision-first])
				}
				continue // the real store staging callback publishes on the server
			}
			var packet []byte
			if isTrunk {
				raw := frames[revision-first]
				if variant == "trunk-lookup" {
					var found []byte
					count := 0
					err = ep.History(ctx, l.Definition.Identity, 1, cache.Uint(1, 8), stamp(1, revision, 1), stamp(1, revision, 1).Add(time.Millisecond), func(_ cache.EventReference, b []byte) error { count++; found = bytes.Clone(b); return nil })
					if err != nil {
						return
					}
					if count != 1 || !bytes.Equal(raw, found) {
						err = fmt.Errorf("lookup did not return acknowledged revision")
						return
					}
					raw = found
				}
				packet = raw
			} else if variant == "pg-json" {
				raw, e := json.Marshal(objects[revision-first])
				if e != nil {
					err = e
					return
				}
				packet = append(raw, '\n')
			} else {
				packet = encode(l, objects[revision-first], 1, true)
			}
			published[revision] = time.Now()
			if err = clients.publish(packet); err != nil {
				return
			}
			if revision > warm {
				totalBytes += len(packet)
			}
		}
		seen := make(map[[2]int]bool)
		latest := map[int]time.Time{}
		deadline := time.NewTimer(30 * time.Second)
		for n := 0; n < (last-first+1)*subscribers; n++ {
			select {
			case outcome := <-pendingIngress:
				pendingIngress = nil
				if outcome.err != nil {
					err = outcome.err
					deadline.Stop()
					return
				}
				if first > warm {
					requests++
					ing = append(ing, outcome.elapsed)
				}
				n-- // durable acknowledgement is not a subscriber acknowledgement
			case ack := <-clients.acks:
				if ack.err != nil {
					err = ack.err
					return
				}
				key := [2]int{ack.subscriber, ack.revision}
				if ack.revision < first || ack.revision > last || seen[key] {
					err = fmt.Errorf("duplicate/out of order delivery")
					return
				}
				seen[key] = true
				if ack.revision > warm {
					cta = append(cta, micros(ack.applied.Sub(committed[ack.revision])))
					wta = append(wta, micros(ack.applied.Sub(started[ack.revision])))
					pta = append(pta, micros(ack.applied.Sub(published[ack.revision])))
					if ack.applied.After(latest[ack.revision]) {
						latest[ack.revision] = ack.applied
					}
				}
			case <-deadline.C:
				err = fmt.Errorf("subscription delivery timeout")
				return
			}
		}
		deadline.Stop()
		if pendingIngress != nil {
			outcome := <-pendingIngress
			pendingIngress = nil
			if outcome.err != nil {
				err = outcome.err
				return
			}
			if first > warm {
				requests++
				ing = append(ing, outcome.elapsed)
			}
		}
		for revision, applied := range latest {
			all = append(all, micros(applied.Sub(committed[revision])))
		}
	}
	runtime.ReadMemStats(&after)
	cpuEnd := cpuTime()
	measured := updates - warm
	result.Updates = measured
	result.CommitToApply = distribution(cta)
	result.WriteToApply = distribution(wta)
	result.PublishToApply = distribution(pta)
	result.AllApplied = distribution(all)
	result.Ingress = distribution(ing)
	result.OriginWrite = distribution(origin)
	result.StreamBytes = float64(totalBytes) / float64(measured)
	result.ReplicationBytes = float64(replicationBytes) / float64(measured)
	result.PGResultBytes = float64(pgBytes) / float64(measured)
	result.IngressRequests = requests
	result.GoBytes = float64(after.TotalAlloc-before.TotalAlloc) / float64(measured)
	result.GoAllocs = float64(after.Mallocs-before.Mallocs) / float64(measured)
	result.GoCPUus = float64(cpuEnd-cpuStart) / float64(measured)
	if variant == "trunk-direct" && subscribers == 1 && round == 1 && updates >= 100 {
		reconnect, err = reconnectCases(pg, ep, l, expected, textBytes)
	}
	return
}

func reconnectCases(pg *pgxpool.Pool, ep *cache.Endpoint, l *cache.Layout, states []Object, textBytes int) ([]reconnectResult, error) {
	last := len(states) - 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rev, e := strconv.Atoi(r.Header.Get("After-Revision"))
		if e != nil {
			http.Error(w, "revision", 400)
			return
		}
		w.Header().Set("Content-Type", "application/pil")
		if r.URL.Query().Get("source") == "postgres" {
			var o Object
			e = pg.QueryRow(r.Context(), "SELECT "+columns+" FROM "+streamTable+" WHERE id=1").Scan(&o.ID, &o.Revision, &o.State, &o.Amount, &o.Attempts, &o.Active, &o.CustomerID, &o.Currency, &o.Email, &o.Note, &o.Description)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			w.Write(encode(l, o, 1, true))
			return
		}
		e = ep.History(r.Context(), l.Definition.Identity, 1, cache.Uint(1, 8), stamp(1, rev, 1).Add(time.Millisecond), stamp(1, last+1, 1), func(_ cache.EventReference, raw []byte) error {
			var f cache.Frame
			if e := l.Scan(raw, &f); e != nil {
				return e
			}
			_, e := w.Write(raw)
			if e == nil {
				w.(http.Flusher).Flush()
			}
			return e
		})
		if e != nil {
			panic(e)
		}
	}))
	defer server.Close()
	client := server.Client()
	defer client.CloseIdleConnections()
	var results []reconnectResult
	for _, gap := range []int{1, 10, 100} {
		for _, mode := range []string{"postgres", "trunk"} {
			var latencies []float64
			size := 0
			for i := 0; i < 35; i++ {
				object := states[last-gap]
				request, _ := http.NewRequest("GET", server.URL+"/?source="+mode, nil)
				request.Header.Set("After-Revision", strconv.Itoa(last-gap))
				begin := time.Now()
				response, e := client.Do(request)
				if e != nil {
					return nil, e
				}
				if response.StatusCode != 200 {
					response.Body.Close()
					return nil, fmt.Errorf("reconnect status %d", response.StatusCode)
				}
				counted := &countReader{r: response.Body}
				var f cache.Frame
				seen := 0
				prior := last - gap
				e = consumeUpdates(l, counted, false, func(payload []byte) error {
					if err := apply(l, payload, &f, &object); err != nil {
						return err
					}
					revision := int(object.Revision)
					if (mode == "trunk" && revision != prior+1) || (mode == "postgres" && revision != last) {
						return fmt.Errorf("reconnect order")
					}
					prior = revision
					seen++
					return nil
				})
				elapsed := micros(time.Since(begin))
				response.Body.Close()
				if e != nil {
					return nil, e
				}
				if !reflect.DeepEqual(object, states[last]) || (mode == "trunk" && seen != gap) || (mode == "postgres" && seen != 1) {
					return nil, fmt.Errorf("reconnect state mismatch")
				}
				if i >= 5 {
					latencies = append(latencies, elapsed)
				}
				size = counted.n
			}
			results = append(results, reconnectResult{mode, gap, textBytes, len(latencies), distribution(latencies), size})
		}
	}
	return results, nil
}

type countReader struct {
	r io.Reader
	n int
}

func (c *countReader) Read(p []byte) (int, error) { n, e := c.r.Read(p); c.n += n; return n, e }

func runSubscriptions(dsn, root, output string, updates, rounds int, fanouts, sizes, variants string, durableBeforePublish bool) error {
	if updates < 32 || updates > 10000 || updates%8 != 0 || rounds < 1 {
		return fmt.Errorf("updates must be a multiple of 8 between 32 and 10000")
	}
	if _, e := os.Stat(root); !os.IsNotExist(e) {
		return fmt.Errorf("fresh benchmark root required")
	}
	subscribers, e := integers(fanouts)
	if e != nil {
		return e
	}
	texts, e := integers(sizes)
	if e != nil {
		return e
	}
	modes := strings.Split(variants, ",")
	for _, m := range modes {
		if m != "pg-json" && m != "pg-pil" && m != "trunk-lookup" && m != "trunk-direct" && m != "trunk-batch8" && m != "trunk-durable" {
			return fmt.Errorf("unknown variant %s", m)
		}
	}
	pg, e := pgxpool.New(ctx, dsn)
	if e != nil {
		return e
	}
	defer pg.Close()
	_, e = pg.Exec(ctx, "CREATE TABLE "+streamTable+" (id bigint PRIMARY KEY,revision_id integer NOT NULL,state text NOT NULL,amount integer NOT NULL,attempts smallint NOT NULL,active boolean NOT NULL,customer_id bigint NOT NULL,currency text NOT NULL,email text NOT NULL,note text,description text NOT NULL)")
	if e != nil {
		return e
	}
	report := subscriptionReport{Representation: "native-pil-http-stream-v1", Go: runtime.Version(), Updates: updates, Rounds: rounds}
	if e = pg.QueryRow(ctx, "SELECT version()").Scan(&report.PostgreSQL); e != nil {
		return e
	}
	report.PostgresSettings = make(map[string]string)
	for _, key := range []string{"fsync", "synchronous_commit", "wal_sync_method", "full_page_writes", "shared_buffers"} {
		var value string
		if e = pg.QueryRow(ctx, "SELECT current_setting($1)", key).Scan(&value); e != nil {
			return e
		}
		report.PostgresSettings[key] = value
	}
	for round := 1; round <= rounds; round++ {
		for _, textSize := range texts {
			for _, fanout := range subscribers {
				for at := range modes {
					mode := modes[(at+round-1)%len(modes)]
					path := filepath.Join(root, fmt.Sprintf("r%d_b%d_f%d_%s", round, textSize, fanout, mode))
					result, reconnect, e := subscriptionCase(pg, path, mode, round, fanout, textSize, updates, durableBeforePublish)
					if e != nil {
						return fmt.Errorf("%s: %w", path, e)
					}
					report.Results = append(report.Results, result)
					report.Reconnect = append(report.Reconnect, reconnect...)
					raw, e := json.MarshalIndent(report, "", "  ")
					if e != nil {
						return e
					}
					if e = os.WriteFile(output, append(raw, '\n'), 0644); e != nil {
						return e
					}
					fmt.Fprintf(os.Stderr, "round=%d bytes=%d subs=%d %s commit/apply p50=%.1fus p95=%.1fus body=%.0fB ingress=%.1fus\n", round, textSize, fanout, mode, result.CommitToApply.P50, result.CommitToApply.P95, result.StreamBytes, result.Ingress.P50)
				}
			}
		}
	}
	return nil
}
