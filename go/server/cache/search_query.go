package cache

// Candidate pushdown and intersections; canonical snapshot ownership stays in Go.
import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Only lossless scalar predicates are pushed down. Decimal-vs-integer, null,
// absent, UUID, substring, and float precision edge cases stay exact residuals.
func indexedFilters(l *Layout, q *QueryPlan) []searchFilter {
	var out []searchFilter
	for _, f := range q.Filters {
		k := fieldIndex(l, f.Field)
		if k < 0 || k == l.Event || k == l.Object {
			continue
		}
		if f.Value.Kind == "null" {
			v := "1"
			if f.Op == "!=" {
				v = "2"
			}
			out = append(out, searchFilter{K: k, Kind: "p", Op: "==", Value: v})
			continue
		}
		field := l.Definition.Fields[k]
		kind := searchKind(field.Type, len(field.Values) > 0)
		v := f.Value.Value
		var code *uint64
		if f.Op == "contains" && (kind == "s" || kind == "m") {
			out = append(out, searchFilter{K: k, Kind: "c", Op: "contains", Value: strings.ToLower(v)})
			continue
		}
		switch kind {
		case "e":
			v = strings.ToLower(strings.ReplaceAll(v, "-", ""))
		case "s":
			kind = "e"
		case "m":
			if f.Op != "==" && f.Op != "!=" {
				out = append(out, searchFilter{K: k, Kind: "e", Op: f.Op, Value: v})
				continue
			}
			found := -1
			for i, x := range field.Values {
				if x == v {
					found = i
					break
				}
			}
			if found >= 0 {
				n := uint64(found)
				code = &n
			}
		case "u", "i":
			value, ok := integerPredicate(k, kind, f)
			if ok {
				out = append(out, value)
			}
			continue
		case "f":
			bits := 64
			if field.Type == "F32" {
				bits = 32
			}
			value, ok := floatPredicate(k, bits, f)
			if ok {
				out = append(out, value)
			}
			continue
		case "b":
			if f.Op != "==" && f.Op != "!=" {
				continue
			}
		default:
			continue
		}
		out = append(out, searchFilter{K: k, Kind: kind, Op: f.Op, Value: v, Code: code})
	}
	return out
}
func appendSearchRow(spans []cursor, row uint64) []cursor {
	if n := len(spans); n > 0 && spans[n-1].end == row {
		spans[n-1].end++
	} else {
		spans = append(spans, cursor{row, row + 1})
	}
	return spans
}

// Returns a residual plan: fulltext has been evaluated by Tantivy for both
// committed and staged records. Remaining predicates retain Go's exact semantics.
func (s *viewSource) indexedCandidates(ctx context.Context, q *QueryPlan) (*QueryPlan, error) {
	fulltext := q.Search != nil
	enabled := s.store.policyFor(s.branch).SearchIndex
	if enabled != nil && !*enabled {
		if fulltext {
			return nil, fmt.Errorf("full-text indexing is disabled for this resource")
		}
		return q, nil
	}
	filters := indexedFilters(s.store.layout, q)
	if !fulltext && len(filters) == 0 {
		return q, nil
	}
	r := s.searchRequest()
	r.Filters = filters
	if fulltext {
		r.Text = q.Search.Text
		if q.Search.Mode == "contains" {
			r.Text = strings.ToLower(r.Text)
		}
		r.Mode = q.Search.Mode
	}
	gate := s.store.searchGate(s.branch)
	if e := gate.LockContext(ctx); e != nil {
		return nil, e
	}
	_, catchupErr := s.catchupSearch(ctx, r)
	gate.Unlock()
	if e := catchupErr; e != nil {
		return nil, fmt.Errorf("search index catch-up: %w", e)
	}
	var pendingRanges [][2]uint64
	for _, c := range s.ranges {
		if c.at < s.binding.Tail {
			end := min(c.end, s.binding.Tail)
			for at := c.at; at < end; {
				run := s.snap.runs[s.snap.runIndex(at)]
				through := min(end, run.End)
				r.Ranges = append(r.Ranges, [2]uint64{s.snap.searchRef(at), s.snap.searchRef(through-1) + 1})
				at = through
			}
		}
		if c.end > s.binding.Tail {
			pendingRanges = append(pendingRanges, [2]uint64{max(c.at, s.binding.Tail), c.end})
		}
	}
	if len(r.Ranges) > 1 {
		merged := r.Ranges[:1]
		for _, span := range r.Ranges[1:] {
			if merged[len(merged)-1][1] == span[0] {
				merged[len(merged)-1][1] = span[1]
			} else {
				merged = append(merged, span)
			}
		}
		r.Ranges = merged
	}
	r.Op = "query"
	reply, e := searchCall(ctx, r)
	if e != nil {
		return nil, e
	}
	var selected []cursor
	for _, ref := range reply.Rows {
		row, e := s.context.resolve(&s.snap, ref)
		if e != nil {
			return nil, e
		}
		selected = appendSearchRow(selected, row)
	}
	// Live references are UUIDs; finalized source rows never leak into this index.
	if len(pendingRanges) > 0 {
		if e := gate.LockContext(ctx); e != nil {
			return nil, e
		}
		defer gate.Unlock()
		if e := s.catchupLive(ctx, r); e != nil {
			return nil, e
		}
		live := r
		live.Op = "live_query"
		live.Ranges = nil
		live.Docs = nil
		positions := map[string]uint64{}
		for _, span := range pendingRanges {
			for row := span[0]; row < span[1]; row++ {
				id := hex.EncodeToString(s.pending[row-s.binding.Tail].id[:])
				live.IDs = append(live.IDs, id)
				positions[id] = row
			}
		}
		result, e := searchCall(ctx, live)
		if e != nil {
			return nil, e
		}
		rows := make([]uint64, 0, len(result.IDs))
		for _, id := range result.IDs {
			if row, ok := positions[id]; ok {
				rows = append(rows, row)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i] < rows[j] })
		for _, row := range rows {
			selected = appendSearchRow(selected, row)
		}
	}

	s.ranges = selected
	residual := *q
	residual.Filters = nil
	for _, f := range q.Filters {
		if len(indexedFilters(s.store.layout, &QueryPlan{Filters: []QueryFilter{f}})) == 0 {
			residual.Filters = append(residual.Filters, f)
		}
	}
	if fulltext {
		residual.Search = nil
	}
	return &residual, nil
}
