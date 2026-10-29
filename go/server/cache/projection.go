package cache

import (
	"fmt"
	"strings"

	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
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
		if f.Name != "@leaf" && referenceType(f.Type) && source.Digest != target.Digest {
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
	var extras map[string]pil.RawObject
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
		if f.Name == "@fields" || f.Name == "@leaf" {
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
			extras = map[string]pil.RawObject{}
		}
		raw, e := fieldObject(f, v)
		if e != nil {
			return nil, e
		}
		extras[f.Name] = raw
	}
	// Promote observed extension values when the preferred definition gives them
	// a typed position. Values that cannot be represented fail without data loss.
	for i, f := range p.target.Definition.Fields {
		if f.Name == "@fields" || f.Name == "@leaf" {
			continue
		}
		raw, ok := extras[f.Name]
		if !ok {
			continue
		}
		if out[i].State != Absent {
			return nil, fmt.Errorf("extension shadows %s", f.Name)
		}
		if referenceType(f.Type) && !isNullObject(raw) {
			return nil, fmt.Errorf("cannot promote an unbound extension value into %s", f.Name)
		}
		v, e := valueFromObject(f, p.target.Widths[i], raw)
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
		out[p.extension] = Value{Offset, emptyObject()}
	}
	return out, nil
}
