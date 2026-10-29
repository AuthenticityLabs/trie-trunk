// bench-retrieval measures the running API with a native Go HTTP client.
// It downloads unchanged summary/seed-pack bodies; it does not render a UI or
// simulate OPFS. HTTP protocol is checked on every response, never inferred.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"github.com/quic-go/quic-go/http3"
)

type binding struct{ Source, Definition string }
type day struct {
	Key        string `json:"key"`
	Source     string `json:"source"`
	Definition string `json:"definition"`
}
type reference struct {
	Stream uint32
	Key    string
	Cached bool
	Native *struct {
		From, To   int64
		Generation string
	}
}
type view struct {
	Snapshot, SummaryVersion, Next string
	Page                           []reference
	Analytics                      *struct{ Days []day }
	Summary                        *struct{ Count uint64 }
}
type request struct {
	Path, Stage string
	Headers     http.Header
	Digests     []string
	Body        []byte
}
type measurement struct {
	Stage, Path, Protocol           string
	HeadersMS, BodyMS               float64
	WireBodyBytes, DecodedBodyBytes int
}
type sample struct {
	Scenario                                                 string
	Trial                                                    int
	TotalMS, SearchMS, SummariesMS, PacksMS, KnownMetadataMS float64
	Rows                                                     int
	SummaryPackets, SeedPacks                                int
	PageIdentity                                             string
	Requests                                                 []measurement
}
type scenario struct {
	Name, Tap, From, To, Include, Query string
	Page                                int
}
type runner struct {
	origin             string
	client             *http.Client
	major, concurrency int
	overlap            bool
}

var baseHeaders = http.Header{"Accept": {"application/pil"}, "Pil-Version": {"2"}, "Pil-Features": {"pac-arrays,adaptive-integers,view-buckets,daily-summaries"}, "Accept-Encoding": {"gzip"}}

func ms(t time.Time) float64 { return float64(time.Since(t)) / float64(time.Millisecond) }
func (r runner) get(ctx context.Context, q request) ([]byte, measurement, error) {
	m := measurement{Stage: q.Stage, Path: strings.Split(q.Path, "?")[0]}
	started := time.Now()
	method := "GET"
	if q.Body != nil {
		method = "POST"
	}
	req, e := http.NewRequestWithContext(ctx, method, r.origin+q.Path, bytes.NewReader(q.Body))
	if e != nil {
		return nil, m, e
	}
	req.Header = q.Headers.Clone()
	res, e := r.client.Do(req)
	if e != nil {
		return nil, m, e
	}
	defer res.Body.Close()
	m.HeadersMS = ms(started)
	m.Protocol = res.Proto
	if res.ProtoMajor != r.major {
		return nil, m, fmt.Errorf("requested HTTP/%d, received %s", r.major, res.Proto)
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 128<<20))
	m.BodyMS = ms(started)
	m.WireBodyBytes = len(raw)
	if e != nil {
		return nil, m, e
	}
	if res.StatusCode != 200 {
		return nil, m, fmt.Errorf("%s HTTP %d: %.200s", m.Path, res.StatusCode, raw)
	}
	if res.Header.Get("Content-Encoding") == "gzip" {
		g, e := gzip.NewReader(bytes.NewReader(raw))
		if e != nil {
			return nil, m, e
		}
		raw, e = io.ReadAll(io.LimitReader(g, 128<<20))
		g.Close()
		if e != nil {
			return nil, m, e
		}
	}
	m.DecodedBodyBytes = len(raw)
	if len(q.Digests) > 0 {
		var packs [][]byte
		if err := decodeSummaryPacks(raw, &packs); err != nil {
			return nil, m, err
		}
		if len(packs) != len(q.Digests) {
			return nil, m, fmt.Errorf("summary array count mismatch")
		}
		for i, pack := range packs {
			sum := sha256.Sum256(pack)
			if hex.EncodeToString(sum[:]) != q.Digests[i] {
				return nil, m, fmt.Errorf("summary digest mismatch")
			}
		}
	}
	if m.Path == "/collect" && (!bytes.HasPrefix(raw, []byte{'T', 'P', 'I', 'L', 2}) || !bytes.HasSuffix(raw, []byte{255, 254})) {
		return nil, m, fmt.Errorf("incomplete native seed-pack response")
	}
	return raw, m, nil
}

// This benchmark accepts only definition/binding/view controls and sealed
// references. Unexpected live seed payloads fail instead of hiding extra work.
type parser struct {
	raw []byte
	at  int
}

func (p *parser) take(n int) ([]byte, error) {
	if n < 0 || n > len(p.raw)-p.at {
		return nil, io.ErrUnexpectedEOF
	}
	b := p.raw[p.at : p.at+n]
	p.at += n
	return b, nil
}
func (p *parser) uint(n int) (uint64, error) {
	b, e := p.take(n)
	if e != nil {
		return 0, e
	}
	var v uint64
	for i := n - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v, nil
}
func (p *parser) extended() (uint64, error) {
	v, e := p.uint(2)
	if e != nil || v < 65535 {
		return v, e
	}
	v, e = p.uint(4)
	if e != nil || v < 0xffffffff {
		return v, e
	}
	return p.uint(8)
}
func parse(raw []byte) (view, map[uint32]binding, error) {
	var v view
	bindings := map[uint32]binding{}
	p := parser{raw: raw}
	magic, e := p.take(5)
	if e != nil || !bytes.Equal(magic, []byte{'T', 'P', 'I', 'L', 2}) {
		return v, nil, fmt.Errorf("invalid PIL stream")
	}
	found := false
	for p.at < len(raw) {
		tag, e := p.uint(1)
		if e != nil {
			return v, nil, e
		}
		if tag == 255 {
			end, e := p.uint(1)
			if e != nil || end != 254 || p.at != len(raw) || !found {
				return v, nil, fmt.Errorf("invalid completion")
			}
			return v, bindings, nil
		}
		mask, e := p.uint(1)
		if e != nil {
			return v, nil, e
		}
		switch tag {
		case 1:
			if mask != 0xfc {
				return v, nil, fmt.Errorf("invalid definition mask")
			}
			var end uint64
			for i := 0; i < 3; i++ {
				n, e := p.extended()
				if e != nil || n < end {
					return v, nil, fmt.Errorf("invalid definition offsets")
				}
				end = n
			}
			if _, e = p.take(int(end)); e != nil {
				return v, nil, e
			}
		case 2:
			if mask != 0xbc {
				return v, nil, fmt.Errorf("invalid binding mask")
			}
			id, e := p.uint(4)
			if e != nil {
				return v, nil, e
			}
			a, e := p.extended()
			if e != nil || a != 32 {
				return v, nil, fmt.Errorf("invalid binding digest")
			}
			end, e := p.extended()
			if e != nil || end < 32 {
				return v, nil, fmt.Errorf("invalid binding end")
			}
			b, e := p.take(int(end))
			if e != nil {
				return v, nil, e
			}
			bindings[uint32(id)] = binding{string(b[32:]), hex.EncodeToString(b[:32])}
		case 8:
			if mask != 0xc0 || found {
				return v, nil, fmt.Errorf("invalid view control")
			}
			n, e := p.extended()
			if e != nil {
				return v, nil, e
			}
			b, e := p.take(int(n))
			if e != nil {
				return v, nil, e
			}
			if e = json.Unmarshal(b, &v); e != nil {
				return v, nil, e
			}
			found = true
		default:
			return v, nil, fmt.Errorf("unsupported control %d: sealed API benchmark only", tag)
		}
	}
	return v, nil, io.ErrUnexpectedEOF
}
func (r runner) batch(ctx context.Context, qs []request) ([]measurement, error) {
	result := make([]measurement, len(qs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for w := 0; w < min(r.concurrency, len(qs)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				_, m, e := r.get(ctx, qs[i])
				result[i] = m
				if e != nil {
					once.Do(func() { first = e; cancel() })
				}
			}
		}()
	}
	for i := range qs {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return result, first
}
func (r runner) run(ctx context.Context, s scenario, trial int) (sample, error) {
	result := sample{Scenario: s.Name, Trial: trial}
	started := time.Now()
	params := url.Values{"tap": {s.Tap}, "from": {s.From}, "to": {s.To}, "page": {fmt.Sprint(s.Page)}, "include": {s.Include}}
	if s.Query != "" {
		params.Set("query", s.Query)
	}
	search := request{Path: "/events?" + params.Encode(), Stage: "search", Headers: baseHeaders}
	raw, m, e := r.get(ctx, search)
	if e != nil {
		return result, e
	}
	result.Requests = append(result.Requests, m)
	v, bindings, e := parse(raw)
	if e != nil {
		return result, e
	}
	result.SearchMS = ms(started)
	result.Rows = len(v.Page)
	keys := []string{}
	for _, ref := range v.Page {
		keys = append(keys, ref.Key)
	}
	hash := sha256.Sum256([]byte(strings.Join(keys, ",")))
	result.PageIdentity = hex.EncodeToString(hash[:])
	defs := []string{}
	seenDefs := map[string]bool{}
	for _, b := range bindings {
		if !seenDefs[b.Definition] {
			defs = append(defs, b.Definition)
			seenDefs[b.Definition] = true
		}
	}
	sort.Strings(defs)
	var summaries, packs []request
	if v.Analytics != nil {
		result.SummaryPackets = len(v.Analytics.Days)
		for start := 0; start < len(v.Analytics.Days); start += 1024 {
			days := v.Analytics.Days[start:min(start+1024, len(v.Analytics.Days))]
			body, err := json.Marshal(struct {
				Packs []day `json:"packs"`
			}{days})
			if err != nil {
				return result, err
			}
			digests := make([]string, len(days))
			for i, d := range days {
				digests[i] = d.Key
			}
			summaries = append(summaries, request{Path: "/summaries", Stage: "summary", Headers: http.Header{"Content-Type": {"application/json"}, "Accept": {"application/pil"}}, Body: body, Digests: digests})
		}
	}
	seen := map[string]bool{}
	for _, ref := range v.Page {
		if ref.Cached {
			continue
		}
		if ref.Native == nil {
			return result, fmt.Errorf("live reference not supported")
		}
		b, ok := bindings[ref.Stream]
		if !ok {
			return result, fmt.Errorf("unknown page binding")
		}
		n := ref.Native
		path := "/collect?" + url.Values{"tap": {b.Source}, "from": {time.UnixMilli(n.From).UTC().Format(time.RFC3339Nano)}, "to": {time.UnixMilli(n.To).UTC().Format(time.RFC3339Nano)}, "definition": {b.Definition}, "format": {"native"}}.Encode()
		if seen[path] {
			continue
		}
		seen[path] = true
		h := baseHeaders.Clone()
		h.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
		h.Set("Definition-Version", b.Definition)
		h.Set("Definition-Known", strings.Join(defs, ","))
		packs = append(packs, request{Path: path, Stage: "seed-pack", Headers: h})
	}
	result.SeedPacks = len(packs)
	if r.overlap {
		all := append(summaries, packs...)
		more, e := r.batch(ctx, all)
		if e != nil {
			return result, e
		}
		result.Requests = append(result.Requests, more...)
	} else {
		start := time.Now()
		more, e := r.batch(ctx, summaries)
		if e != nil {
			return result, e
		}
		result.SummariesMS = ms(start)
		result.Requests = append(result.Requests, more...)
		start = time.Now()
		more, e = r.batch(ctx, packs)
		if e != nil {
			return result, e
		}
		result.PacksMS = ms(start)
		result.Requests = append(result.Requests, more...)
	}
	result.TotalMS = ms(started)
	// Isolate a subsequent metadata response with receipts. This is NOT a
	// complete warm-browser timing, and it is not included in TotalMS.
	h := baseHeaders.Clone()
	h.Set("Definition-Known", strings.Join(defs, ","))
	h.Set("Event-Known", strings.Join(keys[:min(len(keys), 240)], ","))
	h.Set("Summary-Known", v.SummaryVersion)
	search.Headers = h
	search.Stage = "known-metadata"
	start := time.Now()
	raw, m, e = r.get(ctx, search)
	if e != nil {
		return result, e
	}
	warm, _, e := parse(raw)
	if e != nil {
		return result, e
	}
	if len(warm.Page) != len(v.Page) {
		return result, fmt.Errorf("page length changed")
	}
	for i := range warm.Page {
		if warm.Page[i].Key != v.Page[i].Key {
			return result, fmt.Errorf("page identity changed")
		}
	}
	result.KnownMetadataMS = ms(start)
	result.Requests = append(result.Requests, m)
	return result, nil
}
func main() {
	if e := mainErr(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func mainErr() error {
	origin := flag.String("origin", "http://127.0.0.1:3020", "API origin")
	protocol := flag.Int("protocol", 1, "required HTTP major version")
	n := flag.Int("trials", 10, "measured trials after one discarded setup trial")
	parallel := flag.Int("parallel", 4, "concurrent follow-up requests")
	overlap := flag.Bool("overlap", false, "experimental overlap of summary/seed-pack stages")
	only := flag.String("scenario", "", "run one named scenario")
	out := flag.String("out", "/tmp/trunk-api-benchmark.json", "results path")
	flag.Parse()
	if *parallel < 1 || *n < 1 {
		return fmt.Errorf("trials and parallel must be positive")
	}
	var transport http.RoundTripper
	if *protocol == 3 {
		t := &http3.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
		defer t.Close()
		transport = t
	} else {
		t := &http.Transport{ForceAttemptHTTP2: *protocol == 2, MaxIdleConnsPerHost: 64, MaxConnsPerHost: 64}
		defer t.CloseIdleConnections()
		transport = t
	}
	r := runner{*origin, &http.Client{Transport: transport, Timeout: 30 * time.Second}, *protocol, *parallel, *overlap}
	base := scenario{From: "2026-09-25T00:00:00Z", To: "2026-10-26T00:00:00Z", Include: "summary,page,statistics", Page: 100}
	scenarios := []scenario{}
	add := func(name, tap, include, query string, page int) {
		s := base
		s.Name = name
		s.Tap = tap
		if include != "" {
			s.Include = include
		}
		s.Query = query
		s.Page = page
		scenarios = append(scenarios, s)
	}
	numeric := `{"filters":[{"field":"duration","op":">","value":{"kind":"number","value":"600"}}],"order":"desc"}`
	text := `{"filters":[],"search":{"text":"authorized","mode":"terms"},"order":"desc"}`
	add("all-full", "", "", "", 100)
	add("all-page", "", "page", "", 100)
	add("all-summary", "", "summary", "", 100)
	add("branch-full", "demo/payment/transactions.v1/write", "", "", 100)
	add("all-numeric", "", "page", numeric, 100)
	add("branch-numeric", "demo/payment/transactions.v1/write", "page", numeric, 100)
	add("all-text", "", "page", text, 100)
	add("branch-text", "demo/payment/transactions.v1/write", "page", text, 100)
	add("all-1000", "", "", "", 1000)
	samples := []sample{}
	for _, s := range scenarios {
		if *only != "" && s.Name != *only {
			continue
		}
		identity := ""
		for trial := -1; trial < *n; trial++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			m, e := r.run(ctx, s, trial)
			cancel()
			if e != nil {
				return fmt.Errorf("%s trial %d: %w", s.Name, trial, e)
			}
			if identity != "" && identity != m.PageIdentity {
				return fmt.Errorf("snapshot changed between trials")
			}
			identity = m.PageIdentity
			if trial >= 0 {
				samples = append(samples, m)
			}
			fmt.Printf("%s %d/%d %.1f ms, %d summaries, %d seed packs\n", s.Name, trial+1, *n, m.TotalMS, m.SummaryPackets, m.SeedPacks)
		}
		data := map[string]any{"origin": *origin, "protocol": *protocol, "parallel": *parallel, "overlap": *overlap, "trials": *n, "recordedAt": time.Now().UTC(), "runtime": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "logicalCPUs": runtime.NumCPU(), "conditions": "Native Go HTTP API delivery only. Warm uncontrolled server/OS caches, one discarded setup trial, persistent transport per run. Fresh summary/seed-pack receipts each trial, no native byte-coverage hints between packs. Includes HTTP gzip decode, summary SHA256 validation, and routing metadata parse. Excludes seed-pack application decode, browser/SharedWorker/OPFS/render. KnownMetadataMS is a separate single request with receipts, not a warm full view.", "scenarios": scenarios, "samples": samples}
		raw, e := json.MarshalIndent(data, "", "  ")
		if e != nil {
			return e
		}
		if e = os.WriteFile(*out, append(raw, '\n'), 0644); e != nil {
			return e
		}
	}
	return nil
}

func decodeSummaryPacks(raw []byte, out *[][]byte) error {
	l, e := pil.Compile(pil.Definition{Fields: []pil.Field{{Name: "packs", Type: "ARRAY<BYTES>"}}})
	if e != nil {
		return e
	}
	var f pil.Frame
	if e = l.Scan(raw, &f); e != nil {
		return e
	}
	items, e := pil.FieldValue(l.Definition.Fields[0], f.Values[0])
	if e != nil {
		return e
	}
	*out = make([][]byte, len(items.([]any)))
	for i, v := range items.([]any) {
		(*out)[i] = v.([]byte)
	}
	return nil
}
