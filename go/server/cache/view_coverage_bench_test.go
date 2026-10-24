package cache

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func BenchmarkViewBucketPage(b *testing.B) {
	for _, length := range []int{17, 1024} {
		b.Run(fmt.Sprintf("text%d", length), func(b *testing.B) {
			l := testLayout(b, false)
			at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
			s, err := Open(Config{Root: b.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(3 * time.Hour) }})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			for i := 0; i < 1000; i++ {
				values := []Value{uuid(at.Add(time.Duration(i)*time.Second).UnixMilli(), uint32(i+1)), testObject(42), Uint(uint64(i), 8), Uint(uint64(i+10), 8), Uint(uint64(i%3), 1), Text(strings.Repeat("x", length)), Uint(999, 8)}
				frame, err := l.Encode(nil, values)
				if err != nil {
					b.Fatal(err)
				}
				if _, err = s.Append(Write, frame); err != nil {
					b.Fatal(err)
				}
			}
			h := &HTTP{Store: s}
			v, err := s.viewSnapshot(Write, at, at.Add(time.Hour), nil, nil, true)
			if err != nil {
				b.Fatal(err)
			}
			v.api = h
			bucket, err := v.bucket(0, 0)
			v.close()
			if err != nil {
				b.Fatal(err)
			}
			for _, mode := range []string{"baseline", "cold-buckets", "warm-bucket"} {
				b.Run(mode, func(b *testing.B) {
					path := "/events?tap=" + url.QueryEscape(h.tap(Write)) + "&from=2026-09-01T12:00:00Z&to=2026-09-01T13:00:00Z&page=100&include=page"
					req := httptest.NewRequest("GET", path, nil)
					req.Header.Set("Accept", "application/pil")
					req.Header.Set("PIL-Version", "2")
					req.Header.Set("PIL-Features", "pac-arrays,adaptive-integers")
					req.Header.Set("Definition-Known", fmtDigest(l))
					if mode != "baseline" {
						req.Header.Set("PIL-Features", "pac-arrays,adaptive-integers,view-buckets")
					}
					if mode == "warm-bucket" {
						req.Header.Set("Bucket-Known", bucket.Key)
					}
					b.ReportAllocs()
					b.ResetTimer()
					size := 0
					for i := 0; i < b.N; i++ {
						w := httptest.NewRecorder()
						h.ServeHTTP(w, req)
						if w.Code != 200 {
							b.Fatal(w.Body.String())
						}
						size = w.Body.Len()
					}
					b.ReportMetric(float64(size), "response-B")
				})
			}
		})
	}
}
