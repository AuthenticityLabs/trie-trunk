package cache

// Test-only corrected experiment. The field-state mask is unchanged: 00 absent,
// 01 null, 10 direct, 11 offset. A separate packed width plane follows it.
// Every present eligible DIRECT integer contributes a selector INCLUDING native
// width: 16-bit => 1 bit (1/2 bytes); 32/64-bit => 2 bits (1/2/4[/8] bytes).
// No selector for absent/null, U8/I8, dictionary references or variable fields.
// The format profile is known by both ends; this is not silently compatible
// with the original fixed-width wire format and is not production-enabled.
import (
	"fmt"
	"unicode/utf8"
)

type separateWidthLayout struct {
	*fastIntegerLayout
	reserved int
}

func compileSeparateWidthExperiment(d Definition, include16 bool) (*separateWidthLayout, error) {
	ref, err := compileCompactIntegerExperiment(d)
	if err != nil {
		return nil, err
	}
	p := compileFastIntegerExperiment(ref)
	total := 0
	for i := range p.fields {
		f := &p.fields[i]
		if include16 && (d.Fields[i].Type == "U16" || d.Fields[i].Type == "I16") {
			f.eligible = true
		}
		if f.eligible {
			f.selectorBits = 2
			if f.width == 2 {
				f.selectorBits = 1
			}
			total += f.selectorBits
		}
	}
	// Number of width bits is discoverable from the unchanged DIRECT state mask.
	for i := range p.controls {
		for m := 0; m < 256; m++ {
			count := 0
			for j := 0; j < 4 && i*4+j < len(p.fields); j++ {
				f := p.fields[i*4+j]
				if State(m>>uint(6-2*j)&3) == Direct && f.eligible {
					count += f.selectorBits
				}
			}
			p.controls[i][m] = byte(count)
		}
	}
	return &separateWidthLayout{p, (total + 7) / 8}, nil
}
func (a *separateWidthLayout) encode(dst []byte, values []Value, compact bool) ([]byte, error) {
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
					controls[controlBits/8] |= code << uint(7-controlBits%8)
					controlBits++
				} else {
					code := compactFastCode(v.Bytes, f.signed)
					w = 1 << code
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
	// One scratch-capacity byte permits native U16 copies; never emitted.
	capacity := size
	if compact {
		capacity++
	}
	if cap(dst) < capacity {
		dst = make([]byte, size, capacity)
	} else {
		dst = dst[:size]
	}
	copy(dst, mask[:a.mask])
	copy(dst[a.mask:], controls[:controlBytes])
	dst = dst[:a.mask+controlBytes]
	for i, v := range values {
		if v.State == Direct {
			if compact && a.fields[i].eligible && a.fields[i].width == 2 {
				at := len(dst)
				dst = dst[:at+len(v.Bytes)]
				copy(dst[at:], v.Bytes)
				dst = dst[:at+int(widths[i])]
			} else {
				dst = append(dst, v.Bytes[:widths[i]]...)
			}
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

func (a *separateWidthLayout) scan(raw []byte, reuse *Frame) error {
	return a.scanMode(raw, reuse, true)
}
func (a *separateWidthLayout) scanMode(raw []byte, reuse *Frame, compact bool) error {
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
	if cap(reuse.Ends) < n {
		reuse.Ends = make([][2]int, n)
	} else {
		reuse.Ends = reuse.Ends[:n]
	}
	at, bit := controlEnd, 0
	for i, f := range a.fields {
		state := State(raw[i/4] >> uint(6-2*(i%4)) & 3)
		v := Value{State: state}
		if state < Direct {
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
			if compact && f.eligible {
				if f.width == 2 {
					w = 1 + int(controls[bit/8]>>uint(7-bit%8)&1)
					bit++
				} else {
					w = 1 << getCompactCode(controls, bit, f.selectorBits)
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
			reuse.Ends[i] = [2]int{previous, int(end)}
			previous = int(end)
		}
	}
	if previous != len(raw)-at {
		return fmt.Errorf("frame boundary mismatch")
	}
	for i, v := range reuse.Values {
		if v.State == Offset {
			v.Bytes = raw[at+reuse.Ends[i][0] : at+reuse.Ends[i][1]]
			if a.fields[i].text && !utf8.Valid(v.Bytes) {
				return fmt.Errorf("invalid UTF-8")
			}
			reuse.Values[i] = v
		}
	}
	reuse.Bytes = raw
	return nil
}

func (a *separateWidthLayout) encodeReuse(dst []byte, values []Value) ([]byte, error) {
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
					code := compact16Code(v.Bytes, f.signed)
					w = 1 + int(code)
					dst[a.mask+bit/8] |= code << uint(7-bit%8)
					bit++
				} else {
					code := compactFastCode(v.Bytes, f.signed)
					w = 1 << code
					putCompactCode(dst[a.mask:prefix], bit, f.selectorBits, code)
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
