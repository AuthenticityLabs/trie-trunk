package cache

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// Retains the previous daily candidate algorithm as a benchmark-only baseline.
func dailyCandidatesBaseline(b *branchStore, from, to time.Time, width int, tree *timeTrie) []cursor {
	lo, hi := timeBound(from), timeBound(to)
	tail := uint64(len(b.rows) / width)
	var spans []Span
	for _, days := range tree.years {
		for _, p := range days {
			if p.To <= lo || p.From >= hi {
				continue
			}
			for _, r := range p.Ranges {
				spans = append(spans, Span{uint64(r.Start), r.end(tail)})
			}
		}
	}
	spans, _ = Normalize(spans)
	var out []cursor
	for _, span := range spans {
		first := sort.Search(len(b.runs), func(i int) bool { return b.runs[i].End > span.Start })
		for i := first; i < len(b.runs) && b.runs[i].Start < span.End; i++ {
			run := b.runs[i]
			if run.MaxMS < lo || run.MinMS >= hi {
				continue
			}
			start, end := run.Start, run.End
			if start < span.Start {
				start = span.Start
			}
			if end > span.End {
				end = span.End
			}
			left, right := b.lower(start, end, lo), b.lower(start, end, hi)
			if left < right {
				out = append(out, cursor{left, right})
			}
		}
	}
	return out
}

var packageCursorSink []cursor

func BenchmarkSemanticNavigation(b *testing.B) { benchmarkSemanticNavigation(b, 12) }
func BenchmarkHourlyNavigation(b *testing.B)   { benchmarkSemanticNavigation(b, 1) }
func benchmarkSemanticNavigation(b *testing.B, hours int) {
	rows, from, _ := ecRows(1000000, false)
	branch := branchStore{}
	tree := newTimeTrie(hours)
	for i := 0; i < 1000000; i++ {
		id := [16]byte(rows[i*26 : i*26+16])
		ms := eventTime(id[:])
		base := utcHour(ms)
		if len(branch.runs) == 0 || branch.runs[len(branch.runs)-1].PAC.Placement.From != base || branch.tail()-branch.runs[len(branch.runs)-1].Start >= 4096 {
			branch.rows = append(branch.rows, indexHeader(base)...)
			branch.runs = append(branch.runs, Run{Start: branch.tail(), MinMS: ms, PAC: &StoredPAC{Placement: PACPlacement{From: base, To: base + TimeHourMS}}})
		}
		n := branch.tail()
		tree.append(ms, n, time.UTC)
		row := make([]byte, IndexRowSize)
		if e := packIndexID(row, id, base); e != nil {
			b.Fatal(e)
		}
		branch.rows = append(branch.rows, row...)
		run := &branch.runs[len(branch.runs)-1]
		run.End = branch.tail()
		run.MaxMS = ms
	}
	var err error
	branch.time, err = buildTimeDirectory(branch.runs, branch.runs[0].PAC.Placement.From, branch.runs[len(branch.runs)-1].PAC.Placement.To, branch.tail(), branch.generation)
	if err != nil {
		b.Fatal(err)
	}
	for _, window := range []string{fmt.Sprintf("aligned%dh", hours), "partial1h"} {
		var requests [28][2]time.Time
		for i := range requests {
			day := from.AddDate(0, 0, i+1)
			a, z := semanticBounds(day, hours, from.Location())
			if window == "partial1h" {
				a = a.Add(3 * time.Hour)
				if hours == 1 {
					a = a.Add(15 * time.Minute)
				}
				z = a.Add(time.Hour)
			}
			requests[i] = [2]time.Time{a, z}
		}
		for _, mode := range []string{"daily_baseline", fmt.Sprintf("semantic%dh", hours)} {
			b.Run(window+"/"+mode, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					q := requests[i%len(requests)]
					if mode == "daily_baseline" {
						packageCursorSink = dailyCandidatesBaseline(&branch, q[0], q[1], IndexRowSize, &tree)
					} else {
						packageCursorSink = branch.candidates(q[0], q[1], IndexRowSize)
					}
				}
			})
		}
	}
}
