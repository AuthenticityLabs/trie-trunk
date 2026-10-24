package cache

import (
	"fmt"
	"math/bits"
	"unicode/utf8"
)

// Compiled once per definition. The presence lookup and specialized width
// selection are promoted from the corrected separate-width-mask benchmark.
type integerField struct {
	dictionary                      int
	boolean, text, forbidNull       bool
	width, selectorBits             int
	eligible, signed, offsetAllowed bool
}
type integerLayout struct {
	layout         *Layout
	tailMask       int
	fields         []integerField
	controls       [][256]byte
	mask, reserved int
}

func compileIntegers(l *Layout) (*integerLayout, error) {
	mode := l.Definition.AdaptiveIntegers
	if mode == "" || mode == "none" {
		return nil, nil
	}
	if mode != "16" && mode != "32" {
		return nil, fmt.Errorf("adaptive_integers must be none, 16 or 32")
	}
	a := &integerLayout{layout: l, fields: make([]integerField, len(l.Widths)), mask: (len(l.Widths) + 3) / 4}
	if len(l.Widths)%4 != 0 {
		a.tailMask = (1 << ((4 - len(l.Widths)%4) * 2)) - 1
	}
	total := 0
	for i, d := range l.Definition.Fields {
		f := integerField{dictionary: len(d.Values), boolean: d.Type == "BOOL", text: d.Type == "STRING", forbidNull: d.Name == "@fields", width: l.Widths[i], offsetAllowed: l.Widths[i] == 0 || len(d.Values) > 0}
		switch d.Type {
		case "I16", "I32", "I64":
			f.signed = true
		}
		switch d.Type {
		case "I32", "U32", "I64", "U64":
			f.eligible = true
		case "I16", "U16":
			f.eligible = mode == "16"
		}
		if f.eligible {
			f.selectorBits = 2
			if f.width == 2 {
				f.selectorBits = 1
			}
			total += f.selectorBits
		}
		a.fields[i] = f
	}
	a.reserved = (total + 7) / 8
	a.controls = make([][256]byte, a.mask)
	for i := range a.controls {
		for m := 0; m < 256; m++ {
			n := 0
			for j := 0; j < 4 && i*4+j < len(a.fields); j++ {
				if State(m>>uint(6-2*j)&3) == Direct {
					n += a.fields[i*4+j].selectorBits
				}
			}
			a.controls[i][m] = byte(n)
		}
	}
	return a, nil
}
func (a *integerLayout) plane(raw []byte) ([]byte, int, error) {
	if len(raw) < a.mask {
		return nil, 0, fmt.Errorf("short mask")
	}
	n := 0
	for i := range a.controls {
		n += int(a.controls[i][raw[i]])
	}
	end := a.mask + (n+7)/8
	if end > len(raw) {
		return nil, 0, fmt.Errorf("short width plane")
	}
	b := raw[a.mask:end]
	if n%8 != 0 && b[len(b)-1]&byte((1<<(8-n%8))-1) != 0 {
		return nil, 0, fmt.Errorf("nonzero width padding")
	}
	return b, end, nil
}
func (a *integerLayout) fieldWidth(i int, plane []byte, bit *int) (int, error) {
	f := a.fields[i]
	if !f.eligible {
		return f.width, nil
	}
	code := getWidthCode(plane, *bit, f.selectorBits)
	*bit += f.selectorBits
	w := 1 << code
	if w > f.width {
		return 0, fmt.Errorf("width exceeds declared type")
	}
	return w, nil
}
func (a *integerLayout) code(i int, b []byte) byte {
	f := a.fields[i]
	if f.width == 2 {
		return integer16Code(b, f.signed)
	}
	return integerCode(b, f.signed)
}
func (l *Layout) shortValue(i int, v Value) Value {
	if l.integers != nil && v.State == Direct && l.integers.fields[i].eligible && len(v.Bytes) > 0 && len(v.Bytes) <= l.Widths[i] && len(v.Bytes)&(len(v.Bytes)-1) == 0 {
		v.Bytes = v.Bytes[:1<<l.integers.code(i, v.Bytes)]
	}
	return v
}
func integerCode(b []byte, signed bool) byte {
	var u uint64
	switch len(b) {
	case 1:
		u = uint64(b[0])
	case 2:
		u = uint64(order.Uint16(b))
	case 4:
		u = uint64(order.Uint32(b))
	case 8:
		u = order.Uint64(b)
	}
	if signed {
		shift := uint(64 - len(b)*8)
		s := int64(u<<shift) >> shift
		u = uint64(s^(s>>63)) << 1
	}
	return [9]byte{0, 0, 1, 2, 2, 3, 3, 3, 3}[(bits.Len64(u)+7)>>3]
}
func integer16Code(b []byte, signed bool) byte {
	if len(b) == 1 {
		return 0
	}
	u := order.Uint16(b)
	if signed {
		u += 128
	} // wrapping bias maps [-128,127] into [0,255].
	return byte((uint32(u) + 0xff00) >> 16) // 0 for one byte, 1 for two bytes.
}
func putWidthCode(b []byte, bit, bits int, code byte) {
	if bits == 0 {
		return
	}
	i, offset := bit/8, bit%8
	if offset+bits <= 8 {
		b[i] |= code << uint(8-offset-bits)
		return
	}
	b[i] |= code >> 1
	b[i+1] |= (code & 1) << 7
}
func getWidthCode(b []byte, bit, bits int) byte {
	if bits == 0 {
		return 0
	}
	i, offset := bit/8, bit%8
	if offset+bits <= 8 {
		return (b[i] >> uint(8-offset-bits)) & byte((1<<bits)-1)
	}
	return (b[i]&1)<<1 | b[i+1]>>7
}

func (a *integerLayout) encode(dst []byte, values []Value) ([]byte, error) {
	if len(values) != len(a.fields) {
		return nil, fmt.Errorf("field count mismatch")
	}
	prefix := a.mask + a.reserved
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
					code := integer16Code(v.Bytes, f.signed)
					w = 1 + int(code)
					dst[a.mask+bit/8] |= code << uint(7-bit%8)
					bit++
				} else {
					code := integerCode(v.Bytes, f.signed)
					w = 1 << code
					putWidthCode(dst[a.mask:prefix], bit, f.selectorBits, code)
					bit += f.selectorBits
				}
			} else if w == 0 || len(v.Bytes) != w {
				return nil, fmt.Errorf("direct width mismatch")
			}
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
	if gap := a.reserved - used; gap > 0 {
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

func (l *Layout) validDirectWidth(i int, b []byte) bool {
	w := len(b)
	if l.integers != nil && l.integers.fields[i].eligible {
		return w > 0 && w <= l.Widths[i] && w&(w-1) == 0 && 1<<l.integers.code(i, b) == w
	}
	return w == l.Widths[i]
}

// Identity postings/state keys span definition minors. Their representation must
// not change when a later definition enables shortening. Only identity paths
// normalize; ordinary scans/assembly continue to borrow encoded scalar spans.
func (l *Layout) identityValue(i int, v Value) Value {
	if i < 0 || l.integers == nil || !l.integers.fields[i].eligible || v.State != Direct || len(v.Bytes) == 0 || len(v.Bytes) >= l.Widths[i] {
		return v
	}
	out := make([]byte, l.Widths[i])
	if l.integers.fields[i].signed && v.Bytes[len(v.Bytes)-1]&128 != 0 {
		for j := range out {
			out[j] = 255
		}
	}
	copy(out, v.Bytes)
	v.Bytes = out
	return v
}

func (a *integerLayout) scan(raw []byte, reuse *Frame) error {
	if len(raw) < a.mask || len(raw) > MaxFrame {
		return fmt.Errorf("invalid frame size")
	}
	if int(raw[a.mask-1])&a.tailMask != 0 {
		return fmt.Errorf("nonzero mask padding")
	}
	controlBits := 0
	{
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
		if state < Direct {
			if state == Null && f.forbidNull {
				return fmt.Errorf("@fields cannot be null")
			}
			reuse.Values[i] = v
			continue
		}
		switch state {
		case Absent, Null:
		case Offset:
			if !f.offsetAllowed {
				return fmt.Errorf("offset fixed-width field")
			}
		case Direct:
			w := f.width
			if f.eligible {
				if f.width == 2 {
					w = 1 + int(controls[bit/8]>>uint(7-bit%8)&1)
					bit++
				} else {
					w = 1 << getWidthCode(controls, bit, f.selectorBits)
					bit += f.selectorBits
					if w > f.width {
						return fmt.Errorf("width exceeds declared type")
					}
				}
			}
			if w == 0 || w > len(raw)-at {
				return fmt.Errorf("invalid direct width")
			}
			v.Bytes = raw[at : at+w]
			at += w
			if f.eligible && w > 1 && 1<<a.code(i, v.Bytes) != w {
				return fmt.Errorf("noncanonical integer width")
			}
			if f.boolean && v.Bytes[0] > 1 {
				return fmt.Errorf("invalid bool")
			}
			if f.dictionary > 0 && unsigned(v.Bytes) >= uint64(f.dictionary) {
				return fmt.Errorf("invalid dictionary reference")
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
			if !a.fields[i].text {
				if err := a.layout.validateOffset(i, v.Bytes); err != nil {
					return err
				}
			}
			reuse.Values[i] = v
		}
	}
	reuse.Bytes = raw
	return nil
}
