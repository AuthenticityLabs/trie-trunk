package cache

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

// Same canonical PIL frames. The scan is a benchmark-only control: production
// does not fall back to decoding sealed PACs when full-text indexing is disabled.
// The selective token makes contains and terms equivalent in this fixture.
func BenchmarkTantivyCandidates(b *testing.B) {
	requireSearch(b)
	for _, count := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprintf("%d", count), func(b *testing.B) {
			l := testLayout(b, false)
			at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
			now := at.Add(2 * time.Hour)
			s, e := Open(Config{Root: b.TempDir(), Cache: "demo", Tenant: "bench", Layout: l, Now: func() time.Time { return now }})
			if e != nil {
				b.Fatal(e)
			}
			defer s.Close()
			off, on := false, true
			s.policy.SearchIndex = &off
			frames := make([][]byte, count)
			var f Frame
			for i := range frames {
				raw := testFrame(b, l, at.Add(time.Duration(i)*time.Millisecond), uint32(i+1), false)
				if e = l.Scan(raw, &f); e != nil {
					b.Fatal(e)
				}
				word := "completed"
				if i%100 == 0 {
					word = "timeout"
				}
				f.Values[5] = Text(fmt.Sprintf("payment request %d %s at upstream gateway for transaction %d", i, word, i*41))
				frames[i], e = l.Encode(nil, f.Values)
				if e != nil {
					b.Fatal(e)
				}
			}
			begin := time.Now()
			if _, e = s.AppendBatch(Write, frames); e != nil {
				b.Fatal(e)
			}
			if e = s.Flush(); e != nil {
				b.Fatal(e)
			}
			ingest := time.Since(begin)
			v, e := s.viewSnapshot(Write, at, now, nil, nil, true)
			if e != nil {
				b.Fatal(e)
			}
			begin = time.Now()
			s.searchGate(Write).Lock()
			_, e = v.catchupSearch(context.Background(), v.searchRequest())
			s.searchGate(Write).Unlock()
			indexing := time.Since(begin)
			v.close()
			if e != nil {
				b.Fatal(e)
			}
			var canonical, indexBytes int64
			filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, e error) error {
				if e != nil || d.IsDir() {
					return e
				}
				info, e := d.Info()
				if e != nil {
					return e
				}
				if filepath.Ext(filepath.Dir(path)) == ".search" {
					indexBytes += info.Size()
				} else {
					canonical += info.Size()
				}
				return nil
			})
			b.Logf("rows=%d canonical_bytes=%d index_bytes=%d ingest=%s index_build=%s", count, canonical, indexBytes, ingest, indexing)
			cases := []struct {
				name string
				q    QueryPlan
			}{{"text", QueryPlan{Search: &QueryText{"timeout", "contains"}, Order: "asc"}}, {"numeric", QueryPlan{Filters: []QueryFilter{{"duration", ">=", QueryLiteral{"number", fmt.Sprint(count - 10)}}}, Order: "asc"}}, {"compound", QueryPlan{Filters: []QueryFilter{{"state", "==", QueryLiteral{"string", "authorized"}}, {"duration", ">", QueryLiteral{"number", fmt.Sprint(count / 2)}}}, Search: &QueryText{"timeout", "contains"}, Order: "asc"}}}
			for _, c := range cases {
				b.Run(c.name, func(b *testing.B) {
					s.policy.SearchIndex = &off
					want := benchmarkScanRows(b, s, at, now, c.q)
					for _, indexed := range []bool{false, true} {
						name := "scan"
						lookup := benchmarkScanRows
						q := c.q
						s.policy.SearchIndex = &off
						if indexed {
							name = "index"
							lookup = searchRows
							s.policy.SearchIndex = &on
							if q.Search != nil {
								q.Search = &QueryText{q.Search.Text, "terms"}
							}
						}
						got := lookup(b, s, at, now, q)
						if len(got) != len(want) {
							b.Fatalf("different matches %d/%d", len(got), len(want))
						}
						for i := range got {
							if got[i] != want[i] {
								b.Fatal("different references")
							}
						}
						b.Run(name, func(b *testing.B) {
							b.ReportAllocs()
							b.ResetTimer()
							for i := 0; i < b.N; i++ {
								rows := lookup(b, s, at, now, q)
								if len(rows) != len(want) {
									b.Fatal("unstable matches")
								}
							}
						})
					}
				})
			}
		})
	}
}

func benchmarkScanRows(t testing.TB, s *Store, from, to time.Time, q QueryPlan) []uint64 {
	t.Helper()
	v, err := s.viewSnapshot(Write, from, to, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer v.close()
	bound := bindQuery(s.layout, &q)
	var frame Frame
	var rows []uint64
	for _, span := range v.ranges {
		for row := span.at; row < span.end; row++ {
			raw, err := v.fullFrame(row)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.layout.Scan(raw, &frame); err != nil {
				t.Fatal(err)
			}
			if matchBoundQuery(s.layout, &frame, &q, bound) {
				rows = append(rows, row)
			}
		}
	}
	return rows
}
