package cache

import (
	"bytes"
	"container/heap"
	"context"
	"fmt"
	"time"
)

type Query struct {
	Branch   Branch
	From, To time.Time
	Equals   map[string]Value
}
type cursor struct{ at, end uint64 }
type cursors struct {
	branch *branchStore
	base   uint64
	values []cursor
	rows   []byte
	width  int
}

func (h cursors) Len() int { return len(h.values) }
func (h cursors) Less(i, j int) bool {
	return h.values[i].at < h.values[j].at
}
func (h cursors) Swap(i, j int) { h.values[i], h.values[j] = h.values[j], h.values[i] }
func (h *cursors) Push(x any)   { h.values = append(h.values, x.(cursor)) }
func (h *cursors) Pop() any {
	n := len(h.values) - 1
	v := h.values[n]
	h.values = h.values[:n]
	return v
}
func (s *Store) frame(b *branchStore, number uint64, dst []byte) ([]byte, error) {
	if raw := b.live.row(b.rowTime(number), number, s.config.Now()); raw != nil {
		return append(dst[:0], raw...), nil
	}
	reader := podsReader{point: true}
	raw, e := reader.read(b, number, s.row)
	if e != nil {
		return nil, e
	}
	row := b.assemblyRow(number)
	return s.assemble(row, raw, dst, b.rowLeaf(number))
}

// expandFrame is shared by storage readers after payload decoding. Keeping
// identity restoration, trie expansion and PIL output here also permits codec
// measurements without changing the search path or introducing transport IPC.
func (s *Store) expandFrame(row []byte, f *Frame, dst []byte, leaf uint16) ([]byte, error) {
	raw, err := s.layout.Layout.Encode(nil, f.Values)
	if err != nil {
		return nil, err
	}
	return s.assemble(row, raw, dst, leaf)
}
func (l *Layout) equal(i int, a, b Value) bool {
	if a.State == Absent || a.State == Null || b.State == Absent || b.State == Null {
		return a.State == b.State
	}
	field := l.Definition.Fields[i]
	if len(field.Values) > 0 {
		if a.State == Direct {
			n := unsigned(a.Bytes)
			if n >= uint64(len(field.Values)) {
				return false
			}
			a = Text(field.Values[n])
		}
		if b.State == Direct {
			n := unsigned(b.Bytes)
			if n >= uint64(len(field.Values)) {
				return false
			}
			b = Text(field.Values[n])
		}
	}
	a = l.ShortValue(i, a)
	b = l.ShortValue(i, b)
	return a.State == b.State && bytes.Equal(a.Bytes, b.Bytes)
}

// Search traverses time-indexed runs. Callback bytes are borrowed until the
// next callback. Source rows are
// stable logical addresses in the returned branch generation.
func (s *Store) Search(ctx context.Context, q Query, visit func(uint64, []byte) error) error {
	_, point := q.Equals[s.layout.Definition.Resource.Roles.Object]
	return s.search(ctx, q, visit, s.assembledReader(point))
}
func (s *Store) assembledReader(point ...bool) func(*branchStore, uint64, []byte) ([]byte, error) {
	reader := podsReader{point: len(point) > 0 && point[0]}
	assembler := frameAssembler{layout: s.layout, trie: &s.trie}
	return func(b *branchStore, number uint64, dst []byte) ([]byte, error) {
		id := b.assemblyRow(number)
		if raw := b.live.row(b.rowTime(number), number, s.config.Now()); raw != nil {
			return append(dst[:0], raw...), nil
		}
		raw, e := reader.read(b, number, s.row)
		if e != nil {
			return nil, e
		}
		return assembler.assemble(id, raw, dst, b.rowLeaf(number))
	}
}

// SearchLive includes durable but unfinalized PIL frames without assigning them
// physical row references. Replication/object postings use committed Search.
func (s *Store) SearchLive(ctx context.Context, q Query, visit func([]byte) error) error {
	_, point := q.Equals[s.layout.Definition.Resource.Roles.Object]
	return s.searchView(ctx, q, func(_ uint64, raw []byte) error { return visit(raw) }, s.assembledReader(point), true)
}

// A reader parameter keeps traversal identical when measuring payload codecs.
func (s *Store) search(ctx context.Context, q Query, visit func(uint64, []byte) error, frame func(*branchStore, uint64, []byte) ([]byte, error)) error {
	return s.searchView(ctx, q, visit, frame, false)
}
func (s *Store) searchView(ctx context.Context, q Query, visit func(uint64, []byte) error, frame func(*branchStore, uint64, []byte) ([]byte, error), live bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !live {
		s.waitSealing()
	}
	if e := s.check(); e != nil {
		return e
	}
	if !q.Branch.Valid() || !q.From.Before(q.To) {
		return fmt.Errorf("invalid query")
	}
	wanted := map[int]Value{}
	for key, v := range q.Equals {
		found := false
		for i, f := range s.layout.Definition.Fields {
			if f.Name == key {
				wanted[i] = v
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown predicate field %q", key)
		}
	}
	scanPredicate := s.layout.Scan
	objectID, objectLookup := wanted[s.layout.Object]
	if objectLookup {
		if !s.config.ObjectIndex || s.objects == nil {
			return ErrObjectIndexDisabled
		}
		if q.Branch != Write {
			return fmt.Errorf("collection.idx supports object lookup on writes only")
		}
		if e := s.validateObject(objectID); e != nil {
			return e
		}
	}
	b := s.branches[q.Branch]
	if b == nil {
		return nil
	}
	if !objectLookup || !live {
		if e := s.flush(b); e != nil {
			return s.fail(e)
		}
	}
	b.live.prune(s.config.Now())
	var candidates []cursor
	if objectLookup {
		rows, e := s.objectRows(b, objectID, q.From, q.To)
		if e != nil {
			return e
		}
		for _, row := range rows {
			candidates = append(candidates, cursor{row, row + 1})
		}
	} else {
		candidates = b.candidates(q.From, q.To, s.row.Width)
	}
	h := cursors{branch: b, base: b.baseRows, rows: b.rows, width: s.row.Width, values: candidates}
	heap.Init(&h)
	var output []byte
	var f Frame
	// Shared predicates are resolved once per referenced prefix, not per record.

	var staged []pending
	if live {
		var object []byte
		if objectLookup {
			object = s.layout.NormalizeIntegerWidth(s.layout.Object, objectID).Bytes
		}
		staged = b.pendingWindow(object, q.From, q.To)
	}
	for h.Len() > 0 || len(staged) > 0 {
		if e := ctx.Err(); e != nil {
			return e
		}
		if len(staged) > 0 && (h.Len() == 0 || eventTime(staged[0].id[:]) < b.rowTime(h.values[0].at)) {
			p := staged[0]
			staged = staged[1:]
			if len(wanted) > 0 {
				if e := scanPredicate(p.frame, &f); e != nil {
					return e
				}
			}
			match := true
			for i, v := range wanted {
				if !s.layout.equal(i, f.Values[i], v) {
					match = false
					break
				}
			}
			if match {
				if e := visit(ProvisionalRow, p.frame); e != nil {
					return e
				}
			}
			continue
		}
		c := h.values[0]
		number := c.at
		h.values[0].at++
		if h.values[0].at == c.end {
			heap.Pop(&h)
		} else {
			heap.Fix(&h, 0)
		}
		var e error
		output, e = frame(b, number, output)
		if e != nil {
			return e
		}
		if len(wanted) > 0 {
			if e = scanPredicate(output, &f); e != nil {
				return e
			}
			match := true
			for i, v := range wanted {
				if !s.layout.equal(i, f.Values[i], v) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		if e = visit(number, output); e != nil {
			return e
		}
	}
	return nil
}

// PeriodRanges returns a coherent committed snapshot. End=nil retains its
// source-tail meaning; consumers must resolve it against this returned tail.
func (s *Store) PeriodRanges(branch Branch, year, day int) ([]RowRange, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, 0, e
	}
	b := s.branches[branch]
	if b == nil {
		return nil, 0, nil
	}
	tail := b.tail()
	from := time.Date(year, 1, day, 0, 0, 0, 0, time.UTC)
	var out []RowRange
	b.visitPeriods(from, from.AddDate(0, 0, 1), func(p *Period) { out = append(out, p.Ranges...) })
	return out, tail, nil
}
