package cache

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type Aggregate struct{ Count, DurationCount, Sum, Min, Max uint64 }

func (a *Aggregate) add(duration *uint64) error {
	if a.Count == math.MaxUint64 {
		return fmt.Errorf("aggregate overflow")
	}
	a.Count++
	if duration == nil {
		return nil
	}
	n := *duration
	if math.MaxUint64-a.Sum < n || a.DurationCount == math.MaxUint64 {
		return fmt.Errorf("aggregate overflow")
	}
	if a.DurationCount == 0 || n < a.Min {
		a.Min = n
	}
	if a.DurationCount == 0 || n > a.Max {
		a.Max = n
	}
	a.Sum += n
	a.DurationCount++
	return nil
}
func (a *Aggregate) merge(b Aggregate) error {
	if math.MaxUint64-a.Count < b.Count || math.MaxUint64-a.DurationCount < b.DurationCount || math.MaxUint64-a.Sum < b.Sum {
		return fmt.Errorf("aggregate overflow")
	}
	if b.DurationCount > 0 {
		if a.DurationCount == 0 || b.Min < a.Min {
			a.Min = b.Min
		}
		if a.DurationCount == 0 || b.Max > a.Max {
			a.Max = b.Max
		}
	}
	a.Count += b.Count
	a.DurationCount += b.DurationCount
	a.Sum += b.Sum
	return nil
}

type aggregateKey struct {
	Period    int64
	Reference uint16
}
type Group struct {
	Period    int64     `cbor:"0,keyasint"`
	Reference uint16    `cbor:"1,keyasint"`
	Aggregate Aggregate `cbor:"2,keyasint"`
}
type Summary struct {
	DurationWidth byte // Definition storage width; zero when no duration role is bound.
	From, Through uint64
	Columns       AggregateColumns
	Statistics    []statisticsGroup
}

type AggregateColumns struct {
	Period                              []int64
	Reference                           []uint16
	Count, DurationCount, Sum, Min, Max []uint64
}

func (c AggregateColumns) Len() int { return len(c.Count) }
func (c AggregateColumns) At(i int) Group {
	return Group{c.Period[i], c.Reference[i], Aggregate{c.Count[i], c.DurationCount[i], c.Sum[i], c.Min[i], c.Max[i]}}
}
func (c *AggregateColumns) Append(g Group) {
	c.Period = append(c.Period, g.Period)
	c.Reference = append(c.Reference, g.Reference)
	c.Count = append(c.Count, g.Aggregate.Count)
	c.DurationCount = append(c.DurationCount, g.Aggregate.DurationCount)
	c.Sum = append(c.Sum, g.Aggregate.Sum)
	c.Min = append(c.Min, g.Aggregate.Min)
	c.Max = append(c.Max, g.Aggregate.Max)
}
func summaryFromGroups(from, through uint64, groups []Group) Summary {
	s := Summary{From: from, Through: through}
	for _, g := range groups {
		s.Columns.Append(g)
	}
	return s
}
func makeSummary(run Run, groups map[aggregateKey]Aggregate) Summary {
	keys := make([]aggregateKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Period != keys[j].Period {
			return keys[i].Period < keys[j].Period
		}
		return keys[i].Reference < keys[j].Reference
	})
	s := Summary{From: run.Start, Through: run.End}
	for _, k := range keys {
		s.Columns.Append(Group{k.Period, k.Reference, groups[k]})
	}
	return s
}

// Hourly sealing publishes five-minute accumulator columns; complete UTC days
// are subsequently packed. Source-row watermarks prevent replaying a committed
// hour twice. This API returns only sealed aggregates, unlike live view snapshots.
func (s *Store) Summaries(branch Branch, from, to time.Time) (map[int64]map[uint16]Aggregate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summaries(branch, from, to)
}
func (s *Store) summaries(branch Branch, from, to time.Time) (map[int64]map[uint16]Aggregate, error) {
	s.waitSealing()
	if e := s.check(); e != nil {
		return nil, e
	}
	if !branch.Valid() || !branch.Aggregates() {
		return nil, fmt.Errorf("branch does not aggregate")
	}
	out := map[int64]map[uint16]Aggregate{}
	b := s.branches[branch]
	if b == nil {
		return out, nil
	}
	if e := s.flush(b); e != nil {
		return nil, s.fail(e)
	}
	cutoff := s.config.Now().Add(-15 * time.Minute).UnixMilli()
	through := b.baseRows
	for _, summary := range b.summaries {
		if summary.From != through+IndexHeaderSlots || summary.Through <= through {
			return nil, fmt.Errorf("summary watermark mismatch")
		}
		through = summary.Through
		for i := 0; i < summary.Columns.Len(); i++ {
			g := summary.Columns.At(i)
			if g.Period < from.UnixMilli() || g.Period >= to.UnixMilli() || g.Period+300000 > cutoff {
				continue
			}
			groups := out[g.Period]
			if groups == nil {
				groups = map[uint16]Aggregate{}
				out[g.Period] = groups
			}
			a := groups[g.Reference]
			if e := a.merge(g.Aggregate); e != nil {
				return nil, e
			}
			groups[g.Reference] = a
		}
	}
	return out, nil
}
