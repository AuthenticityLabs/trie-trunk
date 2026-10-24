package cache

// Validate and durably stage ingress; sealing assigns canonical row positions.
import (
	"bytes"
	"errors"
	"fmt"
	"trie-trunk/go/codec"
)

// Row IDs are assigned only after the three-cycle finalization boundary.
func (s *Store) Append(branch Branch, raw []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	duplicate, e := s.appendStaged(branch, raw, true)
	if e == nil {
		s.queueSearch(branch)
	}
	return duplicate, e
}

// AppendBatch acknowledges a durable prefix with one swap sync. The returned
// count includes idempotent duplicates, and stops before the first rejection.
func (s *Store) AppendBatch(branch Branch, frames [][]byte) (count int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, raw := range frames {
		if _, err = s.appendStaged(branch, raw, false); err != nil {
			break
		}
		count++
	}
	if b := s.branches[branch]; b != nil && b.swap != nil {
		if e := b.swap.Sync(); e != nil {
			return 0, s.fail(e)
		}
	}
	if count > 0 {
		s.queueSearch(branch)
	}
	return count, err
}
func (s *Store) appendStaged(branch Branch, raw []byte, sync bool) (bool, error) {
	return s.appendOwned(branch, raw, sync, nil)
}
func (s *Store) appendOwned(branch Branch, raw []byte, sync bool, input *nativeBuffer) (bool, error) {
	if e := s.check(); e != nil {
		return false, e
	}
	original := raw
	if !branch.Valid() {
		return false, fmt.Errorf("invalid branch")
	}
	if e := s.layout.Scan(raw, &s.scratch); e != nil {
		return false, s.rejectInput(branch, original, "invalid_pil", e)
	}
	if s.layout.Shared >= 0 && s.scratch.Values[s.layout.Shared].State != Absent {
		if e := s.expandShared(s.scratch.Values); e != nil {
			return false, s.rejectInput(branch, original, "invalid_reference", e)
		}
		expanded, e := s.layout.Encode(nil, s.scratch.Values)
		if e != nil {
			return false, e
		}
		raw = expanded
		if e = s.layout.Scan(raw, &s.scratch); e != nil {
			return false, e
		}
	}
	// Supplied identity is validated before transforms and can never be overwritten.
	identity := s.scratch.Values[s.layout.Event]
	if identity.State != Absent {
		if _, e := eventID(identity); e != nil {
			return false, s.rejectInput(branch, original, "invalid_event_id", e)
		}
	}
	if s.transforms != nil {
		transformed, e := s.transform(s.scratch.Values)
		if e != nil {
			return false, s.rejectInput(branch, original, "transform_definition_mismatch", e)
		}
		raw = transformed
		if e = s.layout.Scan(raw, &s.scratch); e != nil {
			return false, s.rejectInput(branch, original, "transform_definition_mismatch", e)
		}
	}
	if identity.State == Absent {
		generated, e := codec.NewUUIDv7(s.config.Now())
		if e != nil {
			return false, e
		}
		s.scratch.Values[s.layout.Event] = Value{Direct, generated[:]}
		raw, e = s.layout.Encode(nil, s.scratch.Values)
		if e != nil {
			return false, s.rejectInput(branch, original, "invalid_pil", e)
		}
	}
	id, e := eventID(s.scratch.Values[s.layout.Event])
	if e != nil {
		return false, s.rejectInput(branch, original, "invalid_event_id", e)
	}
	b, e := s.openBranch(branch)
	if e != nil {
		return false, s.fail(e)
	}
	if old := b.live.get(id[:], true, s.config.Now()); old != nil {
		if s.layout.sameEvent(old, raw) {
			return true, nil
		}
		return false, fmt.Errorf("conflicting event_id")
	}
	if duplicate, e := s.committedDuplicate(b, id, raw); duplicate || e != nil {
		return duplicate, e
	}
	if branch == Write && s.config.ObjectIndex {
		if _, e := s.checkCollectionWrite(b, s.scratch.Values); e != nil {
			var readError *collectionReadError
			if errors.As(e, &readError) {
				return false, s.fail(e)
			}
			return false, s.rejectInput(branch, original, "invalid_collection_revision", e)
		}
	}
	if s.capacityClosed {
		return false, ErrIndexCapacity
	}
	if eventTime(id[:]) < b.ingressBoundary() || len(b.rows) > 0 && bytes.Compare(id[:], b.rowID(b.tail()-1)) <= 0 {
		return false, s.rejectLate(b, id, raw)
	}
	if s.capacityClosed || b.tail()+uint64(b.live.count(0)+1)*(1+IndexHeaderSlots) > s.indexCapacity() {
		return false, ErrIndexCapacity
	}
	if e = s.stagingCapacity(len(raw)); e != nil {
		return false, e
	}
	if !b.live.room(s.config.Now()) {
		return false, fmt.Errorf("%w: native live index record limit", ErrStagingFull)
	}
	owned, e := b.own(s, id, raw, input)
	if e != nil {
		return false, e
	}
	// Receipt is acknowledged only after the complete frame is on stable storage.
	if e = appendRecord(b.swap, raw); e == nil && sync {
		e = b.swap.Sync()
	}
	if e != nil {
		return false, s.fail(e)
	}
	if e = s.invalidateReceived(branch, raw); e != nil {
		return false, s.fail(e)
	}
	copyFrame := owned
	if e = s.rememberPendingValues(b, pending{id, copyFrame}, s.scratch.Values); e != nil {
		return false, s.fail(e)
	}
	return false, nil
}

func (s *Store) committedDuplicate(b *branchStore, id [16]byte, raw []byte) (bool, error) {
	for _, run := range b.runs {
		ms := eventTime(id[:])
		if ms < run.MinMS || ms > run.MaxMS {
			continue
		}
		at := b.lower(run.Start, run.End, ms)
		for at < run.End {
			if b.rowTime(at) != ms {
				break
			}
			if bytes.Equal(b.rowID(at), id[:]) {
				full, e := s.frame(b, at, nil)
				if e != nil {
					return false, e
				}
				if s.layout.sameEvent(full, raw) {
					return true, nil
				}
				return false, fmt.Errorf("conflicting event_id")
			}
			at++
		}
	}
	return false, nil
}

// Native ingress transfers immutable frames to resident hour arenas. The caller
// releases its buffer handles after this call; accepted frames remain native.
func (s *Store) appendNativeBatch(branch Branch, frames []nativeInput) (count int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, frame := range frames {
		if _, err = s.appendOwned(branch, frame.raw, false, frame.buffer); err != nil {
			break
		}
		count++
	}
	if b := s.branches[branch]; b != nil && b.swap != nil {
		if e := b.swap.Sync(); e != nil {
			return 0, s.fail(e)
		}
	}
	if count > 0 {
		s.queueSearch(branch)
	}
	return
}

// A durable PAC seals its whole hour against new arrivals, independently of
// the seed watermark that certifies completeness after cleanup.
func (b *branchStore) ingressBoundary() int64 {
	boundary := max(b.finalizedBefore, b.sealingBefore)
	if len(b.runs) > 0 {
		boundary = max(boundary, b.runs[len(b.runs)-1].PAC.Placement.To)
	}
	return boundary
}
