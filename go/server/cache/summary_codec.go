package cache

import (
	"fmt"
)

// Coarser rollups retain fixed REF16 + five U64 aggregate rows.
// Five-minute .sum columns and daily compression are in summary_columns.go
// and summary_days.go, respectively.
const aggregateRowSize = 42
const rollupHeaderSize = 24

func validAggregate(a Aggregate) bool {
	if a.Count == 0 || a.DurationCount > a.Count {
		return false
	}
	if a.DurationCount == 0 {
		return a.Sum == 0 && a.Min == 0 && a.Max == 0
	}
	return a.Min <= a.Max && a.Max <= a.Sum && a.Min <= a.Sum/a.DurationCount &&
		(a.Sum/a.DurationCount < a.Max || a.Sum/a.DurationCount == a.Max && a.Sum%a.DurationCount == 0)
}
func putAggregate(dst []byte, g Group) {
	order.PutUint16(dst, g.Reference)
	order.PutUint64(dst[2:], g.Aggregate.Count)
	order.PutUint64(dst[10:], g.Aggregate.DurationCount)
	order.PutUint64(dst[18:], g.Aggregate.Sum)
	order.PutUint64(dst[26:], g.Aggregate.Min)
	order.PutUint64(dst[34:], g.Aggregate.Max)
}
func getAggregate(raw []byte, period int64) Group {
	return Group{period, order.Uint16(raw), Aggregate{order.Uint64(raw[2:]), order.Uint64(raw[10:]), order.Uint64(raw[18:]), order.Uint64(raw[26:]), order.Uint64(raw[34:])}}
}
func validateRollup(r rollup) error {
	if r.From >= r.To {
		return fmt.Errorf("invalid aggregate period")
	}
	for i, g := range r.Groups {
		if g.Period != r.From || !validAggregate(g.Aggregate) || i > 0 && g.Reference <= r.Groups[i-1].Reference {
			return fmt.Errorf("invalid rollup group")
		}
	}
	return nil
}
func encodeRollup(r rollup) ([]byte, error) {
	if e := validateRollup(r); e != nil {
		return nil, e
	}
	if len(r.Groups) > (MaxFrame-rollupHeaderSize)/aggregateRowSize {
		return nil, fmt.Errorf("rollup too large")
	}
	raw := make([]byte, rollupHeaderSize+len(r.Groups)*aggregateRowSize)
	copy(raw, "AGG1")
	order.PutUint64(raw[4:], uint64(r.From))
	order.PutUint64(raw[12:], uint64(r.To))
	order.PutUint32(raw[20:], uint32(len(r.Groups)))
	for i, g := range r.Groups {
		putAggregate(raw[rollupHeaderSize+i*aggregateRowSize:], g)
	}
	return raw, nil
}
func decodeRollup(raw []byte, r *rollup) error {
	if len(raw) < rollupHeaderSize || len(raw) > MaxFrame || string(raw[:4]) != "AGG1" {
		return fmt.Errorf("invalid rollup encoding")
	}
	count := uint64(order.Uint32(raw[20:]))
	if uint64(rollupHeaderSize)+count*aggregateRowSize != uint64(len(raw)) {
		return fmt.Errorf("invalid rollup size")
	}
	out := rollup{From: int64(order.Uint64(raw[4:])), To: int64(order.Uint64(raw[12:])), Groups: make([]Group, int(count))}
	for i := range out.Groups {
		out.Groups[i] = getAggregate(raw[rollupHeaderSize+i*aggregateRowSize:], out.From)
	}
	if e := validateRollup(out); e != nil {
		return e
	}
	*r = out
	return nil
}
