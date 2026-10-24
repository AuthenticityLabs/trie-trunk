package cache

import (
	"encoding/binary"
	"fmt"
	"github.com/fxamacker/cbor/v2"
	"io"
	"strings"
	"unicode/utf8"
)

type State byte

const (
	Absent State = iota
	Null
	Direct
	Offset
)

type Value struct {
	State State  `cbor:"0,keyasint"`
	Bytes []byte `cbor:"1,keyasint"`
}
type Frame struct {
	Values                             []Value
	Bytes                              []byte
	ends                               [][2]int
	compressedIdentity, compressedBody *Frame
	inflated                           []byte
}

// Scan borrows scalar bytes from the frame. It never constructs an object map.
func (l *Layout) Scan(raw []byte, reuse *Frame) error {
	if l.IsCompressed(raw) {
		return l.scanCompressed(raw, reuse)
	}
	return l.scanPlain(raw, reuse)
}
func (l *Layout) scanPlain(raw []byte, reuse *Frame) error {
	if l.integers != nil {
		return l.integers.scan(raw, reuse)
	}
	if len(raw) > MaxFrame {
		return fmt.Errorf("frame too large")
	}
	n := len(l.Widths)
	mask := (n + 3) / 4
	if len(raw) < mask {
		return fmt.Errorf("short mask")
	}
	if n%4 != 0 && raw[mask-1]&byte((1<<((4-n%4)*2))-1) != 0 {
		return fmt.Errorf("nonzero unused mask bits")
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
	at := mask
	for i, f := range l.Definition.Fields {
		state := State(raw[i/4] >> uint(6-2*(i%4)) & 3)
		v := Value{State: state}
		if f.Name == "@fields" && state == Null {
			return fmt.Errorf("@fields cannot be null")
		}
		if state == Direct {
			w := l.Widths[i]
			if w == 0 || w > len(raw)-at {
				return fmt.Errorf("invalid direct field")
			}
			v.Bytes = raw[at : at+w]
			at += w
			if f.Type == "BOOL" && v.Bytes[0] > 1 {
				return fmt.Errorf("invalid bool")
			}
			if len(f.Values) > 0 && unsigned(v.Bytes) >= uint64(len(f.Values)) {
				return fmt.Errorf("invalid dictionary reference")
			}
		} else if state == Offset && l.Widths[i] > 0 && len(f.Values) == 0 {
			return fmt.Errorf("offset fixed-width field")
		}
		reuse.Values[i] = v
	}
	ends := reuse.ends
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
		ends[i] = [2]int{previous, int(end)}
		previous = int(end)
	}
	if previous != len(raw)-at {
		return fmt.Errorf("frame boundary mismatch")
	}
	for i, v := range reuse.Values {
		if v.State != Offset {
			continue
		}
		v.Bytes = raw[at+ends[i][0] : at+ends[i][1]]
		if err := l.validateOffset(i, v.Bytes); err != nil {
			return err
		}
		reuse.Values[i] = v
	}
	reuse.Bytes = raw
	return nil
}
func unsigned(b []byte) uint64 { var p [8]byte; copy(p[:], b); return binary.LittleEndian.Uint64(p[:]) }
func readExtended(b []byte, at *int) (uint64, error) {
	if len(b)-*at < 2 {
		return 0, fmt.Errorf("short extended integer")
	}
	v := uint64(binary.LittleEndian.Uint16(b[*at:]))
	*at += 2
	if v < 65535 {
		return v, nil
	}
	if len(b)-*at < 4 {
		return 0, fmt.Errorf("short extended integer")
	}
	v = uint64(binary.LittleEndian.Uint32(b[*at:]))
	*at += 4
	if v < 0xffffffff {
		if v < 65535 {
			return 0, fmt.Errorf("noncanonical extended integer")
		}
		return v, nil
	}
	if len(b)-*at < 8 {
		return 0, fmt.Errorf("short extended integer")
	}
	v = binary.LittleEndian.Uint64(b[*at:])
	*at += 8
	if v < 0xffffffff {
		return 0, fmt.Errorf("noncanonical extended integer")
	}
	return v, nil
}
func extended(dst []byte, n uint64) []byte {
	if n < 65535 {
		return binary.LittleEndian.AppendUint16(dst, uint16(n))
	}
	dst = binary.LittleEndian.AppendUint16(dst, 65535)
	if n < 0xffffffff {
		return binary.LittleEndian.AppendUint32(dst, uint32(n))
	}
	dst = binary.LittleEndian.AppendUint32(dst, 0xffffffff)
	return binary.LittleEndian.AppendUint64(dst, n)
}
func validateArray(kind string, b []byte) error {
	if !strings.HasPrefix(kind, "ARRAY<") || !strings.HasSuffix(kind, ">") {
		return fmt.Errorf("invalid array type")
	}
	kind = kind[6 : len(kind)-1]
	at := 0
	count, err := readExtended(b, &at)
	if err != nil {
		return err
	}
	w, err := width(kind)
	if err != nil {
		return err
	}
	if w > 0 {
		if count > uint64((len(b)-at)/w) || uint64(len(b)-at) != count*uint64(w) {
			return fmt.Errorf("array size mismatch")
		}
		if kind == "BOOL" {
			for _, v := range b[at:] {
				if v > 1 {
					return fmt.Errorf("invalid array boolean")
				}
			}
		}
		return nil
	}
	if count > uint64((len(b)-at)/2) {
		return fmt.Errorf("array offset count exceeds boundary")
	}
	ends := make([]uint64, int(count))
	var prev uint64
	for i := range ends {
		n, e := readExtended(b, &at)
		if e != nil {
			return e
		}
		if n < prev || n > uint64(len(b)) {
			return fmt.Errorf("invalid array end")
		}
		ends[i] = n
		prev = n
	}
	if prev != uint64(len(b)-at) {
		return fmt.Errorf("array boundary mismatch")
	}
	prev = 0
	for _, end := range ends {
		v := b[at+int(prev) : at+int(end)]
		switch kind {
		case "STRING":
			if !utf8.Valid(v) {
				return fmt.Errorf("invalid array UTF-8")
			}
		case "CBOR":
			if err := cbor.Valid(v); err != nil {
				return err
			}
		}
		prev = end
	}
	return nil
}

// Encode preserves borrowed, already encoded primitive values and field states.
func (l *Layout) Encode(dst []byte, values []Value) ([]byte, error) {
	if l.integers != nil {
		return l.integers.encode(dst, values)
	}
	if len(values) != len(l.Widths) {
		return nil, fmt.Errorf("field count mismatch")
	}
	dst = append(dst[:0], make([]byte, (len(values)+3)/4)...)
	for i, v := range values {
		if v.State > Offset {
			return nil, fmt.Errorf("invalid state")
		}
		dst[i/4] |= byte(v.State) << uint(6-2*(i%4))
		if v.State == Direct {
			if len(v.Bytes) != l.Widths[i] || l.Widths[i] == 0 {
				return nil, fmt.Errorf("direct width mismatch")
			}
			dst = append(dst, v.Bytes...)
		}
	}
	var end uint64
	for _, v := range values {
		if v.State == Offset {
			end += uint64(len(v.Bytes))
			if end > MaxFrame {
				return nil, fmt.Errorf("frame too large")
			}
			dst = extended(dst, end)
		}
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
func Uint(n uint64, w int) Value {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	return Value{Direct, append([]byte(nil), b[:w]...)}
}
func Text(s string) Value { return Value{Offset, []byte(s)} }

// ReadFrame discovers the boundary from the definition and final cumulative
// offset, using PIL's normal layout rather than an extra per-frame length tag.
func (l *Layout) ReadFrame(input io.Reader, dst []byte) ([]byte, error) {
	return l.readFrame(input, dst, true)
}
func (l *Layout) readFrame(input io.Reader, dst []byte, envelope bool) ([]byte, error) {
	return l.readFrameBuffer(input, dst, envelope, nil)
}

// Native ingress supplies a resizer so no frame payload is allocated by Go.
func (l *Layout) readFrameBuffer(input io.Reader, dst []byte, envelope bool, resize func(int) ([]byte, error)) ([]byte, error) {
	mask := (len(l.Widths) + 3) / 4
	var e error
	if resize != nil {
		dst, e = resize(mask)
		if e != nil {
			return nil, e
		}
	} else {
		dst = append(dst[:0], make([]byte, mask)...)
	}
	n, e := io.ReadFull(input, dst)
	if e == io.EOF && n == 0 {
		return nil, io.EOF
	}
	if e != nil {
		return nil, e
	}
	appendN := func(n int) error {
		if n < 0 || len(dst) > MaxFrame-n {
			return fmt.Errorf("frame too large")
		}
		at := len(dst)
		if resize != nil {
			var err error
			dst, err = resize(at + n)
			if err != nil {
				return err
			}
		} else {
			dst = append(dst, make([]byte, n)...)
		}
		_, err := io.ReadFull(input, dst[at:])
		return err
	}
	if envelope && l.compressedMarker(dst) {
		if e = appendN(compressionHeader); e != nil {
			return nil, e
		}
		_, identity, _, stored, err := l.compressionLengths(dst)
		if err != nil {
			return nil, err
		}
		if e = appendN(identity + stored); e != nil {
			return nil, e
		}
		return dst, nil
	}
	var plane []byte
	bit := 0
	if l.integers != nil {
		n := 0
		for i := range l.integers.controls {
			n += int(l.integers.controls[i][dst[i]])
		}
		if e = appendN((n + 7) / 8); e != nil {
			return nil, e
		}
		plane, _, e = l.integers.plane(dst)
		if e != nil {
			return nil, e
		}
	}
	direct, offsets := 0, 0
	for i, w := range l.Widths {
		switch State(dst[i/4] >> uint(6-2*(i%4)) & 3) {
		case Direct:
			if l.integers != nil {
				w, e = l.integers.fieldWidth(i, plane, &bit)
				if e != nil {
					return nil, e
				}
			}
			if w == 0 {
				return nil, fmt.Errorf("invalid direct field")
			}
			direct += w
		case Offset:
			offsets++
		}
	}
	if e = appendN(direct); e != nil {
		return nil, e
	}
	var previous uint64
	for i := 0; i < offsets; i++ {
		start := len(dst)
		if e = appendN(2); e != nil {
			return nil, e
		}
		if order.Uint16(dst[start:]) == 65535 {
			if e = appendN(4); e != nil {
				return nil, e
			}
			if order.Uint32(dst[start+2:]) == 0xffffffff {
				if e = appendN(8); e != nil {
					return nil, e
				}
			}
		}
		at := start
		end, e := readExtended(dst, &at)
		if e != nil {
			return nil, e
		}
		if end < previous || end > MaxFrame {
			return nil, fmt.Errorf("invalid end offset")
		}
		previous = end
	}
	if e = appendN(int(previous)); e != nil {
		return nil, e
	}
	return dst, nil
}

func (l *Layout) validateOffset(i int, raw []byte) error {
	f := l.Definition.Fields[i]
	switch f.Type {
	case "STRING":
		if !utf8.Valid(raw) {
			return fmt.Errorf("invalid UTF-8")
		}
	case "CBOR":
		if err := cbor.Valid(raw); err != nil {
			return err
		}
		if f.Name == "@fields" {
			if len(raw) == 0 || raw[0]>>5 != 5 {
				return fmt.Errorf("@fields must be a map")
			}
			var extra map[string]cbor.RawMessage
			if err := decoder.Unmarshal(raw, &extra); err != nil {
				return err
			}
			for _, field := range l.Definition.Fields {
				if _, ok := extra[field.Name]; ok {
					return fmt.Errorf("@fields shadows %s", field.Name)
				}
			}
		}
	case "BYTES":
	default:
		if err := validateArray(f.Type, raw); err != nil {
			return err
		}
	}
	return nil
}

// ValidateValues checks producer values without constructing an intermediate
// frame. Used when a negotiated layout needs a different positional encoding.
func (l *Layout) ValidateValues(values []Value) error {
	if len(values) != len(l.Widths) {
		return fmt.Errorf("field count mismatch")
	}
	for i, v := range values {
		f := l.Definition.Fields[i]
		switch v.State {
		case Absent:
		case Null:
			if f.Name == "@fields" {
				return fmt.Errorf("@fields cannot be null")
			}
		case Direct:
			if l.Widths[i] == 0 || len(v.Bytes) != l.Widths[i] {
				return fmt.Errorf("direct width mismatch")
			}
			if f.Type == "BOOL" && v.Bytes[0] > 1 {
				return fmt.Errorf("invalid bool")
			}
			if len(f.Values) > 0 && unsigned(v.Bytes) >= uint64(len(f.Values)) {
				return fmt.Errorf("invalid dictionary reference")
			}
		case Offset:
			if l.Widths[i] > 0 && len(f.Values) == 0 {
				return fmt.Errorf("offset fixed-width field")
			}
			if e := l.validateOffset(i, v.Bytes); e != nil {
				return e
			}
		default:
			return fmt.Errorf("invalid state")
		}
	}
	return nil
}
