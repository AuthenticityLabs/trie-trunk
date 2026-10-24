package cache

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/x448/float16"
)

// PositionalCBOR decodes a definite-length CBOR map with numeric field IDs
// directly into definition-bound scalar slots. It is an opt-in codec prototype,
// not a change to population storage. CBOR arrays, nested objects, tags, and
// indefinite-length items are intentionally outside this scalar profile.
// Encoding details: https://www.rfc-editor.org/rfc/rfc8949.html#section-3.1
// Descriptors are compiled once, never inferred from a particular event.
// A reader may be shared; each concurrent caller must own its CBORFrame.
type PositionalCBOR struct{ fields []cborField }
type cborField struct {
	kind  cborKind
	width int
	limit uint64
}
type cborKind uint8

const (
	cborUnsigned cborKind = iota
	cborSigned
	cborBool
	cborFloat
	cborUUID
	cborString
	cborBytes
	cborDictionary
)

// CBORFrame owns little-endian scalar scratch; text/byte/UUID values borrow the
// input buffer. Values remain valid only until either buffer is reused. Once
// sized, Scan allocates no maps, strings, per-scalar objects, or new buffers.
type CBORFrame struct {
	Frame
	scalars []byte
}

func CompilePositionalCBOR(l *Layout) (*PositionalCBOR, error) {
	if l == nil || len(l.Widths) != len(l.Definition.Fields) || len(l.Widths) == 0 {
		return nil, fmt.Errorf("invalid positional CBOR definition")
	}
	r := &PositionalCBOR{fields: make([]cborField, len(l.Widths))}
	for i, f := range l.Definition.Fields {
		d := cborField{width: l.Widths[i]}
		switch f.Type {
		case "U8", "U16", "U32", "U64", "REF16":
			d.kind = cborUnsigned
		case "I8", "I16", "I32", "I64", "TIMESTAMP_MS", "TIMESTAMP_US", "TIMESTAMP_NS":
			d.kind = cborSigned
		case "BOOL":
			d.kind = cborBool
		case "F32", "F64":
			d.kind = cborFloat
		case "UUID":
			d.kind = cborUUID
		case "STRING":
			d.kind = cborString
		case "BYTES":
			d.kind = cborBytes
		default:
			return nil, fmt.Errorf("positional CBOR scalar profile does not support %s", f.Type)
		}
		if len(f.Values) > 0 {
			d.kind = cborDictionary
			d.limit = uint64(len(f.Values))
		}
		r.fields[i] = d
	}
	return r, nil
}

// cborHead accepts all well-formed definite-length integer widths, including
// non-preferred encodings. Bounds are checked before each read. ai retains the
// float representation width; major seven is interpreted separately below.
func cborHead(raw []byte, at *int) (major, ai byte, n uint64, err error) {
	if *at >= len(raw) {
		return 0, 0, 0, fmt.Errorf("truncated CBOR head")
	}
	b := raw[*at]
	*at++
	major, ai = b>>5, b&31
	switch ai {
	case 24:
		if len(raw)-*at < 1 {
			return 0, 0, 0, fmt.Errorf("truncated CBOR uint8")
		}
		n = uint64(raw[*at])
		*at++
	case 25:
		if len(raw)-*at < 2 {
			return 0, 0, 0, fmt.Errorf("truncated CBOR uint16")
		}
		n = uint64(binary.BigEndian.Uint16(raw[*at:]))
		*at += 2
	case 26:
		if len(raw)-*at < 4 {
			return 0, 0, 0, fmt.Errorf("truncated CBOR uint32")
		}
		n = uint64(binary.BigEndian.Uint32(raw[*at:]))
		*at += 4
	case 27:
		if len(raw)-*at < 8 {
			return 0, 0, 0, fmt.Errorf("truncated CBOR uint64")
		}
		n = binary.BigEndian.Uint64(raw[*at:])
		*at += 8
	default:
		if ai >= 28 {
			return 0, 0, 0, fmt.Errorf("reserved or indefinite CBOR head")
		}
		n = uint64(ai)
	}
	return
}

func (r *PositionalCBOR) Scan(raw []byte, reuse *CBORFrame) error {
	if len(raw) > MaxFrame {
		return fmt.Errorf("CBOR frame too large")
	}
	at := 0
	major, _, count, e := cborHead(raw, &at)
	if e != nil {
		return e
	}
	if major != 5 || count > uint64(len(r.fields)) || count > uint64((len(raw)-at)/2) {
		return fmt.Errorf("invalid CBOR field map")
	}
	n := len(r.fields)
	if cap(reuse.Values) < n {
		reuse.Values = make([]Value, n)
	} else {
		reuse.Values = reuse.Values[:n]
		clear(reuse.Values)
	}
	if cap(reuse.scalars) < n*8 {
		reuse.scalars = make([]byte, n*8)
	} else {
		reuse.scalars = reuse.scalars[:n*8]
	}
	reuse.Bytes = nil
	for pair := uint64(0); pair < count; pair++ {
		major, _, key, e := cborHead(raw, &at)
		if e != nil {
			return e
		}
		if major != 0 || key >= uint64(n) {
			return fmt.Errorf("unknown or non-numeric CBOR field")
		}
		i := int(key)
		if reuse.Values[i].State != Absent {
			return fmt.Errorf("duplicate CBOR field")
		}
		major, ai, v, e := cborHead(raw, &at)
		if e != nil {
			return e
		}
		if major == 7 && ai == 22 {
			reuse.Values[i] = Value{State: Null}
			continue
		}
		d := r.fields[i]
		scalar := reuse.scalars[i*8 : (i+1)*8]
		switch d.kind {
		case cborUnsigned, cborSigned, cborDictionary:
			// A dictionary-bound STRING may also carry a literal, as in PIL.
			if d.kind == cborDictionary && major == 3 {
				if v > uint64(len(raw)-at) {
					return fmt.Errorf("truncated CBOR string")
				}
				text := raw[at : at+int(v)]
				at += int(v)
				if !utf8.Valid(text) {
					return fmt.Errorf("invalid CBOR UTF-8")
				}
				reuse.Values[i] = Value{Offset, text}
				continue
			}
			if d.kind == cborSigned {
				if major > 1 || v > uint64(math.MaxInt64) || d.width < 8 && v >= uint64(1)<<uint(d.width*8-1) {
					return fmt.Errorf("CBOR signed integer overflow or type mismatch")
				}
				if major == 1 {
					v = ^v
				}
			} else {
				if major != 0 || d.width < 8 && v >= uint64(1)<<uint(d.width*8) {
					return fmt.Errorf("CBOR unsigned integer overflow or type mismatch")
				}
				if d.kind == cborDictionary && v >= d.limit {
					return fmt.Errorf("invalid CBOR dictionary reference")
				}
			}
			order.PutUint64(scalar, v)
			reuse.Values[i] = Value{Direct, scalar[:d.width]}
		case cborBool:
			if major != 7 || ai != 20 && ai != 21 {
				return fmt.Errorf("invalid CBOR boolean")
			}
			scalar[0] = ai - 20
			reuse.Values[i] = Value{Direct, scalar[:1]}
		case cborFloat:
			if major != 7 || ai < 25 || ai > 27 {
				return fmt.Errorf("invalid CBOR float")
			}
			var f float64
			switch ai {
			case 25:
				f = float64(float16.Frombits(uint16(v)).Float32())
			case 26:
				f = float64(math.Float32frombits(uint32(v)))
			case 27:
				f = math.Float64frombits(v)
			}
			if d.width == 4 {
				small := float32(f)
				if !math.IsNaN(f) && float64(small) != f {
					return fmt.Errorf("CBOR float is not representable as F32")
				}
				order.PutUint32(scalar, math.Float32bits(small))
			} else {
				order.PutUint64(scalar, math.Float64bits(f))
			}
			reuse.Values[i] = Value{Direct, scalar[:d.width]}
		case cborUUID, cborString, cborBytes:
			want := byte(2)
			if d.kind == cborString {
				want = 3
			}
			if major != want || v > uint64(len(raw)-at) {
				return fmt.Errorf("CBOR string type or boundary mismatch")
			}
			value := raw[at : at+int(v)]
			at += int(v)
			state := Offset
			if d.kind == cborUUID {
				if v != 16 {
					return fmt.Errorf("invalid CBOR UUID width")
				}
				state = Direct
			}
			if d.kind == cborString && !utf8.Valid(value) {
				return fmt.Errorf("invalid CBOR UTF-8")
			}
			reuse.Values[i] = Value{state, value}
		}
	}
	if at != len(raw) {
		return fmt.Errorf("trailing CBOR data")
	}
	reuse.Bytes = raw
	return nil
}
