package cache

import (
	"fmt"
	"unicode/utf8"
)

// Specialization of the existing optimized codec, not a wire-format change.
// A 16-bit width needs no leading-zero calculation or selector bit packing.
func compact16Code(b []byte, signed bool) byte {
	if len(b) == 1 {
		return 0
	}
	u := order.Uint16(b)
	if signed {
		u += 128
	} // wrapping bias maps [-128,127] into [0,255].
	return byte((uint32(u) + 0xff00) >> 16) // 0 for one byte, 1 for two bytes.
}
func (a *fastIntegerLayout) encode16(dst []byte, values []Value, compact bool) ([]byte, error) {
	// The original fixed encoder is already a single-pass append fast path when capacity is reused.
	if !compact && cap(dst) > 0 {
		return a.reference.base.Encode(dst, values)
	}
	if len(values) != len(a.fields) {
		return nil, fmt.Errorf("field count mismatch")
	}
	var mask [64]byte
	var controls [64]byte
	var widths [256]byte
	controlBits, directBytes, offsetBytes := 0, 0, 0
	var end uint64
	for i, v := range values {
		state := v.State
		f := a.fields[i]
		switch state {
		case Absent, Null:
		case Direct:
			w := f.width
			if compact && f.eligible {
				n := len(v.Bytes)
				if n == 0 || n > f.width || n&(n-1) != 0 {
					return nil, fmt.Errorf("invalid integer width")
				}
				if f.width == 2 {
					code := compact16Code(v.Bytes, f.signed)
					w = 1 + int(code)
					state = State(3 - code)
				} else {
					code := compactFastCode(v.Bytes, f.signed)
					w = 1 << code
					if w < f.width {
						state = Offset
						putCompactCode(controls[:], controlBits, f.selectorBits, code)
						controlBits += f.selectorBits
					}
				}
			} else if w == 0 || len(v.Bytes) != w {
				return nil, fmt.Errorf("direct width mismatch")
			}
			widths[i] = byte(w)
			directBytes += w
		case Offset:
			if !f.offsetAllowed {
				return nil, fmt.Errorf("logical integer cannot be OFFSET")
			}
			if len(v.Bytes) > MaxFrame-int(end) {
				return nil, fmt.Errorf("frame too large")
			}
			end += uint64(len(v.Bytes))
			offsetBytes += extendedWidth(end)
		default:
			return nil, fmt.Errorf("invalid state")
		}
		mask[i/4] |= byte(state) << uint(6-2*(i%4))
	}
	controlBytes := (controlBits + 7) / 8
	size := a.mask + controlBytes + directBytes + offsetBytes + int(end)
	if size > MaxFrame {
		return nil, fmt.Errorf("frame too large")
	}
	// One scratch byte permits an unconditional native I16/U16 write at
	// the end. It is spare capacity, never part of the encoded frame.
	if cap(dst) < size+1 {
		dst = make([]byte, size+1)
	} else {
		dst = dst[:size+1]
	}
	copy(dst, mask[:a.mask])
	copy(dst[a.mask:], controls[:controlBytes])
	at := a.mask + controlBytes
	for i, v := range values {
		if v.State == Direct {
			w := int(widths[i])
			f := a.fields[i]
			if compact && f.eligible && f.width == 2 {
				copy(dst[at:at+len(v.Bytes)], v.Bytes)
			} else {
				copy(dst[at:at+w], v.Bytes[:w])
			}
			at += w
		}
	}
	dst = dst[:at]
	end = 0
	for _, v := range values {
		if v.State == Offset {
			end += uint64(len(v.Bytes))
			dst = extended(dst, end)
		}
	}
	for _, v := range values {
		if v.State == Offset {
			dst = append(dst, v.Bytes...)
		}
	}
	return dst, nil
}

func (a *fastIntegerLayout) scan16(raw []byte, reuse *Frame) error {
	return a.scanMode16(raw, reuse, true)
}
func (a *fastIntegerLayout) scanMode16(raw []byte, reuse *Frame, compact bool) error {
	if len(raw) < a.mask || len(raw) > MaxFrame {
		return fmt.Errorf("invalid frame size")
	}
	if int(raw[a.mask-1])&a.tailMask != 0 {
		return fmt.Errorf("nonzero mask padding")
	}
	controlBits := 0
	if compact {
		for i := range a.controls {
			controlBits += int(a.controls[i][raw[i]])
		}
	}
	controlEnd := a.mask + (controlBits+7)/8
	if controlEnd > len(raw) {
		return fmt.Errorf("short width plane")
	}
	controls := raw[a.mask:controlEnd]
	if controlBits%8 != 0 && controls[len(controls)-1]&byte((1<<(8-controlBits%8))-1) != 0 {
		return fmt.Errorf("nonzero width padding")
	}
	n := len(a.fields)
	if cap(reuse.Values) < n {
		reuse.Values = make([]Value, n)
	} else {
		reuse.Values = reuse.Values[:n]
	}
	if cap(reuse.ends) < n {
		reuse.ends = make([][2]int, n)
	} else {
		reuse.ends = reuse.ends[:n]
	}
	at, bit := controlEnd, 0
	for i, f := range a.fields {
		state := State(raw[i/4] >> uint(6-2*(i%4)) & 3)
		v := Value{State: state}
		// ABSENT/NULL do not need a width/type decision, especially on PATCHes.
		if state < Direct {
			reuse.Values[i] = v
			continue
		}
		if f.width == 2 && f.eligible {
			if !compact && state == Offset {
				return fmt.Errorf("offset fixed-width field")
			}
			w := 2 - int(state&1)
			if w > len(raw)-at {
				return fmt.Errorf("invalid 16-bit width")
			}
			v = Value{State: Direct, Bytes: raw[at : at+w]}
			at += w
		} else {
			switch state {
			case Absent, Null:
			case Offset:
				if !f.eligible {
					if !f.offsetAllowed {
						return fmt.Errorf("offset fixed-width field")
					}
					break
				}
				if !compact {
					return fmt.Errorf("offset fixed-width field")
				}
				w := 1 << getCompactCode(controls, bit, f.selectorBits)
				bit += f.selectorBits
				if w >= f.width || w > len(raw)-at {
					return fmt.Errorf("invalid compact width")
				}
				v = Value{State: Direct, Bytes: raw[at : at+w]}
				at += w
			case Direct:
				w := f.width
				if w == 0 || w > len(raw)-at {
					return fmt.Errorf("invalid direct width")
				}
				v.Bytes = raw[at : at+w]
				at += w
				if f.boolean && v.Bytes[0] > 1 {
					return fmt.Errorf("invalid bool")
				}
				if f.dictionary > 0 && unsigned(v.Bytes) >= uint64(f.dictionary) {
					return fmt.Errorf("invalid dictionary reference")
				}
			}
		}
		reuse.Values[i] = v
	}
	previous := 0
	for i, v := range reuse.Values {
		if v.State == Offset {
			end, err := readExtended(raw, &at)
			if err != nil {
				return err
			}
			if end < uint64(previous) || end > MaxFrame {
				return fmt.Errorf("invalid end offset")
			}
			reuse.ends[i] = [2]int{previous, int(end)}
			previous = int(end)
		}
	}
	if previous != len(raw)-at {
		return fmt.Errorf("frame boundary mismatch")
	}
	for i, v := range reuse.Values {
		if v.State == Offset {
			v.Bytes = raw[at+reuse.ends[i][0] : at+reuse.ends[i][1]]
			if a.fields[i].text && !utf8.Valid(v.Bytes) {
				return fmt.Errorf("invalid UTF-8")
			}
			reuse.Values[i] = v
		}
	}
	reuse.Bytes = raw
	return nil
}

func (s *streamIntegerLayout) encode16(dst []byte, values []Value) ([]byte, error) {
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
				if f.width == 2 {
					code := compact16Code(v.Bytes, f.signed)
					w = 1 + int(code)
					state = State(3 - code)
				} else {
					code := compactFastCode(v.Bytes, f.signed)
					w = 1 << code
					if w < f.width {
						state = Offset
						putCompactCode(dst[a.mask:prefix], bit, f.selectorBits, code)
						bit += f.selectorBits
					}
				}
			} else if w == 0 || len(v.Bytes) != w {
				return nil, fmt.Errorf("direct width mismatch")
			}
			// Copy the native two-byte input, then retain only the chosen width.
			// This avoids a value-dependent 1-versus-2-byte copy path. Borrowed
			// one-byte inputs remain valid; discarded bytes never enter the frame.
			if f.eligible && f.width == 2 {
				dst = append(dst, v.Bytes...)
				dst = dst[:len(dst)-len(v.Bytes)+w]
			} else {
				dst = append(dst, v.Bytes[:w]...)
			}
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
