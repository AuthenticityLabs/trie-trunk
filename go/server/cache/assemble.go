package cache

import (
	"bytes"
	"fmt"
	"sort"
)

type restoreSlot struct {
	field int
	raw   []byte
}
type frameAssembler struct {
	layout      *Layout
	trie        *sharedTrie
	plans       map[uint16][]restoreSlot
	cachedSlots int
	identity    Frame
}

func (a *frameAssembler) plan(ref uint16) ([]restoreSlot, error) {
	if int(ref) >= len(a.trie.nodes) {
		return nil, fmt.Errorf("unknown shared reference")
	}
	if ref == 0 {
		return nil, nil
	}
	if plan, ok := a.plans[ref]; ok {
		return plan, nil
	}
	var plan []restoreSlot
	for p := ref; p != 0; p = a.trie.nodes[p].Parent {
		n := a.trie.nodes[p]
		plan = append(plan, restoreSlot{int(n.Field), n.Bytes})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].field < plan[j].field })
	// Bound query-local caching even for adversarially large, varied tries.
	if a.cachedSlots+len(plan) <= 4096 {
		if a.plans == nil {
			a.plans = map[uint16][]restoreSlot{}
		}
		a.plans[ref] = plan
		a.cachedSlots += len(plan)
	}
	return plan, nil
}

// Assemble copies encoded primitive spans and the entire existing offset/value
// section unchanged. Only the mask, indexed UUID and referenced direct fields
// are added. No per-event Value array, scalar decoding or Encode call occurs.
// Input is an ingest-validated population protected by committed CRCs. This is
// standalone output; native Collect sends index/population bytes unchanged.
func (a *frameAssembler) assemble(row, pop, dst []byte) ([]byte, error) {
	l := a.layout
	if l.IsCompressed(pop) {
		f := &a.identity
		// Committed bytes were validated at ingress. Check the clear identity only.
		head, n, _, stored, err := l.compressionLengths(pop)
		if err != nil {
			return nil, err
		}
		if len(pop) != head+n+stored || len(row) != 16 {
			return nil, fmt.Errorf("invalid compressed population")
		}
		if err = l.scanPlain(pop[head:head+n], f); err != nil {
			return nil, err
		}
		if !bytes.Equal(f.Values[l.Event].Bytes, row[:16]) {
			return nil, fmt.Errorf("compressed identity differs from index")
		}
		return append(dst[:0], pop...), nil
	}
	if l.Shared >= 0 {
		f := &a.identity
		if err := l.Scan(pop, f); err != nil {
			return nil, err
		}
		if err := expandShared(l, a.trie, f.Values); err != nil {
			return nil, err
		}
		if f.Values[l.Event].State != Absent {
			return nil, fmt.Errorf("population shadows event_id")
		}
		f.Values[l.Event] = Value{Direct, row[:16]}
		return l.Encode(dst, f.Values)
	}
	if l.integers != nil {
		return a.assembleIntegers(row, pop, dst)
	}
	mask := (len(l.Widths) + 3) / 4
	if len(row) != 16 || len(pop) < mask {
		return nil, fmt.Errorf("short stored frame")
	}
	plan, err := a.plan(0)
	if err != nil {
		return nil, err
	}
	dst = append(dst[:0], pop[:mask]...)
	at, next := mask, 0
	for i, w := range l.Widths {
		state := State(pop[i/4] >> uint(6-2*(i%4)) & 3)
		var restore []byte
		if i == l.Event {
			restore = row[:16]
		}
		if next < len(plan) && plan[next].field == i {
			restore = plan[next].raw
			next++
		}
		if restore != nil {
			if state != Absent {
				return nil, fmt.Errorf("population shadows envelope or shared field")
			}
			dst[i/4] |= byte(Direct) << uint(6-2*(i%4))
			dst = append(dst, restore...)
		} else if state == Direct {
			if w == 0 || w > len(pop)-at {
				return nil, fmt.Errorf("invalid stored direct field")
			}
			dst = append(dst, pop[at:at+w]...)
			at += w
		}
	}
	dst = append(dst, pop[at:]...)
	if len(dst) > MaxFrame {
		return nil, fmt.Errorf("assembled frame too large")
	}
	return dst, nil
}

func (s *Store) assemble(row, pop, dst []byte) ([]byte, error) {
	a := frameAssembler{layout: s.layout, trie: &s.trie}
	return a.assemble(row, pop, dst)
}

// Only the masks change as the indexed UUID/shared fields are restored. Stored
// scalar spans and the offset/value tail are copied without scalar decoding.
func (a *frameAssembler) assembleIntegers(row, pop, dst []byte) ([]byte, error) {
	l := a.layout
	ints := l.integers
	mask := ints.mask
	if len(row) != 16 {
		return nil, fmt.Errorf("short row")
	}
	plane, at, err := ints.plane(pop)
	if err != nil {
		return nil, err
	}
	plan, err := a.plan(0)
	if err != nil {
		return nil, err
	}
	dst = append(dst[:0], pop[:mask]...)
	dst[l.Event/4] |= byte(Direct) << uint(6-2*(l.Event%4))
	for _, slot := range plan {
		dst[slot.field/4] |= byte(Direct) << uint(6-2*(slot.field%4))
	}
	bits := 0
	for i := range ints.controls {
		bits += int(ints.controls[i][dst[i]])
	}
	end := mask + (bits+7)/8
	dst = append(dst, make([]byte, end-mask)...)
	sourceBit, targetBit, next := 0, 0, 0
	for i, f := range ints.fields {
		state := State(pop[i/4] >> uint(6-2*(i%4)) & 3)
		var raw []byte
		if i == l.Event {
			raw = row[:16]
		}
		if next < len(plan) && plan[next].field == i {
			raw = plan[next].raw
			next++
		}
		if raw != nil {
			if state != Absent {
				return nil, fmt.Errorf("population shadows shared field")
			}
		} else if state == Direct {
			w, e := ints.fieldWidth(i, plane, &sourceBit)
			if e != nil {
				return nil, e
			}
			if w == 0 || w > len(pop)-at {
				return nil, fmt.Errorf("invalid direct span")
			}
			raw = pop[at : at+w]
			at += w
		}
		if raw == nil {
			continue
		}
		if f.eligible {
			putWidthCode(dst[mask:end], targetBit, f.selectorBits, byte(bitsForWidth(len(raw))))
			targetBit += f.selectorBits
		}
		dst = append(dst, raw...)
	}
	dst = append(dst, pop[at:]...)
	if len(dst) > MaxFrame {
		return nil, fmt.Errorf("assembled frame too large")
	}
	return dst, nil
}
func bitsForWidth(w int) int {
	switch w {
	case 1:
		return 0
	case 2:
		return 1
	case 4:
		return 2
	default:
		return 3
	}
}
