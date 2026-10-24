package cache

/*
#include "native_memory.h"
*/
import "C"
import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// NativeMemoryStats counts requested native buffer capacities, including reader
// pins and temporary ingress. Allocator metadata and index memory are separate.
type NativeMemoryStats struct{ Used, Peak, Limit uint64 }
type nativeMemory struct {
	ingressMu sync.Mutex
	idle      [ingressIdleChunks]ingressSlot
	closed    atomic.Bool
	ptr       unsafe.Pointer
	limit     uint64
}

func newNativeMemory(limit uint64) *nativeMemory {
	return &nativeMemory{ptr: C.trunk_memory_new(C.uint64_t(limit)), limit: limit}
}
func (m *nativeMemory) close() {
	if m == nil {
		return
	}
	m.ingressMu.Lock()
	defer m.ingressMu.Unlock()
	if m.ptr != nil {
		m.closed.Store(true)
		for i := range m.idle {
			if c := m.idle[i].value.Swap(nil); c != nil {
				c.buffer.close()
			}
		}
		C.trunk_memory_free(m.ptr)
		m.ptr = nil
	}
}
func (m *nativeMemory) stats() NativeMemoryStats {
	if m == nil || m.ptr == nil {
		return NativeMemoryStats{}
	}
	return NativeMemoryStats{uint64(C.trunk_memory_used(m.ptr)), uint64(C.trunk_memory_peak(m.ptr)), m.limit}
}
func (s *Store) NativeMemory() NativeMemoryStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memory.stats()
}

type nativeBuffer struct {
	ptr    unsafe.Pointer
	bytes  []byte
	frozen bool
}

func (m *nativeMemory) buffer() *nativeBuffer { return &nativeBuffer{ptr: C.trunk_buffer_new(m.ptr)} }
func (b *nativeBuffer) resize(n int) ([]byte, error) {
	if b.frozen || b.ptr == nil {
		return nil, fmt.Errorf("native buffer is not writable")
	}
	if n < 0 || n > MaxFrame*8 {
		return nil, fmt.Errorf("native buffer exceeds frame capacity")
	}
	if n <= cap(b.bytes) {
		b.bytes = b.bytes[:n]
		return b.bytes, nil
	}
	capacity := max(n, min(max(32, cap(b.bytes)*2), MaxFrame*8))
	p := C.trunk_buffer_resize(b.ptr, C.size_t(capacity))
	if p == nil {
		return nil, fmt.Errorf("%w: native buffer budget exhausted", ErrStagingFull)
	}
	b.bytes = unsafe.Slice((*byte)(unsafe.Pointer(p)), capacity)[:n]
	return b.bytes, nil
}
func (b *nativeBuffer) close() {
	if b != nil && b.ptr != nil {
		C.trunk_buffer_free(b.ptr)
		b.ptr = nil
		b.bytes = nil
	}
}
func (b *nativeBuffer) matches(raw []byte) bool {
	if b == nil || len(raw) == 0 || len(b.bytes) == 0 {
		return false
	}
	start := uintptr(unsafe.Pointer(&b.bytes[0]))
	at := uintptr(unsafe.Pointer(&raw[0]))
	return at >= start && at-start <= uintptr(len(b.bytes)) && uintptr(len(raw)) <= uintptr(len(b.bytes))-(at-start)
}

type nativeArena struct {
	ptr     unsafe.Pointer
	adopted map[unsafe.Pointer]struct{}
}

func (a *nativeArena) close() {
	if a != nil && a.ptr != nil {
		C.trunk_arena_free(a.ptr)
		a.ptr = nil
	}
}
func (b *branchStore) own(s *Store, id [16]byte, raw []byte, input *nativeBuffer) ([]byte, error) {
	hour := eventTime(id[:]) / TimeHourMS
	if b.memory == nil {
		b.memory = map[int64]*nativeArena{}
	}
	a := b.memory[hour]
	if a == nil {
		a = &nativeArena{ptr: C.trunk_arena_new(), adopted: map[unsafe.Pointer]struct{}{}}
		b.memory[hour] = a
	}
	if !input.matches(raw) {
		stream := nativeIngress{memory: s.memory}
		defer stream.close()
		frame, e := stream.allocate(len(raw))
		if e != nil {
			return nil, e
		}
		copy(frame.raw, raw)
		input, raw = frame.buffer, frame.raw
	}
	key := unsafe.Pointer(&input.bytes[0])
	if _, ok := a.adopted[key]; ok {
		return raw, nil
	}
	if !bool(C.trunk_arena_adopt(a.ptr, input.ptr)) {
		return nil, fmt.Errorf("%w: native arena allocation failed", ErrStagingFull)
	}
	a.adopted[key] = struct{}{}
	input.frozen = true
	return raw, nil
}

// Caller holds the store mutex. A snapshot pins all current hour arenas once,
// not once per record. No finalizer participates in buffer lifetime correctness.
func (b *branchStore) pinMemory() func() {
	pins := make([]unsafe.Pointer, 0, len(b.memory))
	for _, a := range b.memory {
		pins = append(pins, C.trunk_arena_pin(a.ptr))
	}
	return func() {
		for _, p := range pins {
			C.trunk_arena_free(p)
		}
		pins = nil
	}
}
func (b *branchStore) pruneMemory(now time.Time) {
	cutoff := now.Add(-HotWindow).UnixMilli() / TimeHourMS
	cutoff = min(cutoff, b.live.firstPending()/TimeHourMS)
	for key, v := range b.liveViews {
		if !time.Now().Before(v.expires) {
			delete(b.liveViews, key)
			continue
		}
		if len(v.events) > 0 {
			cutoff = min(cutoff, eventTime(v.events[0].id[:])/TimeHourMS)
		}
	}
	for hour, a := range b.memory {
		if hour < cutoff {
			a.close()
			delete(b.memory, hour)
		}
	}
}

func (b *nativeBuffer) readAt(f *os.File, start, end uint64) ([]byte, error) {
	if end < start || end-start > MaxFrame*8 {
		return nil, fmt.Errorf("invalid byte range")
	}
	raw, e := b.resize(int(end - start))
	if e != nil {
		return nil, e
	}
	_, e = f.ReadAt(raw, int64(start))
	return raw, e
}
