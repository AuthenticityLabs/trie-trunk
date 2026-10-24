package cache

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

// Prior two-pass sealing work retained only as a benchmark control.
func sealedStatistics(l *Layout, branch Branch, frames []pending) ([]statisticsGroup, error) {
	type key struct {
		Period int64
		Labels [4]string
	}
	groups := map[key]*statisticsGroup{}
	hist := map[key]map[uint64]uint64{}
	var f Frame
	for _, p := range frames {
		if e := l.Scan(p.frame, &f); e != nil {
			return nil, e
		}
		labels := statisticsLabels(l, branch, &f)
		k := key{eventTime(p.id[:]) / sumBucketMS * sumBucketMS, labels}
		g := groups[k]
		if g == nil {
			g = &statisticsGroup{Period: k.Period, Labels: labels}
			groups[k] = g
			hist[k] = map[uint64]uint64{}
		}
		g.Count++
		if branch == Error {
			g.Errors++
		}
		if branch.Aggregates() && l.Duration >= 0 && f.Values[l.Duration].State == Direct {
			hist[k][unsigned(f.Values[l.Duration].Bytes)]++
		}
	}
	out := make([]statisticsGroup, 0, len(groups))
	for k, g := range groups {
		for value, count := range hist[k] {
			g.Durations = append(g.Durations, [2]uint64{value, count})
		}
		sort.Slice(g.Durations, func(i, j int) bool { return g.Durations[i][0] < g.Durations[j][0] })
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Period != out[j].Period {
			return out[i].Period < out[j].Period
		}
		for k := range out[i].Labels {
			if out[i].Labels[k] != out[j].Labels[k] {
				return out[i].Labels[k] < out[j].Labels[k]
			}
		}
		return false
	})
	return out, nil
}

func summaryBenchFixture(b *testing.B, n int) (*Store, *branchStore, []pending, []Frame, time.Time) {
	b.Helper()
	l := testLayout(b, false)
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	s := &Store{layout: l, trie: newTrie()}
	branch := &branchStore{branch: Write, sum: &os.File{}}
	frames := make([]pending, n)
	decoded := make([]Frame, n)
	for i := range frames {
		raw := testFrame(b, l, at.Add(time.Duration(i)*time.Hour/time.Duration(n)), uint32(i+1), i%3 == 0)
		if e := l.Scan(raw, &decoded[i]); e != nil {
			b.Fatal(e)
		}
		copy(frames[i].id[:], decoded[i].Values[l.Event].Bytes)
		frames[i].frame = raw
		s.trie.intern(l, decoded[i].Values)
	}
	return s, branch, frames, decoded, at
}
func BenchmarkSummaryAccumulator(b *testing.B) {
	s, branch, frames, decoded, _ := summaryBenchFixture(b, 10000)
	// Identity validation/dedup and PIL scan happen before this incremental cost.
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := i % len(frames)
		if e := s.accumulate(branch, frames[j].id, decoded[j].Values); e != nil {
			b.Fatal(e)
		}
	}
}
func BenchmarkSummaryHourBuild(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		for _, mode := range []string{"prior-two-pass-seal", "ingress-plus-seal", "seal-preaccumulated"} {
			b.Run(fmt.Sprintf("%s/%d", mode, n), func(b *testing.B) {
				s, branch, frames, decoded, _ := summaryBenchFixture(b, n)
				run := Run{Start: 1, End: 1 + uint64(n)}
				for i, p := range frames {
					if e := s.accumulate(branch, p.id, decoded[i].Values); e != nil {
						b.Fatal(e)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for j := 0; j < b.N; j++ {
					var sum Summary
					var e error
					if mode == "prior-two-pass-seal" {
						groups := map[aggregateKey]Aggregate{}
						var f Frame
						for _, p := range frames {
							if e = s.layout.Scan(p.frame, &f); e != nil {
								b.Fatal(e)
							}
							ref := s.trie.intern(s.layout, f.Values)
							key := aggregateKey{eventTime(p.id[:]) / sumBucketMS * sumBucketMS, ref}
							g := groups[key]
							var d *uint64
							if f.Values[s.layout.Duration].State == Direct {
								v := unsigned(f.Values[s.layout.Duration].Bytes)
								d = &v
							}
							if e = g.add(d); e != nil {
								b.Fatal(e)
							}
							groups[key] = g
						}
						sum = makeSummary(run, groups)
						sum.Statistics, e = sealedStatistics(s.layout, Write, frames)
					} else {
						if mode == "ingress-plus-seal" {
							branch.accumulators = nil
							for i, p := range frames {
								if e = s.accumulate(branch, p.id, decoded[i].Values); e != nil {
									b.Fatal(e)
								}
							}
						}
						for _, a := range branch.accumulators {
							sum, e = a.snapshot(run, s.layout, &s.trie)
						}
					}
					if e != nil {
						b.Fatal(e)
					}
					if _, e = encodeSummary(sum); e != nil {
						b.Fatal(e)
					}
				}
			})
		}
	}
}
func summaryBenchDay(b *testing.B) (time.Time, []Summary) {
	s, branch, frames, decoded, at := summaryBenchFixture(b, 10000)
	for i, p := range frames {
		if e := s.accumulate(branch, p.id, decoded[i].Values); e != nil {
			b.Fatal(e)
		}
	}
	var source Summary
	for _, a := range branch.accumulators {
		var e error
		source, e = a.snapshot(Run{Start: 1, End: 10001}, s.layout, &s.trie)
		if e != nil {
			b.Fatal(e)
		}
	}
	sums := make([]Summary, 24)
	for h := range sums {
		sum := source
		sum.Columns.Period = append([]int64(nil), source.Columns.Period...)
		sum.Statistics = append([]statisticsGroup(nil), source.Statistics...)
		for i := range sum.Columns.Period {
			sum.Columns.Period[i] += int64(h) * TimeHourMS
		}
		// Vary duration values between hours: do not benchmark 24 identical
		// distributions as though they were independent observations.
		delta := uint64(h * 10000)
		sum.Columns.Sum = append([]uint64(nil), source.Columns.Sum...)
		sum.Columns.Min = append([]uint64(nil), source.Columns.Min...)
		sum.Columns.Max = append([]uint64(nil), source.Columns.Max...)
		for i, n := range sum.Columns.DurationCount {
			if n > 0 {
				sum.Columns.Sum[i] += n * delta
				sum.Columns.Min[i] += delta
				sum.Columns.Max[i] += delta
			}
		}
		for i := range sum.Statistics {
			sum.Statistics[i].Period += int64(h) * TimeHourMS
			sum.Statistics[i].Durations = append([][2]uint64(nil), source.Statistics[i].Durations...)
			for j := range sum.Statistics[i].Durations {
				sum.Statistics[i].Durations[j][0] += delta
			}
		}
		sums[h] = sum
	}
	return at, sums
}
func BenchmarkSummaryDayCodec(b *testing.B) {
	at, sums := summaryBenchDay(b)
	for _, level := range []int{0, 6, 9} {
		compress := level != 0
		codecLevel := max(level, 1)
		raw, e := encodeSummaryDayLevel(at.UnixMilli(), at.UnixMilli(), at.Add(24*time.Hour).UnixMilli(), sums, compress, codecLevel)
		if e != nil {
			b.Fatal(e)
		}
		if !compress && os.Getenv("TRUNK_SUMMARY_DENSE") != "" {
			if e := os.WriteFile(os.Getenv("TRUNK_SUMMARY_DENSE"), raw[32:], 0600); e != nil {
				b.Fatal(e)
			}
		}
		b.Run(fmt.Sprintf("encode/gzip%d", level), func(b *testing.B) {
			b.ReportMetric(float64(len(raw)), "bytes/day")
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, e := encodeSummaryDayLevel(at.UnixMilli(), at.UnixMilli(), at.Add(24*time.Hour).UnixMilli(), sums, compress, codecLevel); e != nil {
					b.Fatal(e)
				}
			}
		})
		b.Run(fmt.Sprintf("decode/gzip%d", level), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, _, _, e := decodeSummaryDay(raw); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
