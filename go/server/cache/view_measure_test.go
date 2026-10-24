package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"testing"
	"time"
)

// An opt-in workload comparison, not a claim that PAC makes the PIL codec faster.
func TestMeasurePagedView(t *testing.T) {
	n := measurementSize(t)
	l, frames, from, to, _ := measurementFixtureProfile(t, n, false, "range-sized")
	d := l.Definition
	d.Resource.Aggregations = []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}
	d.Resource.Roles.Duration = "duration"
	d.Fields[3].Type = "U64"
	source := l
	l, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	for i, raw := range frames {
		var f Frame
		if e = source.Scan(raw, &f); e != nil {
			t.Fatal(e)
		}
		f.Values[3] = Uint(unsigned(f.Values[3].Bytes), 8)
		frames[i], e = l.Encode(nil, f.Values)
		if e != nil {
			t.Fatal(e)
		}
	}
	cfg := Config{Root: t.TempDir(), Cache: "bench", Tenant: "measure", Layout: l, Now: func() time.Time { return to.Add(time.Hour) }}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for i := 0; i < len(frames); i += 1024 {
		if _, e = s.AppendBatch(Write, frames[i:min(i+1024, len(frames))]); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	frames = nil
	h := &HTTP{Store: s}
	params := url.Values{"tap": {h.tap(Write)}, "from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
	results := map[string]any{}
	for _, mode := range []string{"full-pil", "page-100-and-summary", "summary-only"} {
		path := "/events?" + params.Encode() + "&page=100"
		if mode == "full-pil" {
			path = "/collect?" + params.Encode()
		} else if mode == "summary-only" {
			path += "&include=summary"
		}
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept", "application/pil")
		req.Header.Set("PIL-Version", "2")
		req.Header.Set("PIL-Features", "pac-arrays,adaptive-integers")
		req.Header.Set("Definition-Known", fmtDigest(l))
		check := httptest.NewRecorder()
		if mode == "full-pil" {
			benchmarkStandaloneCollect(check, req, h)
		} else {
			h.ServeHTTP(check, req)
		}
		if check.Code != 200 {
			t.Fatal(check.Code, check.Body.String())
		}
		size := check.Body.Len()
		if mode == "full-pil" {
			if verifyMeasuredPIL(t, l, check.Body.Bytes()) != n {
				t.Fatal("full count")
			}
		} else {
			v := collectView(t, l, h, check.Body.Bytes())
			if v.result.Summary.Count != uint64(n) {
				t.Fatal("summary count")
			}
			want := min(100, n)
			if mode == "summary-only" {
				want = 0
			}
			if len(v.frames) != want {
				t.Fatal("page count")
			}
		}
		run := func() error {
			sink := &countedResponse{header: make(http.Header)}
			if mode == "full-pil" {
				benchmarkStandaloneCollect(sink, req, h)
			} else {
				h.ServeHTTP(sink, req)
			}
			if sink.bytes != size {
				return fmt.Errorf("delivery changed")
			}
			return nil
		}
		times := timed(t, 9, run)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if e = run(); e != nil {
			t.Fatal(e)
		}
		runtime.ReadMemStats(&after)
		results[mode] = map[string]any{"latency": times, "response_bytes": size, "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "allocations": after.Mallocs - before.Mallocs}
	}
	saveMeasurement(t, map[string]any{"events": n, "results": results, "scope": "same Go handler, temporary store, warm OS cache, cached definitions, range-sized integers except U64 duration required by aggregate role; standard aggregates enabled, collection.idx disabled; no network, compression, rendering or ingestion timing"})
}
