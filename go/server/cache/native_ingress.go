package cache

import (
	"fmt"
	"io"
	"math/rand/v2"
	"sync/atomic"
)

const ingressChunkBytes = 16 << 10
const ingressIdleChunks = 16

// Separate cache lines avoid bouncing every idle slot between busy readers.
type ingressSlot struct {
	value atomic.Pointer[ingressChunk]
	_     [56]byte
}

// Published prefixes never move or change. An exclusive ingress lease writes
// only the unused suffix; hour arenas independently pin the backing allocation.
type ingressChunk struct {
	available atomic.Int64
	slot      int
	buffer    *nativeBuffer
	used      int
	_         [32]byte // prevent two independently leased cursors sharing a cache line
}
type nativeInput struct {
	raw    []byte
	buffer *nativeBuffer
}
type nativeIngress struct {
	memory  *nativeMemory
	current *ingressChunk
	retired []*ingressChunk
	partial int
}

func (m *nativeMemory) takeChunk(minimum int) (*ingressChunk, error) {
	if m.closed.Load() {
		return nil, fmt.Errorf("native memory is closed")
	}
	start := int(rand.Uint32() % ingressIdleChunks)
	for j := 0; j < ingressIdleChunks; j++ {
		slot := &m.idle[(start+j)%ingressIdleChunks].value
		c := slot.Load()
		if c != nil && c.available.Load() >= int64(minimum) && slot.CompareAndSwap(c, nil) {
			// The same chunk can leave and re-enter a slot between Load and CAS.
			// Recheck under exclusive ownership before trusting its remaining capacity.
			if len(c.buffer.bytes)-c.used >= minimum {
				return c, nil
			}
			m.putChunk(c)
		}
	}
	// Only allocation and shutdown need the handle mutex. The ordinary leased
	// suffix path uses independent atomic slots and never crosses the C ABI.
	m.ingressMu.Lock()
	defer m.ingressMu.Unlock()
	if m.ptr == nil {
		return nil, fmt.Errorf("native memory is closed")
	}
	used := m.stats().Used
	available := uint64(0)
	if used < m.limit {
		available = m.limit - used
	}
	n := max(minimum, int(min(available, uint64(ingressChunkBytes))))
	buffer := m.buffer()
	if _, err := buffer.resize(n); err != nil {
		buffer.close()
		return nil, err
	}
	return &ingressChunk{buffer: buffer, slot: start}, nil
}
func (m *nativeMemory) putChunk(c *ingressChunk) {
	if c == nil {
		return
	}
	if !m.closed.Load() && len(c.buffer.bytes) <= ingressChunkBytes && len(c.buffer.bytes)-c.used >= 32 {
		c.available.Store(int64(len(c.buffer.bytes) - c.used))
		for j := 0; j < ingressIdleChunks; j++ {
			slot := &m.idle[(c.slot+j)%ingressIdleChunks].value
			if slot.CompareAndSwap(nil, c) {
				// A close that has already swept this slot must not leave a late return
				// stranded. Exactly one of this CAS, close, or another borrower owns it.
				if m.closed.Load() && slot.CompareAndSwap(c, nil) {
					c.buffer.close()
				}
				return
			}
		}
	}
	c.buffer.close()
}
func (r *nativeIngress) resize(n int) ([]byte, error) {
	if n < 0 || n > MaxFrame {
		return nil, fmt.Errorf("frame too large")
	}
	if r.current == nil || len(r.current.buffer.bytes)-r.current.used < n {
		next, err := r.memory.takeChunk(n)
		if err != nil {
			return nil, err
		}
		if old := r.current; old != nil {
			copy(next.buffer.bytes[next.used:], old.buffer.bytes[old.used:old.used+r.partial])
			// Existing batch members still borrow this allocation until stage completes.
			if old.used == 0 {
				r.memory.putChunk(old)
			} else {
				r.retired = append(r.retired, old)
			}
		}
		r.current = next
	}
	r.partial = n
	c := r.current
	return c.buffer.bytes[c.used : c.used+n : c.used+n], nil
}

// allocate is the known-length path for journal recovery and Go Append callers.
func (r *nativeIngress) allocate(n int) (nativeInput, error) {
	r.partial = 0
	raw, err := r.resize(n)
	if err != nil {
		return nativeInput{}, err
	}
	r.current.used += n
	r.partial = 0
	return nativeInput{raw: raw, buffer: r.current.buffer}, nil
}
func (r *nativeIngress) read(l *Layout, input io.Reader) (nativeInput, error) {
	r.partial = 0
	raw, err := l.ReadFrameBuffer(input, nil, true, r.resize)
	if err != nil {
		return nativeInput{}, err
	}
	r.current.used += len(raw)
	r.partial = 0
	return nativeInput{raw: raw, buffer: r.current.buffer}, nil
}

// Only after the entire batch has been accepted/rejected may its temporary
// backing handles be released. Accepted records are held by their hour arenas.
func (r *nativeIngress) staged() {
	for _, c := range r.retired {
		c.buffer.close()
	}
	clear(r.retired)
	r.retired = r.retired[:0]
}
func (r *nativeIngress) close() {
	r.staged()
	r.memory.putChunk(r.current)
	r.current = nil
}
func (s *Store) ingressStream() (*nativeIngress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	return &nativeIngress{memory: s.memory}, nil
}
