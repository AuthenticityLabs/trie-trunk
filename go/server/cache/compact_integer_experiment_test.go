package cache

// Experimental codec only: Integer mask overrides are not registered PIL wire semantics.
// Keep it out of production binaries until measurements justify adoption and
// protocol negotiation, Rust/WASM, arrays and storage bindings are implemented.
import (
	"fmt"
	"io"
	"math/bits"
	"strings"
	"unicode/utf8"
)

type compactIntegerField struct {
	width, bits      int
	signed, eligible bool
}
type compactIntegerLayout struct {
	base           *Layout
	fields         []compactIntegerField
	mask32, mask64 []byte
	mask           int
	eligible       bool
}

func compileCompactIntegerExperiment(d Definition) (*compactIntegerLayout, error) {
	l, err := Compile(d)
	if err != nil {
		return nil, err
	}
	a := &compactIntegerLayout{base: l, fields: make([]compactIntegerField, len(d.Fields)), mask: (len(d.Fields) + 3) / 4}
	a.mask32 = make([]byte, a.mask)
	a.mask64 = make([]byte, a.mask)
	for i, f := range d.Fields {
		kind := f.Type
		if strings.HasPrefix(kind, "ARRAY<") || kind == "CBOR" || f.Name == "@fields" {
			return nil, fmt.Errorf("experimental scalar profile excludes %s", kind)
		}
		af := compactIntegerField{width: l.Widths[i], signed: kind == "I8" || kind == "I16" || kind == "I32" || kind == "I64"}
		switch kind {
		case "U32", "I32":
			af.eligible = true
			af.bits = 1
			a.mask32[i/4] |= 1 << uint(6-2*(i%4))
		case "U64", "I64":
			af.eligible = true
			af.bits = 2
			a.mask64[i/4] |= 1 << uint(6-2*(i%4))
		}
		a.eligible = a.eligible || af.eligible
		a.fields[i] = af
	}
	return a, nil
}

// Decode a signed scalar only when requested by its consumer. The scanner
// borrows its original bytes just as the fixed PIL scanner does.
func compactSigned(b []byte) int64 {
	u := unsigned(b)
	if len(b) < 8 && b[len(b)-1]&0x80 != 0 {
		u |= ^uint64(0) << uint(len(b)*8)
	}
	return int64(u)
}
func compactWidth(b []byte, signed bool) int {
	u := unsigned(b)
	if signed {
		s := compactSigned(b)
		u = uint64(s ^ (s >> 63))
		switch {
		case u <= 127:
			return 1
		case u <= 32767:
			return 2
		case u <= 2147483647:
			return 4
		default:
			return 8
		}
	}
	switch {
	case u <= 255:
		return 1
	case u <= 65535:
		return 2
	case u <= 4294967295:
		return 4
	default:
		return 8
	}
}
func compactCode(width int) byte {
	switch width {
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

// MSB-first selectors; a two-bit selector may straddle bytes after a one-bit
// selector. The total plane length is derived from definition + presence mask.
func putCompactCode(b []byte, bit, bits int, code byte) {
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
func getCompactCode(b []byte, bit, bits int) byte {
	if bits == 0 {
		return 0
	}
	i, offset := bit/8, bit%8
	if offset+bits <= 8 {
		return (b[i] >> uint(8-offset-bits)) & byte((1<<bits)-1)
	}
	return (b[i]&1)<<1 | b[i+1]>>7
}

// 11 is COMPACT for eligible integers, OFFSET for variable fields. Only
// COMPACT fields contribute width bits. 00/01/10 keep existing semantics.
func (a *compactIntegerLayout) controlBits(mask []byte) int {
	count := 0
	for i := 0; i < a.mask; i++ {
		both := mask[i] & (mask[i] >> 1)
		count += bits.OnesCount8(both&a.mask32[i]) + 2*bits.OnesCount8(both&a.mask64[i])
	}
	return count
}

func (a *compactIntegerLayout) encode(dst []byte, values []Value) ([]byte, error) {
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
				w = compactWidth(v.Bytes, f.signed)
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

func (a *compactIntegerLayout) scan(raw []byte, reuse *Frame) error {
	if len(raw) < a.mask || len(raw) > MaxFrame {
		return fmt.Errorf("invalid frame size")
	}
	n := len(a.fields)
	if n%4 != 0 && raw[a.mask-1]&byte((1<<((4-n%4)*2))-1) != 0 {
		return fmt.Errorf("nonzero mask padding")
	}
	bits := a.controlBits(raw)
	if bits == 0 {
		return a.base.Scan(raw, reuse)
	}
	controlEnd := a.mask + (bits+7)/8
	if len(raw) < controlEnd {
		return fmt.Errorf("short width plane")
	}
	controls := raw[a.mask:controlEnd]
	if bits%8 != 0 && controls[len(controls)-1]&byte((1<<(8-bits%8))-1) != 0 {
		return fmt.Errorf("nonzero width padding")
	}
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
		compact := state == Offset && f.eligible
		if state == Direct || compact {
			w := f.width
			if compact {
				w = 1 << getCompactCode(controls, bit, f.bits)
				bit += f.bits
				if w >= f.width {
					return fmt.Errorf("compact width is not shorter")
				}
			}
			if w == 0 || w > f.width || w > len(raw)-at {
				return fmt.Errorf("invalid direct width")
			}
			v.State = Direct
			v.Bytes = raw[at : at+w]
			at += w
			definition := a.base.Definition.Fields[i]
			if definition.Type == "BOOL" && v.Bytes[0] > 1 {
				return fmt.Errorf("invalid bool")
			}
			if len(definition.Values) > 0 && unsigned(v.Bytes) >= uint64(len(definition.Values)) {
				return fmt.Errorf("invalid dictionary reference")
			}
		} else if state == Offset && f.width > 0 && len(a.base.Definition.Fields[i].Values) == 0 {
			return fmt.Errorf("offset integer")
		}
		reuse.Values[i] = v
	}
	previous := 0
	for i, v := range reuse.Values {
		if v.State != Offset {
			continue
		}
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
	if previous != len(raw)-at {
		return fmt.Errorf("frame boundary mismatch")
	}
	for i, v := range reuse.Values {
		if v.State != Offset {
			continue
		}
		v.Bytes = raw[at+reuse.ends[i][0] : at+reuse.ends[i][1]]
		if a.base.Definition.Fields[i].Type == "STRING" && !utf8.Valid(v.Bytes) {
			return fmt.Errorf("invalid UTF-8")
		}
		reuse.Values[i] = v
	}
	reuse.Bytes = raw
	return nil
}

// Discover the boundary from mask, width controls and final end offset without
// decoding integer values or adding a frame-length prefix.
func (a *compactIntegerLayout) readFrame(r io.Reader) ([]byte, error) {
	b := make([]byte, a.mask)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	read := func(n int) error {
		if n < 0 || n > MaxFrame-len(b) {
			return fmt.Errorf("frame too large")
		}
		at := len(b)
		b = append(b, make([]byte, n)...)
		_, err := io.ReadFull(r, b[at:])
		return err
	}
	bits := a.controlBits(b)
	if err := read((bits + 7) / 8); err != nil {
		return nil, err
	}
	control := b[a.mask:]
	direct, offsets, bit := 0, 0, 0
	for i, f := range a.fields {
		state := State(b[i/4] >> uint(6-2*(i%4)) & 3)
		compact := state == Offset && f.eligible
		if state == Direct || compact {
			w := f.width
			if compact {
				w = 1 << getCompactCode(control, bit, f.bits)
				bit += f.bits
				if w >= f.width {
					return nil, fmt.Errorf("compact width is not shorter")
				}
			}
			if w == 0 || w > f.width {
				return nil, fmt.Errorf("invalid width")
			}
			direct += w
		} else if state == Offset {
			offsets++
		}
	}
	if err := read(direct); err != nil {
		return nil, err
	}
	var previous uint64
	for i := 0; i < offsets; i++ {
		start := len(b)
		if err := read(2); err != nil {
			return nil, err
		}
		if order.Uint16(b[start:]) == 65535 {
			if err := read(4); err != nil {
				return nil, err
			}
			if order.Uint32(b[start+2:]) == 0xffffffff {
				if err := read(8); err != nil {
					return nil, err
				}
			}
		}
		at := start
		end, err := readExtended(b, &at)
		if err != nil {
			return nil, err
		}
		if end < previous || end > MaxFrame {
			return nil, fmt.Errorf("invalid end offset")
		}
		previous = end
	}
	if err := read(int(previous)); err != nil {
		return nil, err
	}
	var check Frame
	if err := a.scan(b, &check); err != nil {
		return nil, err
	}
	return b, nil
}
