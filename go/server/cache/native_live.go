package cache

/*
#include "native_live.h"
*/
import "C"
import (
	"fmt"
	"slices"
	"sort"
	"time"
	"unsafe"
)

// The live index contains native addresses only. Hour arenas own those bytes;
// the store lock protects mutations and snapshots pin arenas before escaping it.
type nativeLive struct{ ptr unsafe.Pointer }

func newNativeLive(limit int) *nativeLive { return &nativeLive{C.trunk_live_new(C.size_t(limit))} }
func (l *nativeLive) close() {
	if l != nil && l.ptr != nil {
		C.trunk_live_free(l.ptr)
		l.ptr = nil
	}
}
func (l *nativeLive) count(kind byte) int {
	if l == nil {
		return 0
	}
	return int(C.trunk_live_count(l.ptr, C.uint8_t(kind)))
}
func (l *nativeLive) room(now time.Time) bool {
	return bool(C.trunk_live_room(l.ptr, C.int64_t(now.Add(-HotWindow).UnixMilli())))
}
func (l *nativeLive) prune(now time.Time) {
	if l != nil {
		C.trunk_live_prune(l.ptr, C.int64_t(now.Add(-HotWindow).UnixMilli()))
	}
}
func (l *nativeLive) get(id []byte, pending bool, now time.Time) []byte {
	if l == nil || len(id) != 16 || (!pending && eventTime(id) < now.Add(-HotWindow).UnixMilli()) {
		return nil
	}
	var key C.TrunkLiveId
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&key.bytes[0])), 16), id)
	r := C.trunk_live_get(l.ptr, key, C.bool(pending), C.int64_t(now.Add(-HotWindow).UnixMilli()))
	if r.length == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(r.address), int(r.length))
}
func (l *nativeLive) revision(key collectionRevision) bool {
	return l != nil && bool(C.trunk_live_revision(l.ptr, (*C.uint8_t)(unsafe.Pointer(&key.Object[0])), C.uint64_t(key.Revision)))
}
func (s *Store) rememberLive(b *branchStore, p pending, row uint64, staged bool) error {
	var object *collectionRevision
	var revision collectionRevision
	if b.branch == Write && s.config.ObjectIndex && s.layout.Object >= 0 {
		var f Frame
		if e := s.layout.identityFrame(p.frame, &f); e != nil {
			return e
		}
		if f.Values[s.layout.Revision].State != Absent {
			var e error
			revision, e = s.collectionRevision(f.Values)
			if e != nil {
				return e
			}
			object = &revision
		}
	}
	if !b.live.insert(p, object, row, staged, s.config.Now().Add(-HotWindow).UnixMilli()) {
		return fmt.Errorf("%w: native live index record limit", ErrStagingFull)
	}
	if staged {
		b.pendingVersion++
		b.pendingBytes += len(p.frame)
	}
	return nil
}
func (s *Store) rememberPending(b *branchStore, p pending) error {
	var f Frame
	if e := s.layout.Scan(p.frame, &f); e != nil {
		return e
	}
	return s.rememberPendingValues(b, p, f.Values)
}
func (s *Store) rememberSealed(b *branchStore, id [16]byte, raw []byte, row uint64) error {
	return s.rememberLive(b, pending{id, raw}, row, false)
}

// Native snapshots own only descriptors. Callers pin the payload hour arenas
// before letting a snapshot escape the writer lock.
type liveResult struct {
	capacity int
	records  []C.TrunkLiveRecord
}

func (r liveResult) close() {
	if r.capacity > 0 {
		C.trunk_live_result_free(unsafe.SliceData(r.records), C.size_t(len(r.records)), C.size_t(r.capacity))
	}
}
func (l *nativeLive) snapshot(object []byte, kind byte, from, to time.Time) liveResult {
	return l.snapshotAt(object, kind, from, to, -1<<63)
}
func (l *nativeLive) snapshotAt(object []byte, kind byte, from, to time.Time, cutoff int64) liveResult {
	if l == nil {
		return liveResult{}
	}
	var obj *C.uint8_t
	if len(object) == 16 {
		obj = (*C.uint8_t)(unsafe.Pointer(&object[0]))
	}
	r := C.trunk_live_snapshot(l.ptr, obj, C.uint8_t(kind), C.int64_t(timeBound(from)), C.int64_t(timeBound(to)), C.int64_t(cutoff))
	return liveResult{int(r.capacity), unsafe.Slice(r.data, int(r.len))}
}
func (l *nativeLive) visit(object []byte, kind byte, from, to time.Time, visit func(C.TrunkLiveRecord)) {
	r := l.snapshot(object, kind, from, to)
	defer r.close()
	for _, record := range r.records {
		visit(record)
	}
}
func (b *branchStore) residentHistory(object []byte, from, to, now time.Time, source *historySource, binding string, h *historySnapshot) bool {
	if from.Before(now.Add(-HotWindow)) {
		return false
	}
	r := b.live.snapshotAt(object, 2, from, to, now.Add(-HotWindow).UnixMilli())
	defer r.close()
	h.locations = slices.Grow(h.locations, len(r.records))
	for _, record := range r.records {
		row := uint64(record.row)
		if row != ProvisionalRow && (row < b.baseRows || row >= b.tail()) {
			continue
		}
		var id [16]byte
		copy(id[:], unsafe.Slice((*byte)(unsafe.Pointer(&record.id[0])), 16))
		h.locations = append(h.locations, historyLocation{source: source, row: row, binding: binding, id: id, raw: unsafe.Slice((*byte)(record.address), int(record.length))})
	}
	return true
}
func (b *branchStore) pendingWindow(object []byte, from, to time.Time) []pending {
	var out []pending
	b.live.visit(object, 0, from, to, func(r C.TrunkLiveRecord) {
		var id [16]byte
		copy(id[:], unsafe.Slice((*byte)(unsafe.Pointer(&r.id[0])), 16))
		out = append(out, pending{id, unsafe.Slice((*byte)(r.address), int(r.length))})
	})
	return out
}
func (b *branchStore) pendingAll() []pending {
	return b.pendingWindow(nil, time.UnixMilli(0), time.UnixMilli(1<<48))
}
func (b *branchStore) residentObjectRows(id []byte, from, to, now time.Time) ([]uint64, bool) {
	if len(id) != 16 || from.Before(now.Add(-HotWindow)) {
		return nil, false
	}
	b.live.prune(now)
	var rows []uint64
	b.live.visit(id, 1, from, to, func(r C.TrunkLiveRecord) {
		row := uint64(r.row)
		if row >= b.baseRows && row < b.tail() {
			rows = append(rows, row)
		}
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i] < rows[j] })
	return rows, true
}
func (b *branchStore) finishPending(cutoff int64) { C.trunk_live_finish(b.live.ptr, C.int64_t(cutoff)) }

func (l *nativeLive) firstPending() int64 { return int64(C.trunk_live_first_pending(l.ptr)) }

// LiveIndexStats reports identities, not payload bytes. Allocator/bucket overhead
// remains native; the configured entry cap bounds it independently of buffers.
type LiveIndexStats struct{ Pending, Resident, Identities, Limit int }

func (s *Store) LiveIndexes() map[Branch]LiveIndexStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[Branch]LiveIndexStats{}
	for branch, b := range s.branches {
		if b.live != nil {
			out[branch] = LiveIndexStats{b.live.count(0), b.live.count(1), b.live.count(2), s.config.LiveIndexRecords}
		}
	}
	return out
}

func (l *nativeLive) insert(p pending, key *collectionRevision, row uint64, staged bool, cutoff int64) bool {
	var r C.TrunkLiveRecord
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&r.id[0])), 16), p.id[:])
	r.address = unsafe.Pointer(&p.frame[0])
	r.length = C.size_t(len(p.frame))
	r.row = C.uint64_t(row)
	var object *C.uint8_t
	var rev C.uint64_t
	if key != nil {
		object = (*C.uint8_t)(unsafe.Pointer(&key.Object[0]))
		rev = C.uint64_t(key.Revision)
	}
	return bool(C.trunk_live_put(l.ptr, r, object, rev, C.bool(staged), C.int64_t(cutoff)))
}
