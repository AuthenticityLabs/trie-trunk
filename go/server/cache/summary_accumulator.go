package cache

import (
	"fmt"
	"sort"
)

// Only aggregate cells live on the Go heap, never copies of event payloads.
// An hour becomes immutable when the sealer installs its ingress boundary.
type accumulatorKey struct {
	period int64
	path   string
	labels [4]string
}
type summaryAccumulator struct {
	keys      []accumulatorKey
	index     map[accumulatorKey]int
	columns   AggregateColumns
	errors    []uint64
	durations []map[uint64]uint64
}

func (s *Store) accumulate(b *branchStore, id [16]byte, values []Value) error {
	if b.sum == nil {
		return nil
	}
	hour := utcHour(eventTime(id[:]))
	if b.accumulators == nil {
		b.accumulators = map[int64]*summaryAccumulator{}
	}
	a := b.accumulators[hour]
	if a == nil {
		a = &summaryAccumulator{index: map[accumulatorKey]int{}}
		b.accumulators[hour] = a
	}
	var pathBuffer [256]byte
	path := pathBuffer[:0]
	for _, i := range s.layout.Path {
		v := values[i]
		if v.State != Direct {
			break
		}
		at := len(path)
		path = append(path, make([]byte, 4)...)
		order.PutUint32(path[at:], uint32(len(v.Bytes)))
		path = append(path, v.Bytes...)
	}
	period := eventTime(id[:]) / sumBucketMS * sumBucketMS
	labels := statisticsLabels(s.layout, b.branch, &Frame{Values: values})
	// The lookup conversion does not escape. Own the signature only when a
	// new aggregate cell is created, not once for every event in that cell.
	i, ok := a.index[accumulatorKey{period, string(path), labels}]
	if !ok {
		key := accumulatorKey{period, string(path), labels}
		i = len(a.keys)
		a.index[key] = i
		a.keys = append(a.keys, key)
		a.columns.Append(Group{Period: key.period})
		a.errors = append(a.errors, 0)
		a.durations = append(a.durations, nil)
	}
	g := a.columns.At(i).Aggregate
	var duration *uint64
	if s.layout.Duration >= 0 && values[s.layout.Duration].State == Direct {
		v := unsigned(values[s.layout.Duration].Bytes)
		duration = &v
	}
	if e := g.add(duration); e != nil {
		return e
	}
	a.columns.Count[i], a.columns.DurationCount[i], a.columns.Sum[i], a.columns.Min[i], a.columns.Max[i] = g.Count, g.DurationCount, g.Sum, g.Min, g.Max
	if b.branch == Error {
		a.errors[i]++
	}
	if duration != nil {
		if a.durations[i] == nil {
			a.durations[i] = map[uint64]uint64{}
		}
		a.durations[i][*duration]++
	}
	return nil
}
func (a *summaryAccumulator) snapshot(run Run, l *Layout, trie *sharedTrie) (Summary, error) {
	groups := map[aggregateKey]Aggregate{}
	type statKey struct {
		period int64
		labels [4]string
	}
	stats := map[statKey]*statisticsGroup{}
	hist := map[statKey]map[uint64]uint64{}
	for i, k := range a.keys {
		path := []byte(k.path)
		var ref uint16
		for _, field := range l.Path {
			if len(path) == 0 {
				break
			}
			if len(path) < 4 {
				return Summary{}, fmt.Errorf("invalid accumulator path")
			}
			n := int(order.Uint32(path))
			path = path[4:]
			if n > len(path) {
				return Summary{}, fmt.Errorf("invalid accumulator field")
			}
			next, ok := trie.edges[sharedKey{ref, uint16(field), string(path[:n])}]
			if !ok {
				break
			}
			ref = next
			path = path[n:]
		}
		key := aggregateKey{k.period, ref}
		g := groups[key]
		if e := g.merge(a.columns.At(i).Aggregate); e != nil {
			return Summary{}, e
		}
		groups[key] = g
		sk := statKey{k.period, k.labels}
		st := stats[sk]
		if st == nil {
			st = &statisticsGroup{Period: k.period, Labels: k.labels}
			stats[sk] = st
			hist[sk] = map[uint64]uint64{}
		}
		st.Count += a.columns.Count[i]
		st.Errors += a.errors[i]
		for value, count := range a.durations[i] {
			hist[sk][value] += count
		}
	}
	out := makeSummary(run, groups)
	if l.Duration >= 0 {
		out.DurationWidth = byte(l.Widths[l.Duration])
	}
	for k, st := range stats {
		for v, n := range hist[k] {
			st.Durations = append(st.Durations, [2]uint64{v, n})
		}
		sort.Slice(st.Durations, func(i, j int) bool { return st.Durations[i][0] < st.Durations[j][0] })
		out.Statistics = append(out.Statistics, *st)
	}
	sort.Slice(out.Statistics, func(i, j int) bool {
		a, b := out.Statistics[i], out.Statistics[j]
		if a.Period != b.Period {
			return a.Period < b.Period
		}
		for k := range a.Labels {
			if a.Labels[k] != b.Labels[k] {
				return a.Labels[k] < b.Labels[k]
			}
		}
		return false
	})
	_, e := validateSummary(out)
	return out, e
}
func (s *Store) rememberPendingValues(b *branchStore, p pending, values []Value) error {
	if e := s.rememberLive(b, p, ProvisionalRow, true); e != nil {
		return e
	}
	return s.accumulate(b, p.id, values)
}
