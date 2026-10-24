package cache

import (
	"context"
	"sort"
)

type ViewCount struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}
type ViewRank struct {
	Service   string  `json:"service"`
	Resource  string  `json:"resource"`
	Operation string  `json:"operation"`
	Value     float64 `json:"value"`
	Samples   uint64  `json:"samples"`
}
type ViewDuration struct {
	Count   uint64  `json:"count"`
	Average float64 `json:"average"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
}
type ViewStatistics struct {
	Complete   bool         `json:"complete"`
	Matched    uint64       `json:"matched"`
	Evaluated  uint64       `json:"evaluated"`
	Services   []ViewCount  `json:"services"`
	Levels     []ViewCount  `json:"levels"`
	Duration   ViewDuration `json:"duration"`
	Operations struct {
		Errors  []ViewRank `json:"errors"`
		Latency []ViewRank `json:"latency"`
	} `json:"operations"`
}
type operationKey struct{ service, resource, operation string }
type operationStats struct {
	errors    uint64
	durations map[uint64]uint64
}

// Exact p95 merges sealed duration distributions and resident live values.
// Cold partial groups remain explicitly incomplete; charts never decode PACs.
// The caller pins generation and tail to the same snapshot as the page/summary.
func viewStatistics(ctx context.Context, sources []*viewSource, observe ...func(statisticsGroup)) (*ViewStatistics, error) {
	out := &ViewStatistics{Services: []ViewCount{}, Levels: []ViewCount{}}
	out.Operations.Errors = []ViewRank{}
	out.Operations.Latency = []ViewRank{}
	services, levels := map[string]uint64{}, map[string]uint64{}
	operations := map[operationKey]*operationStats{}
	add := func(g statisticsGroup) {
		if len(observe) > 0 {
			observe[0](g)
		}
		out.Evaluated += g.Count
		services[g.Labels[0]] += g.Count
		levels[g.Labels[3]] += g.Count
		key := operationKey{g.Labels[0], g.Labels[1], g.Labels[2]}
		group := operations[key]
		if group == nil {
			group = &operationStats{durations: map[uint64]uint64{}}
			operations[key] = group
		}
		group.errors += g.Errors
		for _, pair := range g.Durations {
			value, n := pair[0], pair[1]
			group.durations[value] += n
			d := &out.Duration
			v := float64(value)
			if d.Count == 0 || v < d.Min {
				d.Min = v
			}
			if v > d.Max {
				d.Max = v
			}
			d.Count += n
			d.Average += (v - d.Average) * float64(n) / float64(d.Count)
		}
	}
	for _, s := range sources {
		for _, span := range s.ranges {
			out.Matched += span.end - span.at
		}
		// Whole sealed runs use the accumulator. A partial selection can use live
		// resident frames, but never opens/decompresses a cold PAC for charting.
		covered := map[uint64]uint64{}
		for _, sum := range s.summaries {
			if sum.Through > s.binding.Tail || len(sum.Statistics) == 0 {
				continue
			}
			var selected uint64
			for _, span := range s.ranges {
				a, z := max(span.at, sum.From), min(span.end, sum.Through)
				if a < z {
					selected += z - a
				}
			}
			if selected == sum.Through-sum.From {
				for _, g := range sum.Statistics {
					add(g)
				}
				covered[sum.From] = sum.Through
			}
		}
		var coveredThrough uint64
		var f Frame
		for _, span := range s.ranges {
			for row := span.at; row < span.end; {
				if e := ctx.Err(); e != nil {
					return nil, e
				}
				if row < coveredThrough {
					row = min(span.end, coveredThrough)
					continue
				}
				if end, ok := covered[row]; ok {
					coveredThrough = end
					row = min(span.end, end)
					continue
				}
				var raw []byte
				if row >= s.binding.Tail {
					raw = s.pending[row-s.binding.Tail].frame
				} else {
					raw = s.resident(row)
				}
				row++
				if raw == nil {
					continue
				}
				l := s.api.Store.layout
				if e := l.Scan(raw, &f); e != nil {
					return nil, e
				}
				g := statisticsGroup{Labels: statisticsLabels(l, s.branch, &f), Count: 1}
				if s.branch == Error {
					g.Errors = 1
				}
				if s.branch.Aggregates() && l.Duration >= 0 && f.Values[l.Duration].State == Direct {
					g.Durations = [][2]uint64{{unsigned(f.Values[l.Duration].Bytes), 1}}
				}
				add(g)
			}
		}
	}
	out.Complete = out.Evaluated == out.Matched
	counts := func(values map[string]uint64) []ViewCount {
		rows := make([]ViewCount, 0, len(values))
		for name, n := range values {
			rows = append(rows, ViewCount{name, n})
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Count == rows[j].Count {
				return rows[i].Name < rows[j].Name
			}
			return rows[i].Count > rows[j].Count
		})
		return rows
	}
	out.Services = counts(services)
	out.Levels = counts(levels)
	for key, g := range operations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if g.errors > 0 {
			out.Operations.Errors = append(out.Operations.Errors, ViewRank{key.service, key.resource, key.operation, float64(g.errors), g.errors})
		}
		if len(g.durations) > 0 {
			values := make([]uint64, 0, len(g.durations))
			var n uint64
			for value, count := range g.durations {
				values = append(values, value)
				n += count
			}
			sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
			rank := n - n/20
			var seen, p95 uint64
			for _, value := range values {
				seen += g.durations[value]
				if seen >= rank {
					p95 = value
					break
				}
			}
			out.Operations.Latency = append(out.Operations.Latency, ViewRank{key.service, key.resource, key.operation, float64(p95), n})
		}

	}
	top := func(rows []ViewRank) []ViewRank {
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if a.Value != b.Value {
				return a.Value > b.Value
			}
			if a.Service != b.Service {
				return a.Service < b.Service
			}
			if a.Resource != b.Resource {
				return a.Resource < b.Resource
			}
			return a.Operation < b.Operation
		})
		return rows[:min(10, len(rows))]
	}
	out.Operations.Errors = top(out.Operations.Errors)
	out.Operations.Latency = top(out.Operations.Latency)
	return out, nil
}
func statLabel(f Field, v Value) string {
	if v.State == Absent || v.State == Null {
		return ""
	}
	if v.State == Direct && len(f.Values) > 0 {
		return f.Values[unsigned(v.Bytes)]
	}
	if f.Type == "STRING" {
		if len(v.Bytes) == 0 {
			return "(empty)"
		}
		return string(v.Bytes)
	}
	return ""
}
