package cache

import (
	"context"
	"fmt"
	"math"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestViewStatisticsFullRangeSparseAndContinuation(t *testing.T) {
	d := testLayout(t, false).Definition
	d.Fields = append(d.Fields, Field{Name: "operation", Type: "STRING", Values: []string{"authorize", "cancel"}}, Field{Name: "level", Type: "STRING", Values: []string{"INFO", "ERROR"}})
	d.Resource.Path = []string{"state", "operation", "level"}
	l, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 40; i++ {
		branch, level := Write, uint64(0)
		if i%4 == 0 {
			branch = Error
			level = 1
		}
		duration := Uint(uint64(i%20+1+100*(i/20)), 8)
		if i == 20 {
			duration = Value{}
		}
		if i == 21 {
			duration = Value{State: Null}
		}
		v := []Value{uuid(at.Add(time.Duration(i)*time.Minute).UnixMilli(), uint32(i+1)), testObject(42), Uint(uint64(i), 8), duration, Uint(0, 1), Text("completed"), Uint(999, 8), Uint(uint64(i/20), 1), Uint(level, 1)}
		if i == 22 {
			v[4] = Value{}
		} // no shared prefix: classification must come from PIL
		raw, e := l.Encode(nil, v)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(branch, raw); e != nil {
			t.Fatal(e)
		}
	}
	// Debug durations must not contaminate operational duration aggregates.
	v := []Value{uuid(at.Add(41*time.Minute).UnixMilli(), 99), testObject(42), {}, Uint(999999, 8), Uint(0, 1), Text("debug"), {}, Uint(0, 1), Uint(0, 1)}
	raw, _ := l.Encode(nil, v)
	if _, err = s.Append(Debug, raw); err != nil {
		t.Fatal(err)
	}
	h := &HTTP{Store: s}
	path := "/events?from=2026-09-01T12:00:00Z&to=2026-09-01T13:00:00Z&page=2&include=summary,page,statistics"
	get := func(path, known string) viewDecoded {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept", "application/pil")
		req.Header.Set("PIL-Version", "2")
		req.Header.Set("PIL-Features", "pac-arrays")
		req.Header.Set("Summary-Known", known)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return collectView(t, l, h, w.Body.Bytes())
	}
	first := get(path, "")
	summary := first.result.Summary
	stats := summary.Statistics
	if summary.Count != 41 || len(first.frames) != 2 || stats == nil {
		t.Fatal(summary)
	}
	if stats.Duration.Count != 38 || math.Abs(stats.Duration.Average-2217.0/38) > 1e-9 || stats.Duration.Min != 1 || stats.Duration.Max != 120 {
		t.Fatal(stats.Duration)
	}
	if len(stats.Services) != 1 || stats.Services[0].Name != "payment" || stats.Services[0].Count != 41 {
		t.Fatal(stats.Services)
	}
	if len(stats.Operations.Errors) != 2 || stats.Operations.Errors[0].Value != 5 || stats.Operations.Errors[1].Value != 5 {
		t.Fatal(stats.Operations.Errors)
	}
	ranks := stats.Operations.Latency
	if len(ranks) != 2 || ranks[0].Operation != "cancel" || ranks[0].Value != 120 || ranks[0].Samples != 18 || ranks[1].Value != 19 || ranks[1].Samples != 20 {
		t.Fatal(ranks)
	}
	// Known summary prevents any repeated duration scan on subsequent pages.
	next := get(path+"&after="+url.QueryEscape(first.result.Next), first.result.Snapshot)
	if next.result.Summary != nil || next.result.Snapshot != first.result.Snapshot || len(next.frames) != 2 {
		t.Fatal(next.result)
	}
	// Exact partial bounds (no rounding out to whole aggregate buckets).
	narrow := get("/events?from=2026-09-01T12:00:00.001Z&to=2026-09-01T12:02:00Z&page=1&include=statistics", "")
	if narrow.result.Summary.Count != 1 || narrow.result.Summary.Statistics.Duration.Average != 2 || len(narrow.frames) != 0 {
		t.Fatal(narrow.result)
	}
}

func BenchmarkViewStatistics(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			l := testLayout(b, false)
			at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			end := at.Add(time.Hour)
			s, e := Open(Config{Root: b.TempDir(), Cache: "bench", Tenant: "test", Layout: l, Now: func() time.Time { return end.Add(time.Hour) }})
			if e != nil {
				b.Fatal(e)
			}
			defer s.Close()
			for start := 0; start < n; start += 1000 {
				var frames [][]byte
				for i := start; i < min(start+1000, n); i++ {
					frames = append(frames, testFrame(b, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i+1), i%5 == 0))
				}
				if _, e = s.AppendBatch(Write, frames); e != nil {
					b.Fatal(e)
				}
			}
			source, e := s.viewSnapshot(Write, at, end, nil, nil, true)
			if e != nil {
				b.Fatal(e)
			}
			defer source.close()
			source.api = &HTTP{Store: s}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				stats, e := viewStatistics(context.Background(), []*viewSource{source})
				if e != nil {
					b.Fatal(e)
				}
				if stats.Duration.Count != uint64(n-n/5) {
					b.Fatal(stats.Duration)
				}
			}
		})
	}
}
