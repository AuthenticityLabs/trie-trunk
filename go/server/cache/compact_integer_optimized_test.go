package cache

// An implementation experiment with exactly the first compact prototype's wire
// format. The reference encoder/scanner are retained for paired measurements.
import (
	"fmt"
	"math/bits"
	"unicode/utf8"
)

type fastIntegerField struct {
	width, selectorBits, dictionary                int
	eligible, signed, boolean, text, offsetAllowed bool
}
type fastIntegerLayout struct {
	reference      *compactIntegerLayout
	fields         []fastIntegerField
	controls       [][256]byte
	mask, tailMask int
}

func compileFastIntegerExperiment(a *compactIntegerLayout) *fastIntegerLayout {
	f := &fastIntegerLayout{reference: a, fields: make([]fastIntegerField, len(a.fields)), controls: make([][256]byte, a.mask), mask: a.mask}
	if len(a.fields)%4 != 0 {
		f.tailMask = (1 << ((4 - len(a.fields)%4) * 2)) - 1
	}
	for i, v := range a.fields {
		d := a.base.Definition.Fields[i]
		f.fields[i] = fastIntegerField{v.width, v.bits, len(d.Values), v.eligible, v.signed, d.Type == "BOOL", d.Type == "STRING", v.width == 0 || len(d.Values) > 0}
	}
	for i := range f.controls {
		for m := 0; m < 256; m++ {
			b := byte(m) & (byte(m) >> 1)
			f.controls[i][m] = byte(bits.OnesCount8(b&a.mask32[i]) + 2*bits.OnesCount8(b&a.mask64[i]))
		}
	}
	return f
}

// Typed loads avoid the original generic unsigned() zero-fill/copy. Normalizing
// signed magnitude allows the same leading-zero calculation for both kinds.
func compactFastCode(b []byte, signed bool) byte {
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
func extendedWidth(n uint64) int {
	if n < 65535 {
		return 2
	}
	if n < 0xffffffff {
		return 6
	}
	return 14
}

// compact=false is the matched optimized fixed-width control. Both versions
// benefit from one exact output allocation and the same layout preparation.
func (a *fastIntegerLayout) encode(dst []byte, values []Value, compact bool) ([]byte, error) {
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
				code := compactFastCode(v.Bytes, f.signed)
				w = 1 << code
				if w < f.width {
					state = Offset
					putCompactCode(controls[:], controlBits, f.selectorBits, code)
					controlBits += f.selectorBits
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
	if cap(dst) < size {
		dst = make([]byte, size)
	} else {
		dst = dst[:size]
	}
	copy(dst, mask[:a.mask])
	copy(dst[a.mask:], controls[:controlBytes])
	dst = dst[:a.mask+controlBytes]
	for i, v := range values {
		if v.State == Direct {
			dst = append(dst, v.Bytes[:widths[i]]...)
		}
	}
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

func (a *fastIntegerLayout) scan(raw []byte, reuse *Frame) error { return a.scanMode(raw, reuse, true) }
func (a *fastIntegerLayout) scanMode(raw []byte, reuse *Frame, compact bool) error {
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

// Ablation control: change only scalar loads/width selection, retaining the original
// encoder passes, validation, and allocation strategy.
func (a *compactIntegerLayout) encodeFastLoads(dst []byte, values []Value) ([]byte, error) {
	if len(values) != len(a.fields) {
		return nil, fmt.Errorf("field count mismatch")
	}
	if !a.eligible {
		return a.base.Encode(dst, values)
	}
	dst = append(dst[:0], make([]byte, a.mask)...)
	var widths [256]byte
	controlBits := 0
	for i, v := range values {
		if v.State > Offset {
			return nil, fmt.Errorf("invalid state")
		}
		state := v.State
		f := a.fields[i]
		if state == Direct {
			w := f.width
			if f.eligible {
				size := len(v.Bytes)
				if size > f.width || size != 1 && size != 2 && size != 4 && size != 8 {
					return nil, fmt.Errorf("invalid integer width")
				}
				w = 1 << compactFastCode(v.Bytes, f.signed)
				if w < f.width {
					state = Offset
					controlBits += f.bits
				}
			} else if w == 0 || len(v.Bytes) != w {
				return nil, fmt.Errorf("direct width mismatch")
			}
			widths[i] = byte(w)
		} else if state == Offset && f.width > 0 && len(a.base.Definition.Fields[i].Values) == 0 {
			return nil, fmt.Errorf("logical integer cannot be OFFSET")
		}
		dst[i/4] |= byte(state) << uint(6-2*(i%4))
	}
	dst = append(dst, make([]byte, (controlBits+7)/8)...)
	bit := 0
	for i, v := range values {
		if v.State != Direct {
			continue
		}
		f := a.fields[i]
		w := int(widths[i])
		if f.eligible && w < f.width {
			putCompactCode(dst[a.mask:], bit, f.bits, compactCode(w))
			bit += f.bits
		}
		dst = append(dst, v.Bytes[:w]...)
	}
	var end uint64
	for _, v := range values {
		if v.State != Offset {
			continue
		}
		end += uint64(len(v.Bytes))
		if end > MaxFrame {
			return nil, fmt.Errorf("frame too large")
		}
		dst = extended(dst, end)
	}
	for _, v := range values {
		if v.State == Offset {
			dst = append(dst, v.Bytes...)
		}
	}
	if len(dst) > MaxFrame {
		return nil, fmt.Errorf("frame too large")
	}
	return dst, nil
}
