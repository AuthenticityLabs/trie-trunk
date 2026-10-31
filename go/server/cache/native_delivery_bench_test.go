package cache

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type countedResponse struct {
	header http.Header
	bytes  int
}

func (w *countedResponse) Header() http.Header { return w.header }
func (w *countedResponse) WriteHeader(status int) {
	if status != 200 {
		panic(fmt.Sprintf("benchmark HTTP status %d", status))
	}
}
func (w *countedResponse) Write(b []byte) (int, error) { w.bytes += len(b); return len(b), nil }

// Same cached definition/store, request window, HTTP handler and sink. This
// measures encoding/framing/range reads (including checksums), not network time.
func BenchmarkNativeDelivery(b *testing.B) {
	for _, count := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			l := testLayout(b, false)
			at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			s, e := Open(Config{Root: b.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(2 * time.Hour) }})
			if e != nil {
				b.Fatal(e)
			}
			defer s.Close()
			frames := make([][]byte, count)
			for i := range frames {
				frames[i] = testFrame(b, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i+1), i%3 != 0)
			}
			if n, e := s.AppendBatch(Write, frames); e != nil || n != count {
				b.Fatal(n, e)
			}
			if e = s.Flush(); e != nil {
				b.Fatal(e)
			}
			frames = nil
			h := &HTTP{Store: s}
			q := url.Values{"tap": {h.tap(Write)}, "from": {at.Format(time.RFC3339Nano)}, "to": {at.Add(time.Hour).Format(time.RFC3339Nano)}}
			var snapshot Manifest
			e = s.Collect(context.Background(), CollectionRequest{Branch: Write, At: at, DataFrom: at, DataTo: at.Add(time.Hour)}, func(m Manifest) error { snapshot = m; return nil }, func(NativeChunk) error { return nil })
			if e != nil {
				b.Fatal(e)
			}
			coverage, _ := json.Marshal([]Coverage{{Definition: l.Digest, Generation: snapshot.Generation, Index: snapshot.Index, MemberRanges: snapshot.MemberRanges, TrieNodes: snapshot.TrieNodes}})
			for _, mode := range []string{"standalone", "native-cold", "native-index-only", "native-warm"} {
				b.Run(mode, func(b *testing.B) {
					params := q.Encode()
					if mode != "standalone" {
						params += "&format=native"
					}
					if mode == "native-index-only" {
						params += "&data=none"
					}
					r := httptest.NewRequest("GET", "/collect?"+params, nil)
					r.Header.Set("Accept", "application/pil")
					r.Header.Set("PIL-Version", "2")
					r.Header.Set("Accept-Encoding", "gzip")
					r.Header.Set("PIL-Features", "native-ranges,adaptive-integers")
					r.Header.Set("Definition-Known", fmtDigestNative(l.Digest))
					if mode == "native-warm" {
						r.Header.Set("Trunk-Coverage", base64.RawURLEncoding.EncodeToString(coverage))
					}
					b.ReportAllocs()
					b.ResetTimer()
					size := 0
					for i := 0; i < b.N; i++ {
						w := &countedResponse{header: make(http.Header)}
						if mode == "standalone" {
							benchmarkStandaloneCollect(w, r, h)
						} else {
							h.serveHTTP(w, r)
						}
						size = w.bytes
					}
					b.ReportMetric(float64(size), "wire-B/op")
					b.ReportMetric(float64(count), "events/op")
				})
			}
		})
	}
}

// Benchmark-only baseline. Production /collect never expands stored PACs.
func benchmarkStandaloneCollect(w http.ResponseWriter, r *http.Request, h *HTTP) {
	q := r.URL.Query()
	from, e := time.Parse(time.RFC3339Nano, q.Get("from"))
	if e != nil {
		panic(e)
	}
	to, e := time.Parse(time.RFC3339Nano, q.Get("to"))
	if e != nil {
		panic(e)
	}
	branches, e := h.selectedBranches(q.Get("tap"))
	if e != nil {
		panic(e)
	}
	out := bufio.NewWriterSize(w, 128<<10)
	out.Write([]byte{'T', 'P', 'I', 'L', 2})
	l := h.Store.layout
	if !strings.Contains(r.Header.Get("Definition-Known"), fmtDigest(l)) {
		if e = writeDefine(out, l); e != nil {
			panic(e)
		}
	}
	for i, b := range branches {
		if e = writeBind(out, uint32(i), l.Digest, h.tap(b)); e != nil {
			panic(e)
		}
		e = h.Store.SearchLive(r.Context(), Query{Branch: b, From: from, To: to}, func(raw []byte) error { return writeEvent(out, uint32(i), raw) })
		if e != nil {
			panic(e)
		}
	}
	out.Write([]byte{255, 254})
	if e = out.Flush(); e != nil {
		panic(e)
	}
	w.Header().Set("Trunk-Status", "ok")
}
