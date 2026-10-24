package cache

// Checkpoint catch-up and live indexing. Callers hold the branch search gate.
import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

func (s *viewSource) searchRequest() searchRequest {
	l := s.store.layout
	fields := make([]string, len(l.Definition.Fields))
	for i, f := range l.Definition.Fields {
		fields[i] = searchKind(f.Type, len(f.Values) > 0)
		if i == l.Event {
			fields[i] = "x"
		}
	}
	first := ^uint64(0)
	if len(s.snap.runs) > 0 {
		first = s.snap.searchRef(s.snap.runs[0].Start)
	}
	return searchRequest{FirstRef: first, Path: filepath.Join(s.store.dir, s.branch.Name()+".search"), Binding: s.binding.Definition + ":" + s.binding.Generation, Fields: fields, Base: s.binding.Retired, Tail: s.binding.Tail}
}
func (s *viewSource) catchupSearch(ctx context.Context, r searchRequest) (searchResponse, error) {
	s.store.mu.Lock()
	if s.store.closed {
		s.store.mu.Unlock()
		return searchResponse{}, fmt.Errorf("search source closed")
	}
	s.store.searchTouched = true
	s.store.mu.Unlock()
	r.Op = "status"
	reply, e := searchCall(ctx, r)
	if e != nil {
		return reply, e
	}
	for row := reply.Tail; row < s.binding.Tail; {
		if e = ctx.Err(); e != nil {
			return reply, e
		}
		r.Docs = nil
		size := 0
		for row = s.snap.nextRecord(row); row < s.binding.Tail && len(r.Docs) < 2048 && size < 4<<20; {
			row = s.snap.nextRecord(row)
			if row >= s.binding.Tail {
				break
			}
			raw, e := s.fullFrame(row)
			if e != nil {
				return reply, e
			}
			d, e := searchDocument(s.store.layout, s.snap.searchRef(row), raw)
			d.SourceRow = row
			if e != nil {
				return reply, e
			}
			r.Docs = append(r.Docs, d)
			size += len(raw)
			row++
		}
		r.Op = "append"
		r.Tail = row
		reply, e = searchCall(ctx, r)
		if e != nil {
			return reply, e
		}
	}
	return reply, nil
}

// Catch up a reusable live index. The helper publishes its reader before replying.
// Repeat queries send only missing event documents, never rebuild the tail.
func (s *viewSource) catchupLive(ctx context.Context, r searchRequest) error {
	r.Op = "live_status"
	r.Docs = nil
	r.Ranges = nil
	r.IDs = nil
	reply, e := searchCall(ctx, r)
	if e != nil {
		return e
	}
	known := make(map[string]bool, len(reply.IDs))
	for _, id := range reply.IDs {
		known[id] = true
	}
	size := 0
	flush := func() error {
		if len(r.Docs) == 0 {
			return nil
		}
		r.Op = "live_append"
		_, e := searchCall(ctx, r)
		r.Docs = nil
		size = 0
		return e
	}
	for _, p := range s.pending {
		id := hex.EncodeToString(p.id[:])
		if known[id] {
			continue
		}
		d, e := searchDocument(s.store.layout, 0, p.frame)
		if e != nil {
			return e
		}
		d.ID = id
		r.Docs = append(r.Docs, d)
		size += len(p.frame)
		if len(r.Docs) >= 2048 || size >= 4<<20 {
			if e = flush(); e != nil {
				return e
			}
		}
	}
	return flush()
}
