package cache

import (
	"fmt"
	"math"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

// Projection is compiled once per negotiation. Positional input is always
// interpreted with its source definition, never relabeled as another layout.
type Projection struct {
	source, target *Layout
	slots          []int
	dictionaries   [][]Value
	references     []map[uint16][]byte
	extension      int
}

func CompileProjection(source, target *Layout) (*Projection, error) {
	a, b := source.Definition, target.Definition
	if a.Identity != b.Identity || a.Resource.Major != b.Resource.Major || a.Resource.Roles != b.Resource.Roles {
		return nil, fmt.Errorf("incompatible collection, major or identity roles")
	}
	if b.Version < a.Version {
		return nil, fmt.Errorf("server definition is older than producer definition")
	}
	if a.Version == b.Version && source.Digest != target.Digest {
		return nil, fmt.Errorf("conflicting definitions at the same version")
	}
	p := &Projection{source: source, target: target, extension: -1}
	fields := map[string]int{}
	for i, f := range b.Fields {
		fields[f.Name] = i
		if f.Name == "@fields" {
			p.extension = i
		}
	}
	for _, f := range a.Fields {
		dest, ok := fields[f.Name]
		if !ok {
			if p.extension < 0 {
				return nil, fmt.Errorf("server definition cannot retain field %s", f.Name)
			}
			dest = -1
		} else if b.Fields[dest].Type != f.Type {
			return nil, fmt.Errorf("field %s changes type", f.Name)
		}
		var references map[uint16][]byte
		if referenceType(f.Type) && source.Digest != target.Digest {
			if dest < 0 {
				return nil, fmt.Errorf("reference field %s cannot be demoted to @fields", f.Name)
			}
			references = referenceValues(source, target, f.Name)
			if references == nil {
				return nil, fmt.Errorf("REF16 migration requires a reference-table mapping for %s", f.Name)
			}
		}
		p.references = append(p.references, references)
		p.slots = append(p.slots, dest)
		var dict []Value
		if dest >= 0 && len(f.Values) > 0 {
			labels := map[string]int{}
			for n, label := range b.Fields[dest].Values {
				labels[label] = n
			}
			for _, label := range f.Values {
				if n, ok := labels[label]; ok {
					dict = append(dict, Uint(uint64(n), target.Widths[dest]))
				} else {
					dict = append(dict, Text(label))
				}
			}
		}
		p.dictionaries = append(p.dictionaries, dict)
	}
	return p, nil
}
func (p *Projection) Values(input []Value) ([]Value, error) {
	if len(input) != len(p.slots) {
		return nil, fmt.Errorf("source field count mismatch")
	}
	out := make([]Value, len(p.target.Widths))
	var extras map[string]cbor.RawMessage
	for i, f := range p.source.Definition.Fields {
		if f.Name == "@fields" && input[i].State != Absent {
			if input[i].State != Offset {
				return nil, fmt.Errorf("@fields must be a map")
			}
			if e := decoder.Unmarshal(input[i].Bytes, &extras); e != nil || extras == nil {
				return nil, fmt.Errorf("invalid @fields")
			}
		}
	}
	for i, v := range input {
		if v.State == Absent {
			continue
		}
		f := p.source.Definition.Fields[i]
		if f.Name == "@fields" {
			continue
		}
		dest := p.slots[i]
		if p.references[i] != nil {
			mapped, err := translateReference(f.Type, v, p.references[i])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			v = mapped
		}
		if v.State == Direct && len(f.Values) > 0 {
			n := unsigned(v.Bytes)
			if n >= uint64(len(f.Values)) {
				return nil, fmt.Errorf("invalid source dictionary reference")
			}
			if dest >= 0 {
				v = p.dictionaries[i][n]
			} else {
				v = Text(f.Values[n])
			}
		}
		if dest >= 0 {
			if v.State == Direct && len(v.Bytes) < p.target.Widths[dest] {
				b := make([]byte, p.target.Widths[dest])
				copy(b, v.Bytes)
				if strings.HasPrefix(f.Type, "I") && len(v.Bytes) > 0 && v.Bytes[len(v.Bytes)-1]&128 != 0 {
					for n := len(v.Bytes); n < len(b); n++ {
						b[n] = 255
					}
				}
				v.Bytes = b
			}
			out[dest] = v
			continue
		}
		if extras == nil {
			extras = map[string]cbor.RawMessage{}
		}
		raw, e := fieldCBOR(f, v)
		if e != nil {
			return nil, e
		}
		extras[f.Name] = raw
	}
	// Promote observed extension values when the preferred definition gives them
	// a typed position. Values that cannot be represented fail without data loss.
	for i, f := range p.target.Definition.Fields {
		if f.Name == "@fields" {
			continue
		}
		raw, ok := extras[f.Name]
		if !ok {
			continue
		}
		if out[i].State != Absent {
			return nil, fmt.Errorf("extension shadows %s", f.Name)
		}
		if referenceType(f.Type) && !(len(raw) == 1 && raw[0] == 0xf6) {
			return nil, fmt.Errorf("cannot promote an unbound extension value into %s", f.Name)
		}
		v, e := valueFromCBOR(f, p.target.Widths[i], raw)
		if e != nil {
			return nil, fmt.Errorf("promote %s: %w", f.Name, e)
		}
		out[i] = v
		delete(extras, f.Name)
	}
	if len(extras) > 0 {
		if p.extension < 0 {
			return nil, fmt.Errorf("server definition lacks @fields")
		}
		raw, e := encoder.Marshal(extras)
		if e != nil {
			return nil, e
		}
		out[p.extension] = Value{Offset, raw}
	} else if p.extension >= 0 && extras != nil {
		out[p.extension] = Value{Offset, []byte{0xa0}}
	}
	return out, nil
}
func fieldCBOR(f Field, v Value) ([]byte, error) {
	if v.State == Null {
		return []byte{0xf6}, nil
	}
	if f.Type == "CBOR" {
		return v.Bytes, nil
	}
	if strings.HasPrefix(f.Type, "ARRAY<") {
		element := f.Type[6 : len(f.Type)-1]
		w, e := width(element)
		if e != nil {
			return nil, e
		}
		at := 0
		n, e := readExtended(v.Bytes, &at)
		if e != nil {
			return nil, e
		}
		ends := make([]uint64, int(n))
		if w > 0 {
			for i := range ends {
				ends[i] = uint64((i + 1) * w)
			}
		} else {
			for i := range ends {
				ends[i], e = readExtended(v.Bytes, &at)
				if e != nil {
					return nil, e
				}
			}
		}
		values := make([]cbor.RawMessage, len(ends))
		previous := 0
		for i, end := range ends {
			values[i], e = fieldCBOR(Field{Type: element}, Value{State: Offset, Bytes: v.Bytes[at+previous : at+int(end)]})
			if e != nil {
				return nil, e
			}
			previous = int(end)
		}
		return encoder.Marshal(values)
	}

	if f.Type == "BYTES" || f.Type == "UUID" {
		if v.Bytes == nil {
			return encoder.Marshal([]byte{})
		}
		return encoder.Marshal(v.Bytes)
	}
	if f.Type == "F32" {
		return encoder.Marshal(math.Float32frombits(order.Uint32(v.Bytes)))
	}
	if f.Type == "F64" {
		return encoder.Marshal(math.Float64frombits(order.Uint64(v.Bytes)))
	}
	return encoder.Marshal(inspectValue(f.Type, v.Bytes))
}

// ValueFromCBOR converts one extension value to a declared PIL field. It does
// not decode an entire event or infer a definition. The caller must keep raw
// alive while using the returned value, which may borrow its bytes.
func (l *Layout) ValueFromCBOR(index int, raw []byte) (Value, error) {
	if index < 0 || index >= len(l.Definition.Fields) {
		return Value{}, fmt.Errorf("field index out of range")
	}
	return valueFromCBOR(l.Definition.Fields[index], l.Widths[index], raw)
}

func valueFromCBOR(f Field, w int, raw []byte) (Value, error) {
	if len(raw) == 1 && raw[0] == 0xf6 {
		return Value{State: Null}, nil
	}
	if f.Type == "CBOR" {
		return Value{Offset, raw}, nil
	}
	if strings.HasPrefix(f.Type, "ARRAY<") {
		var items []cbor.RawMessage
		if e := decoder.Unmarshal(raw, &items); e != nil {
			return Value{}, e
		}
		element := f.Type[6 : len(f.Type)-1]
		width, e := width(element)
		if e != nil {
			return Value{}, e
		}
		body := extended(nil, uint64(len(items)))
		var data []byte
		for _, item := range items {
			v, e := valueFromCBOR(Field{Type: element}, width, item)
			if e != nil {
				return Value{}, e
			}
			if v.State == Null {
				return Value{}, fmt.Errorf("native array elements cannot be null")
			}
			data = append(data, v.Bytes...)
			if width == 0 {
				body = extended(body, uint64(len(data)))
			}
		}
		return Value{Offset, append(body, data...)}, nil
	}

	// Reuse the checked scalar decoder for typed extension promotion.
	l := &Layout{Widths: []int{w}, Definition: Definition{Fields: []Field{f}}}
	reader, e := CompilePositionalCBOR(l)
	if e != nil {
		return Value{}, e
	}
	// Dictionary references in @fields are ordinary strings, not numeric IDs.
	if len(f.Values) > 0 {
		var text string
		if e = decoder.Unmarshal(raw, &text); e != nil {
			return Value{}, e
		}
		for i, label := range f.Values {
			if label == text {
				return Uint(uint64(i), w), nil
			}
		}
		return Text(text), nil
	}
	var frame CBORFrame
	record := append([]byte{0xa1, 0}, raw...)
	if e = reader.Scan(record, &frame); e != nil {
		return Value{}, e
	}
	return frame.Values[0], nil
}
