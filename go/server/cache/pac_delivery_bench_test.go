package cache

import (
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type gzipBenchmarkResponse struct {
	*countedResponse
	gzip *gzip.Writer
}

func (w *gzipBenchmarkResponse) Write(p []byte) (int, error) { return w.gzip.Write(p) }

// Both paths use the same sealed store, definition and warm file cache.
// The baseline expands members and applies HTTP gzip; production copies stored
// compressed PACs with native index/trie companions. Their wire sizes differ.
func BenchmarkSealedPACDelivery(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			l := testLayout(b, false)
			at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			s, e := Open(Config{Root: b.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }})
			if e != nil {
				b.Fatal(e)
			}
			defer s.Close()
			off := false
			s.policy.SearchIndex = &off
			frames := make([][]byte, n)
			for i := range frames {
				frames[i] = testFrame(b, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i+1), i%3 != 0)
			}
			if _, e = s.AppendBatch(Error, frames); e != nil {
				b.Fatal(e)
			}
			if e = s.Flush(); e != nil {
				b.Fatal(e)
			}
			h := &HTTP{Store: s}
			r := httptest.NewRequest("GET", "/collect?tap="+h.tap(Error)+"&from="+at.Format(time.RFC3339)+"&to="+at.Add(time.Hour).Format(time.RFC3339), nil)
			r.Header.Set("Accept", "application/pil")
			r.Header.Set("PIL-Version", "2")
			r.Header.Set("PIL-Features", "native-ranges")
			r.Header.Set("Accept-Encoding", "gzip")
			r.Header.Set("Definition-Known", fmtDigest(l))
			for _, mode := range []string{"expand_then_gzip", "whole_pac"} {
				b.Run(mode, func(b *testing.B) {
					b.ReportAllocs()
					var size int
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						sink := &countedResponse{header: make(http.Header)}
						if mode == "whole_pac" {
							h.ServeHTTP(sink, r)
						} else {
							z, _ := gzip.NewWriterLevel(sink, gzip.BestSpeed)
							w := &gzipBenchmarkResponse{sink, z}
							benchmarkStandaloneCollect(w, r, h)
							if e := z.Close(); e != nil {
								b.Fatal(e)
							}
						}
						size = sink.bytes
					}
					b.ReportMetric(float64(size), "wire-B/op")
					b.ReportMetric(float64(n), "records/op")
				})
			}
		})
	}
}
