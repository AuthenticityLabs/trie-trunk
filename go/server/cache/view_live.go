package cache

import (
	"fmt"
	"time"
)

// Live views pin immutable frame references, not disk rows. Continuations keep
// the original pending set even if it is finalized or new late events arrive.
// A bounded, expiring registry prevents abandoned cursors from retaining frames
// indefinitely. Eviction is an explicit restart, never a drifting page.
type liveViewSnapshot struct {
	events  []pending
	expires time.Time
}

const maxLiveViews = 64

func (b *branchStore) captureLive(from, to time.Time, prior *viewBinding) (string, []pending, error) {
	now := time.Now()
	for key, v := range b.liveViews {
		if !now.Before(v.expires) {
			delete(b.liveViews, key)
		}
	}
	if prior != nil {
		if prior.Live == "" {
			return "", nil, nil
		}
		v, ok := b.liveViews[prior.Live]
		if !ok {
			return "", nil, fmt.Errorf("live snapshot expired; restart search")
		}
		return prior.Live, v.events, nil
	}
	values := b.pendingWindow(nil, from, to)
	if len(values) == 0 {
		return "", nil, nil
	}
	key := viewDigest(struct {
		Version  uint64
		From, To int64
		Count    int
	}{b.pendingVersion, from.UnixNano(), to.UnixNano(), len(values)})
	if v, ok := b.liveViews[key]; ok {
		v.expires = now.Add(15 * time.Minute)
		b.liveViews[key] = v
		return key, v.events, nil
	}
	if b.liveViews == nil {
		b.liveViews = map[string]liveViewSnapshot{}
	}
	if len(b.liveViews) >= maxLiveViews {
		oldest := ""
		var expiry time.Time
		for k, v := range b.liveViews {
			if oldest == "" || v.expires.Before(expiry) {
				oldest = k
				expiry = v.expires
			}
		}
		delete(b.liveViews, oldest)
	}
	values = append([]pending(nil), values...)
	b.liveViews[key] = liveViewSnapshot{values, now.Add(15 * time.Minute)}
	return key, values, nil
}
func (s *viewSource) eventID(row uint64) []byte {
	if row >= s.binding.Tail {
		return s.pending[row-s.binding.Tail].id[:]
	}
	return s.context.identity(&s.snap, row)
}
func (s *viewSource) fullFrame(row uint64) ([]byte, error) {
	if row >= s.binding.Tail {
		return s.pending[row-s.binding.Tail].frame, nil
	}
	if raw := s.resident(row); raw != nil {
		return raw, nil
	}
	raw, e := s.reader.read(&s.snap, row, s.store.row)
	if e != nil {
		return nil, e
	}
	return s.assembler.assemble(s.context.identity(&s.snap, row), raw, nil)
}

func (s *viewSource) resident(row uint64) []byte {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	b := s.store.branches[s.branch]
	if b == nil || b.generation != s.snap.generation {
		return nil
	}
	// Background indexing borrows any retained hot frame; age is irrelevant to
	// its immutable contents. It must not advance or consult the ingestion clock.
	var now time.Time
	if s.indexing {
		now = time.UnixMilli(s.context.segment(&s.snap, row).PAC.Placement.From + indexDelta(s.snap.row(row)))
	} else {
		now = s.store.config.Now()
	}
	return b.live.get(s.context.identity(&s.snap, row), false, now)
}
