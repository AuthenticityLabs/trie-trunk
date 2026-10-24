package cache

import "fmt"

// Reserve the maximum small selector plane, select widths while appending
// direct values, then close any unused selector gap with one overlapping copy.
// This removes the width scratch array and separate direct-value emission pass.
// Wire bytes remain identical. The tradeoff is at most one move of the direct
// section; it cannot shift variable strings/payloads because they follow later.
type streamIntegerLayout struct {
	plan     *fastIntegerLayout
	reserved int
}

func compileStreamIntegerExperiment(a *fastIntegerLayout) *streamIntegerLayout {
	bits := 0
	for _, f := range a.fields {
		if f.eligible {
			bits += f.selectorBits
		}
	}
	return &streamIntegerLayout{a, (bits + 7) / 8}
}
func (s *streamIntegerLayout) encode(dst []byte, values []Value) ([]byte, error) {
	a := s.plan
	if len(values) != len(a.fields) {
		return nil, fmt.Errorf("field count mismatch")
	}
	prefix := a.mask + s.reserved
	dst = append(dst[:0], make([]byte, prefix)...)
	bit := 0
	for i, v := range values {
		f := a.fields[i]
		state := v.State
		switch state {
		case Absent, Null:
		case Direct:
			w := f.width
			if f.eligible {
				n := len(v.Bytes)
				if n == 0 || n > f.width || n&(n-1) != 0 {
					return nil, fmt.Errorf("invalid integer width")
				}
				code := compactFastCode(v.Bytes, f.signed)
				w = 1 << code
				if w < f.width {
					state = Offset
					putCompactCode(dst[a.mask:prefix], bit, f.selectorBits, code)
					bit += f.selectorBits
				}
			} else if w == 0 || len(v.Bytes) != w {
				return nil, fmt.Errorf("direct width mismatch")
			}
			dst = append(dst, v.Bytes[:w]...)
		case Offset:
			if !f.offsetAllowed {
				return nil, fmt.Errorf("logical integer cannot be OFFSET")
			}
		default:
			return nil, fmt.Errorf("invalid state")
		}
		dst[i/4] |= byte(state) << uint(6-2*(i%4))
	}
	used := (bit + 7) / 8
	if gap := s.reserved - used; gap > 0 {
		copy(dst[a.mask+used:], dst[prefix:])
		dst = dst[:len(dst)-gap]
	}
	var end uint64
	for _, v := range values {
		if v.State == Offset {
			if len(v.Bytes) > MaxFrame-int(end) {
				return nil, fmt.Errorf("frame too large")
			}
			end += uint64(len(v.Bytes))
			dst = extended(dst, end)
		}
	}
	if len(dst)+int(end) > MaxFrame {
		return nil, fmt.Errorf("frame too large")
	}
	for _, v := range values {
		if v.State == Offset {
			dst = append(dst, v.Bytes...)
		}
	}
	return dst, nil
}
