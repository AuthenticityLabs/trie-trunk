// A benchmark adapter, not a new production object-materialization endpoint.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"trie-trunk/go/server/cache"
)

var ctx = context.Background()
var start = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
var states = []string{"pending", "authorized", "canceled"}

type Object struct {
	ID          int64   `json:"id"`
	Revision    int32   `json:"revision_id"`
	State       string  `json:"state"`
	Amount      int32   `json:"amount"`
	Attempts    int16   `json:"attempts"`
	Active      bool    `json:"active"`
	CustomerID  int64   `json:"customer_id"`
	Currency    string  `json:"currency"`
	Email       string  `json:"email"`
	Note        *string `json:"note"`
	Description string  `json:"description"`
}

func fixture(id int64, rev, textSize int) Object {
	o := Object{ID: id, Revision: 0, State: states[0], Amount: 12300 + int32(id%1000), Attempts: 0, Active: true, CustomerID: id + 1000000, Currency: "USD", Email: fmt.Sprintf("customer-%d@example.test", id)}
	// Deterministic distinct text rather than a run of compressible padding.
	for n := 0; len(o.Description) < textSize; n++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%d/%d", id, n)))
		o.Description += hex.EncodeToString(h[:])
	}
	o.Description = o.Description[:textSize]
	note := "Initial order"
	o.Note = &note
	for r := 1; r <= rev; r++ {
		mutate(&o, r)
	}
	return o
}
func mutate(o *Object, r int) {
	o.Revision = int32(r)
	switch r % 3 {
	case 0:
		o.State = states[r/3%3]
		o.Amount = 12300 + int32(o.ID%1000) + int32(r)
	case 1:
		o.Attempts = int16(r)
		o.Active = r%2 == 0
	case 2:
		if r%2 == 0 {
			o.Note = nil
		} else {
			s := fmt.Sprintf("Review %d", r)
			o.Note = &s
		}
		o.State = states[r%3]
	}
}
func layout() *cache.Layout {
	l, e := cache.Compile(cache.Definition{PIL: 1, AdaptiveIntegers: "16", Identity: "payment/transactions", Version: 1 << 20, Fields: []cache.Field{
		{Name: "event_id", Type: "UUID"}, {Name: "id", Type: "U64"}, {Name: "revision_id", Type: "U32"},
		{Name: "state", Type: "STRING", Values: states}, {Name: "amount", Type: "U32"}, {Name: "attempts", Type: "U16"},
		{Name: "active", Type: "BOOL"}, {Name: "customer_id", Type: "U64"}, {Name: "currency", Type: "STRING", Values: []string{"USD"}},
		{Name: "email", Type: "STRING"}, {Name: "note", Type: "STRING"}, {Name: "description", Type: "STRING"},
	}, Nuts: cache.Binding{Format: cache.Format, Domain: "payment", Collection: "transactions", Major: 1, Timezone: "UTC", Roles: cache.Roles{Event: "event_id", Object: "id", Revision: "revision_id"}, Path: []string{"state", "currency"}}})
	must(e)
	return l
}
func stamp(id int64, rev, objects int) time.Time {
	return start.Add(time.Duration(int64(rev)*int64(objects)+id) * time.Millisecond)
}
func encode(l *cache.Layout, o Object, objects int, forceFull bool) []byte {
	v := make([]cache.Value, len(l.Widths))
	var uuid [16]byte
	binary.BigEndian.PutUint64(uuid[:8], uint64(stamp(o.ID, int(o.Revision), objects).UnixMilli())<<16)
	uuid[6] = 0x70
	uuid[8] = 0x80
	binary.BigEndian.PutUint32(uuid[12:], uint32(o.ID))
	v[0] = cache.Value{State: cache.Direct, Bytes: uuid[:]}
	v[1] = cache.Uint(uint64(o.ID), 8)
	v[2] = cache.Uint(uint64(o.Revision), 4)
	full := forceFull || o.Revision == 0
	r := int(o.Revision)
	if full || r%3 == 0 || r%3 == 2 {
		for i, s := range states {
			if s == o.State {
				v[3] = cache.Uint(uint64(i), 1)
			}
		}
	}
	if full || r%3 == 0 {
		v[4] = cache.Uint(uint64(o.Amount), 4)
	}
	if full || r%3 == 1 {
		v[5] = cache.Uint(uint64(o.Attempts), 2)
		b := uint64(0)
		if o.Active {
			b = 1
		}
		v[6] = cache.Uint(b, 1)
	}
	if full {
		v[7] = cache.Uint(uint64(o.CustomerID), 8)
		v[8] = cache.Uint(0, 1)
		v[9] = cache.Text(o.Email)
		v[11] = cache.Text(o.Description)
	}
	if full || r%3 == 2 {
		if o.Note == nil {
			v[10] = cache.Value{State: cache.Null}
		} else {
			v[10] = cache.Text(*o.Note)
		}
	}
	raw, e := l.Encode(nil, v)
	must(e)
	return raw
}
func number(v cache.Value) uint64 {
	var b [8]byte
	copy(b[:], v.Bytes)
	return binary.LittleEndian.Uint64(b[:])
}
func apply(l *cache.Layout, raw []byte, f *cache.Frame, o *Object) error {
	if e := l.Scan(raw, f); e != nil {
		return e
	}
	for i, v := range f.Values {
		if v.State == cache.Absent {
			continue
		}
		switch i {
		case 1:
			o.ID = int64(number(v))
		case 2:
			o.Revision = int32(number(v))
		case 3:
			o.State = states[number(v)]
		case 4:
			o.Amount = int32(number(v))
		case 5:
			o.Attempts = int16(number(v))
		case 6:
			o.Active = number(v) == 1
		case 7:
			o.CustomerID = int64(number(v))
		case 8:
			o.Currency = "USD"
		case 9:
			o.Email = string(v.Bytes)
		case 10:
			if v.State == cache.Null {
				o.Note = nil
			} else {
				s := string(v.Bytes)
				o.Note = &s
			}
		case 11:
			o.Description = string(v.Bytes)
		}
	}
	return nil
}

const columns = "id,revision_id,state,amount,attempts,active,customer_id,currency,email,note,description"

func pgRead(pool *pgxpool.Pool, id int64) (Object, error) {
	var o Object
	e := pool.QueryRow(ctx, "SELECT "+columns+" FROM replay_bench_objects WHERE id=$1", id).Scan(&o.ID, &o.Revision, &o.State, &o.Amount, &o.Attempts, &o.Active, &o.CustomerID, &o.Currency, &o.Email, &o.Note, &o.Description)
	return o, e
}

type Suite struct {
	l                            *cache.Layout
	ep                           *cache.Endpoint
	pg                           *pgxpool.Pool
	objects, revisions, textSize int
	expected, base1, base10      []Object
}

func (s *Suite) replay(id int64, missing int) (Object, error) {
	o := Object{}
	from := start
	if missing >= 0 {
		baseRev := s.revisions - 1 - missing
		if baseRev >= 0 {
			if missing == 1 {
				o = s.base1[id-1]
			} else if missing == 10 {
				o = s.base10[id-1]
			} else {
				o = s.expected[id-1]
			}
			from = stamp(id, baseRev, s.objects).Add(time.Millisecond)
		}
	}
	var f cache.Frame
	e := s.ep.History(ctx, s.l.Definition.Identity, 1, cache.Uint(uint64(id), 8), from, stamp(int64(s.objects), s.revisions, s.objects), func(_ cache.EventReference, raw []byte) error { return apply(s.l, raw, &f, &o) })
	return o, e
}
func (s *Suite) historyURL(id int64, missing int) string {
	from := start
	if missing >= 0 {
		from = stamp(id, s.revisions-1-missing, s.objects).Add(time.Millisecond)
	}
	q := url.Values{"collection": {"payment/transactions.v1"}, "id": {fmt.Sprint(id)}, "from": {from.Format(time.RFC3339Nano)}, "to": {stamp(int64(s.objects), s.revisions, s.objects).Format(time.RFC3339Nano)}}
	return "/objects/history?" + q.Encode()
}

// Parses the actual production TPIL stream with the definition already cached.
func (s *Suite) decodeHistory(raw []byte, o Object) (Object, error) {
	if !bytes.HasPrefix(raw, []byte{'T', 'P', 'I', 'L', 2}) {
		return o, fmt.Errorf("bad history prefix: %.120s", raw)
	}
	r := bytes.NewReader(raw[5:])
	var f cache.Frame
	bind := &cache.Layout{Definition: cache.Definition{Fields: []cache.Field{{Type: "U32"}, {Type: "BYTES"}, {Type: "STRING"}}}, Widths: []int{4, 0, 0}}
	var reuse []byte
	for {
		tag, e := r.ReadByte()
		if e != nil {
			return o, e
		}
		switch tag {
		case 2:
			reuse, e = bind.ReadFrame(r, reuse)
			if e != nil {
				return o, e
			}
			var b cache.Frame
			if e = bind.Scan(reuse, &b); e != nil {
				return o, e
			}
			if !bytes.Equal(b.Values[1].Bytes, s.l.Digest[:]) {
				return o, fmt.Errorf("definition mismatch")
			}
		case 3:
			var id [4]byte
			if _, e = io.ReadFull(r, id[:]); e != nil {
				return o, e
			}
			if binary.LittleEndian.Uint32(id[:]) != 0 {
				return o, fmt.Errorf("unknown stream")
			}
			reuse, e = s.l.ReadFrame(r, reuse)
			if e != nil {
				return o, e
			}
			if e = apply(s.l, reuse, &f, &o); e != nil {
				return o, e
			}
		case 255:
			last, e := r.ReadByte()
			if e != nil || last != 254 || r.Len() != 0 {
				return o, fmt.Errorf("incomplete history")
			}
			return o, nil
		default:
			return o, fmt.Errorf("unexpected tag %d", tag)
		}
	}
}

type Result struct {
	Name       string  `json:"name"`
	N          int     `json:"n"`
	P50us      float64 `json:"p50_us"`
	P95us      float64 `json:"p95_us"`
	P99us      float64 `json:"p99_us"`
	Meanus     float64 `json:"mean_us"`
	OpsSec     float64 `json:"ops_sec"`
	GoBytesOp  float64 `json:"go_bytes_op"`
	GoAllocsOp float64 `json:"go_allocs_op"`
	BodyBytes  float64 `json:"response_body_bytes"`
}
type op func(int64) (Object, int, error)

func (s *Suite) measure(name string, n, concurrency int, fn op) Result {
	ids := make([]int64, n)
	rng := rand.New(rand.NewSource(42))
	for i := range ids {
		ids[i] = int64(rng.Intn(s.objects) + 1)
	}
	// Validate every measured output against independently generated final state.
	run := func(id int64) int {
		o, b, e := fn(id)
		must(e)
		if !reflect.DeepEqual(o, s.expected[id-1]) {
			panic(fmt.Sprintf("%s wrong result id %d rev %d/%d", name, id, o.Revision, s.expected[id-1].Revision))
		}
		return b
	}
	for i := 0; i < min(40, n); i++ {
		run(ids[i])
	}
	runtime.GC()
	dur := make([]int64, n)
	sizes := make([]int, n)
	var next atomic.Int64
	var wg sync.WaitGroup
	begin := time.Now()
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				t := time.Now()
				o, b, e := fn(ids[i])
				dur[i] = time.Since(t).Nanoseconds()
				must(e)
				if !reflect.DeepEqual(o, s.expected[ids[i]-1]) {
					panic("result mismatch")
				}
				sizes[i] = b
			}
		}()
	}
	wg.Wait()
	wall := time.Since(begin)
	// Separate allocation pass excludes output validation, latency slices and GC setup.
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	for i := 0; i < min(50, n); i++ {
		_, _, e := fn(ids[i])
		must(e)
	}
	runtime.ReadMemStats(&b)
	sort.Slice(dur, func(i, j int) bool { return dur[i] < dur[j] })
	var total int64
	var size int
	for i, v := range dur {
		total += v
		size += sizes[i]
	}
	result := Result{name, n, float64(dur[n/2]) / 1000, float64(dur[min(n-1, n*95/100)]) / 1000, float64(dur[min(n-1, n*99/100)]) / 1000, float64(total) / float64(n) / 1000, float64(n) / wall.Seconds(), float64(b.TotalAlloc-a.TotalAlloc) / float64(min(50, n)), float64(b.Mallocs-a.Mallocs) / float64(min(50, n)), float64(size) / float64(n)}
	fmt.Fprintf(os.Stderr, "%s p50=%.1fus p95=%.1fus bytes=%.0f\n", name, result.P50us, result.P95us, result.BodyBytes)
	return result
}

type Report struct {
	TrunkOpenSeconds   float64           `json:"trunk_open_seconds"`
	TrunkOpenHeapBytes int64             `json:"trunk_open_heap_bytes"`
	WindowAge          string            `json:"window_age"`
	Reader             string            `json:"reader"`
	ReusedFixture      bool              `json:"reused_fixture"`
	Objects            int               `json:"objects"`
	Revisions          int               `json:"revisions_per_object"`
	TextSize           int               `json:"description_bytes"`
	Events             int               `json:"events"`
	PostgreSQL         string            `json:"postgresql"`
	Go                 string            `json:"go"`
	PGSettings         map[string]string `json:"postgresql_settings"`
	PGBytes            int64             `json:"postgresql_table_and_indexes_bytes"`
	TrunkBytes         map[string]int64  `json:"trunk_files_bytes"`
	TrunkSeedSeconds   float64           `json:"trunk_seed_seconds"`
	PGSeedSeconds      float64           `json:"pg_copy_current_rows_seconds"`
	Results            []Result          `json:"results"`
	Explain            []string          `json:"explain"`
}

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	dsn := flag.String("dsn", "postgres://jason@127.0.0.1:15439/postgres?sslmode=disable", "dedicated benchmark database only")
	root := flag.String("root", "/private/tmp/trie-replay-bench/trunk", "empty benchmark storage root")
	output := flag.String("output", "result.json", "output report")
	objects := flag.Int("objects", 10000, "number of objects")
	revs := flag.Int("revisions", 10, "one full creation plus sparse updates per object")
	textSize := flag.Int("text-bytes", 256, "unchanged high-cardinality description size")
	samples := flag.Int("samples", 300, "samples per case")
	rounds := flag.Int("rounds", 3, "repeat rounds with rotated case order")
	reuse := flag.Bool("reuse", false, "read an existing benchmark fixture without reseeding")
	reader := flag.String("reader", "production", "benchmark build label")
	age := flag.Duration("age", time.Hour, "clock offset after last event: 1m live tail, 1h resident, 48h cold")
	subscriptions := flag.Bool("subscriptions", false, "run persistent binary PIL publication benchmark")
	fanouts := flag.String("fanouts", "1,10,100", "HTTP stream subscriber counts")
	streamSizes := flag.String("stream-sizes", "256,4096", "unchanged description sizes")
	streamVariants := flag.String("stream-variants", "pg-pil,trunk-durable,trunk-direct", "HTTP stream publisher variants")
	durableBeforePublish := flag.Bool("durable-before-publish", false, "wait for durable ingress before publication (false requires staged observer build)")
	compression := flag.Bool("stream-compression", false, "measure streaming gzip of prepared native PIL packets without PostgreSQL")
	compressionFixtures := flag.String("compression-fixtures", "", "export prepared compression fixtures to a new directory without PostgreSQL")
	compressionFormat := flag.String("compression-format", "pil", "compression fixture representation (pil only)")
	flag.Parse()
	if *compressionFixtures != "" {
		must(exportCompressionFixtures(*compressionFixtures, *compressionFormat))
		return
	}
	if *compression {
		must(runStreamCompression(*output))
		return
	}
	if *subscriptions {
		must(runSubscriptions(*dsn, *root, *output, *samples, *rounds, *fanouts, *streamSizes, *streamVariants, *durableBeforePublish))
		return
	}
	if *revs < 1 || *revs > 30000 || *objects < 1 {
		panic("invalid fixture size")
	}
	if _, e := os.Stat(*root); !*reuse && !os.IsNotExist(e) {
		panic("root must not exist; refusing to overwrite")
	}
	pc, e := pgxpool.ParseConfig(*dsn)
	must(e)
	pc.MaxConns = 8
	pg, e := pgxpool.NewWithConfig(ctx, pc)
	must(e)
	defer pg.Close()
	// Establish the entire pool before timing, including concurrent runs.
	var connections []*pgxpool.Conn
	for i := 0; i < 8; i++ {
		c, e := pg.Acquire(ctx)
		must(e)
		connections = append(connections, c)
	}
	for _, c := range connections {
		c.Release()
	}
	// Never remove an existing table: reruns use a fresh isolated cluster/database.
	if !*reuse {
		_, e = pg.Exec(ctx, `CREATE TABLE replay_bench_objects (id bigint PRIMARY KEY, revision_id integer NOT NULL, state text NOT NULL, amount integer NOT NULL, attempts smallint NOT NULL, active boolean NOT NULL, customer_id bigint NOT NULL, currency text NOT NULL, email text NOT NULL, note text, description text NOT NULL)`)
		must(e)
	}
	s := Suite{l: layout(), pg: pg, objects: *objects, revisions: *revs, textSize: *textSize}
	report := Report{WindowAge: age.String(), Reader: *reader, ReusedFixture: *reuse, Objects: *objects, Revisions: *revs, TextSize: *textSize, Events: *objects * *revs, Go: runtime.Version(), PGSettings: map[string]string{}, TrunkBytes: map[string]int64{}}
	must(pg.QueryRow(ctx, "SELECT version()").Scan(&report.PostgreSQL))
	for _, name := range []string{"shared_buffers", "fsync", "synchronous_commit", "full_page_writes", "block_size", "max_connections"} {
		var v string
		must(pg.QueryRow(ctx, "SHOW "+name).Scan(&v))
		report.PGSettings[name] = v
	}
	// PostgreSQL stores current objects. Trie-Trunk stores complete creation + all changes.
	current := make([]Object, *objects)
	s.expected = make([]Object, *objects)
	s.base1 = make([]Object, *objects)
	s.base10 = make([]Object, *objects)
	for i := range current {
		current[i] = fixture(int64(i+1), 0, *textSize)
		s.expected[i] = fixture(int64(i+1), *revs-1, *textSize)
		s.base1[i] = fixture(int64(i+1), max(0, *revs-2), *textSize)
		s.base10[i] = fixture(int64(i+1), max(0, *revs-11), *textSize)
	}
	now := start.Add(-time.Hour)
	cfg := cache.Config{Root: *root, Cache: "benchmark", Tenant: "benchmark", Layout: s.l, ObjectIndex: true, Now: func() time.Time { return now }}
	if !*reuse {
		store, e := cache.Open(cfg)
		must(e)
		begin := time.Now()
		batch := make([][]byte, 0, 1024)
		flush := func() {
			if len(batch) > 0 {
				n, e := store.AppendBatch(cache.Write, batch)
				must(e)
				if n != len(batch) {
					panic("short append")
				}
				batch = batch[:0]
			}
		}
		for r := 0; r < *revs; r++ {
			for i := range current {
				if r > 0 {
					mutate(&current[i], r)
				}
				batch = append(batch, encode(s.l, current[i], *objects, false))
				if len(batch) == cap(batch) {
					flush()
				}
			}
			if r%max(1, *revs/10) == 0 {
				fmt.Fprintf(os.Stderr, "seed revision %d/%d\n", r, *revs)
			}
		}
		flush()
		now = stamp(int64(*objects), *revs, *objects).Add(*age)
		must(store.Close())
		report.TrunkSeedSeconds = time.Since(begin).Seconds()
	}
	now = stamp(int64(*objects), *revs, *objects).Add(*age)
	runtime.GC()
	var beforeOpen, afterOpen runtime.MemStats
	runtime.ReadMemStats(&beforeOpen)
	openStart := time.Now()
	s.ep, e = cache.OpenEndpoint(cfg)
	must(e)
	report.TrunkOpenSeconds = time.Since(openStart).Seconds()
	runtime.GC()
	runtime.ReadMemStats(&afterOpen)
	report.TrunkOpenHeapBytes = int64(afterOpen.HeapAlloc) - int64(beforeOpen.HeapAlloc)
	defer s.ep.Close()
	if !*reuse {
		begin := time.Now()
		rows := make([][]any, *objects)
		for i, o := range s.expected {
			rows[i] = []any{o.ID, o.Revision, o.State, o.Amount, o.Attempts, o.Active, o.CustomerID, o.Currency, o.Email, o.Note, o.Description}
		}
		_, e = pg.CopyFrom(ctx, pgx.Identifier{"replay_bench_objects"}, []string{"id", "revision_id", "state", "amount", "attempts", "active", "customer_id", "currency", "email", "note", "description"}, pgx.CopyFromRows(rows))
		must(e)
		report.PGSeedSeconds = time.Since(begin).Seconds()
		_, e = pg.Exec(ctx, "VACUUM ANALYZE replay_bench_objects")
		must(e)
	}
	var rowCount int
	var minRev, maxRev int32
	must(pg.QueryRow(ctx, "SELECT count(*),min(revision_id),max(revision_id) FROM replay_bench_objects").Scan(&rowCount, &minRev, &maxRev))
	if rowCount != *objects || minRev != int32(*revs-1) || maxRev != minRev {
		panic("fixture dimensions mismatch")
	}
	must(pg.QueryRow(ctx, "SELECT pg_total_relation_size('replay_bench_objects')").Scan(&report.PGBytes))
	erows, e := pg.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) SELECT "+columns+" FROM replay_bench_objects WHERE id=42")
	must(e)
	for erows.Next() {
		var line string
		must(erows.Scan(&line))
		report.Explain = append(report.Explain, line)
	}
	must(erows.Err())
	erows.Close()
	must(filepath.WalkDir(*root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			report.TrunkBytes[d.Name()] += info.Size()
		}
		return nil
	}))
	mux := http.NewServeMux()
	mux.Handle("/objects/", s.ep)
	// Both full-object adapters use the same Go JSON encoder and same client decoder.
	mux.HandleFunc("/current", func(w http.ResponseWriter, r *http.Request) {
		id, e := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		var o Object
		if r.URL.Query().Get("source") == "postgres" {
			o, e = pgRead(pg, id)
		} else {
			o, e = s.replay(id, -1)
		}
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if e = json.NewEncoder(w).Encode(o); e != nil {
			panic(e)
		}
	})
	mux.HandleFunc("/current-pil", func(w http.ResponseWriter, r *http.Request) {
		id, e := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		o, e := pgRead(pg, id)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/pil")
		_, e = w.Write(encode(s.l, o, s.objects, true))
		must(e)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := server.Client()
	client.Timeout = 30 * time.Second
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	transport.MaxIdleConns = 16
	client.Transport = transport
	defer transport.CloseIdleConnections()
	get := func(path string, history bool) ([]byte, error) {
		r, e := http.NewRequest("GET", server.URL+path, nil)
		if e != nil {
			return nil, e
		}
		if history {
			r.Header.Set("Accept", "application/pil")
			r.Header.Set("PIL-Version", "2")
			r.Header.Set("PIL-Features", "adaptive-integers")
			r.Header.Set("Definition-Known", fmt.Sprintf("%x", s.l.Digest))
		}
		res, e := client.Do(r)
		if e != nil {
			return nil, e
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil {
			return nil, e
		}
		if res.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, b)
		}
		if history && res.Trailer.Get("Trunk-Status") != "ok" {
			return nil, fmt.Errorf("incomplete history trailer")
		}
		return b, nil
	}
	fullHTTP := func(source string) op {
		return func(id int64) (Object, int, error) {
			raw, e := get("/current?source="+source+"&id="+fmt.Sprint(id), false)
			var o Object
			if e == nil {
				e = json.Unmarshal(raw, &o)
			}
			return o, len(raw), e
		}
	}
	historyHTTP := func(missing int) op {
		return func(id int64) (Object, int, error) {
			raw, e := get(s.historyURL(id, missing), true)
			var o Object
			if missing == 1 {
				o = s.base1[id-1]
			} else if missing == 10 {
				o = s.base10[id-1]
			} else if missing == 0 {
				o = s.expected[id-1]
			}
			if e == nil {
				o, e = s.decodeHistory(raw, o)
			}
			return o, len(raw), e
		}
	}
	cases := []struct {
		name string
		fn   op
	}{
		{"postgres_direct_current", func(id int64) (Object, int, error) { o, e := pgRead(pg, id); return o, 0, e }},
		{"trunk_native_full_replay", func(id int64) (Object, int, error) { o, e := s.replay(id, -1); return o, 0, e }},
		{"postgres_http_current_pil", func(id int64) (Object, int, error) {
			raw, e := get("/current-pil?id="+fmt.Sprint(id), false)
			var o Object
			var f cache.Frame
			if e == nil {
				e = apply(s.l, raw, &f, &o)
			}
			return o, len(raw), e
		}},
		{"postgres_http_current_json", fullHTTP("postgres")}, {"trunk_http_reconstructed_json", fullHTTP("trunk")},
		{"trunk_http_full_history_pil", historyHTTP(-1)},
		{"trunk_http_no_changes_pil", historyHTTP(0)},
		{"local_materialized_object", func(id int64) (Object, int, error) { return s.expected[id-1], 0, nil }},
	}
	if *revs > 1 {
		cases = append(cases, struct {
			name string
			fn   op
		}{"trunk_native_delta1", func(id int64) (Object, int, error) { o, e := s.replay(id, 1); return o, 0, e }}, struct {
			name string
			fn   op
		}{"trunk_http_delta1_pil", historyHTTP(1)})
	}
	if *revs > 10 {
		cases = append(cases, struct {
			name string
			fn   op
		}{"trunk_http_delta10_pil", historyHTTP(10)})
	}
	for round := 0; round < *rounds; round++ {
		for j := range cases {
			c := cases[(j+round*3)%len(cases)]
			report.Results = append(report.Results, s.measure(fmt.Sprintf("%s/round%d", c.name, round+1), *samples, 1, c.fn))
		}
		for _, c := range cases {
			if c.name == "postgres_http_current_json" || c.name == "trunk_http_reconstructed_json" || c.name == "trunk_http_delta1_pil" {
				report.Results = append(report.Results, s.measure(fmt.Sprintf("%s_concurrency8/round%d", c.name, round+1), *samples, 8, c.fn))
			}
		}
	}
	raw, e := json.MarshalIndent(report, "", "  ")
	must(e)
	must(os.WriteFile(*output, append(raw, '\n'), 0644))
}
