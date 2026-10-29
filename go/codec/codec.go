// Package codec provides the shared dictionary and JSON/CBOR wire vocabulary.
// It has no dependency on logging or HTTP.
package codec

import (
	"bytes"
	"encoding/json"
	"fmt"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"strconv"

	"github.com/fxamacker/cbor/v2"
)

type Format uint8

const (
	ReadableJSON Format = 0
	CompactJSON  Format = 1
	CBOR         Format = 2
	PIL          Format = 3
)

type Key uint16
type Record map[Key]any

// MarshalCompact encodes fixed, tagged structs without building intermediate
// maps. JSON tags hold decimal keys; CBOR tags use keyasint.
func MarshalCompact(value any, format Format) ([]byte, error) {
	switch format {
	case CompactJSON:
		return json.Marshal(value)
	case PIL:
		return pil.MarshalObject(value)
	default:
		return nil, fmt.Errorf("expected compact format")
	}
}

// Symbol is either a dictionary reference or literal text. Numeric zero means
// unspecified; Text("") remains a present, empty literal.
type Symbol struct {
	text    string
	id      uint16
	literal bool
}

func Ref(id uint16) Symbol    { return Symbol{id: id} }
func Text(text string) Symbol { return Symbol{text: text, literal: true} }
func (s Symbol) Id() uint16   { return s.id }
func (s Symbol) IsZero() bool { return !s.literal && s.id == 0 }
func (s Symbol) LimitText(max int) Symbol {
	if s.literal {
		s.text = LimitText(s.text, max)
	}
	return s
}
func (s Symbol) Raw() any {
	if s.literal {
		return s.text
	}
	return s.id
}
func (s Symbol) MarshalJSON() ([]byte, error) {
	if s.literal {
		return json.Marshal(s.text)
	}
	return strconv.AppendUint(nil, uint64(s.id), 10), nil
}

// Dictionary is immutable after construction. Names identify the application
// catalog, including its version; a name must never be reused for different references.
type Dictionary struct {
	name    string
	domains map[string]map[uint16]string
}

// ValidateReserved prevents application catalogs from redefining built-in
// values. Application error/message references start at 128.
func (d *Dictionary) ValidateReserved() error {
	if d == nil {
		return nil
	}
	for _, domain := range []string{"error_code", "error_type", "error_message", "message"} {
		for id := range d.domains[domain] {
			if id < 128 {
				return fmt.Errorf("%s reference %d is reserved; application values start at 128", domain, id)
			}
		}
	}
	return nil
}

func NewDictionary(name string, domains map[string]map[uint16]string) (*Dictionary, error) {
	if name == "" {
		return nil, fmt.Errorf("dictionary name is required")
	}
	d := &Dictionary{name: name, domains: make(map[string]map[uint16]string)}
	for domain, values := range domains {
		if domain == "" {
			return nil, fmt.Errorf("empty dictionary domain")
		}
		table := make(map[uint16]string, len(values))
		seen := make(map[string]bool, len(values))
		for id, label := range values {
			if id == 0 || label == "" || seen[label] {
				return nil, fmt.Errorf("invalid or duplicate value in %s", domain)
			}
			table[id], seen[label] = label, true
		}
		d.domains[domain] = table
	}
	if err := d.ValidateReserved(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Dictionary) Name() string {
	if d == nil {
		return ""
	}
	return d.name
}
func (d *Dictionary) Resolve(domain string, s Symbol) any {
	if !s.literal && d != nil {
		if label, ok := d.domains[domain][s.id]; ok {
			return label
		}
	}
	return s.Raw() // Unknown references survive; never guess their meaning.
}

type Field struct {
	Name     string            `json:"name" cbor:"0,keyasint" pil:"0,keyasint"`
	Domain   string            `json:"domain,omitempty" cbor:"1,keyasint,omitempty" pil:"1,keyasint,omitempty"`
	Enums    map[uint16]string `json:"values,omitempty" cbor:"2,keyasint,omitempty" pil:"2,keyasint,omitempty"`
	Object   Schema            `json:"object,omitempty" cbor:"3,keyasint,omitempty" pil:"3,keyasint,omitempty"`
	Array    Schema            `json:"array,omitempty" cbor:"4,keyasint,omitempty" pil:"4,keyasint,omitempty"`
	Unit     string            `json:"unit,omitempty" cbor:"5,keyasint,omitempty" pil:"5,keyasint,omitempty"`
	Encoding string            `json:"encoding,omitempty" cbor:"6,keyasint,omitempty" pil:"6,keyasint,omitempty"`
}
type Schema map[Key]Field

var dec = func() cbor.DecMode {
	m, err := (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF,
		MaxNestedLevels: 32, MaxArrayElements: 65536, MaxMapPairs: 65536}).DecMode()
	if err != nil {
		panic(err)
	}
	return m
}()

// Encode uses native PIL objects or JSON text. CBOR is accepted only by Decode.
func Encode(record Record, schema Schema, dictionary *Dictionary, format Format) ([]byte, error) {
	switch format {
	case ReadableJSON:
		return json.Marshal(Expand(record, schema, dictionary))
	case CompactJSON:
		return json.Marshal(record)
	case PIL:
		return pil.MarshalObject(record)
	default:
		return nil, fmt.Errorf("unsupported format %d", format)
	}
}

// Decode accepts one compact record. Unknown keys and enum values are retained.
// Callers must limit input size before decoding untrusted data.
func Decode(data []byte, format Format) (Record, error) {
	var out Record
	switch format {
	case CompactJSON:
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid JSON record")
		}
		if err := d.Decode(&out); err != nil {
			return nil, err
		}
	case PIL:
		if err := pil.UnmarshalObject(data, &out); err != nil {
			return nil, err
		}
	case CBOR:
		if err := dec.Unmarshal(data, &out); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("decode requires compact JSON or CBOR")
	}
	if out == nil {
		return nil, fmt.Errorf("expected record")
	}
	return out, nil
}

func asRecord(v any) Record {
	switch v := v.(type) {
	case Record:
		return v
	case map[string]any:
		r := Record{}
		for k, value := range v {
			if n, err := strconv.ParseUint(k, 10, 16); err == nil {
				r[Key(n)] = value
			}
		}
		return r
	case map[any]any:
		r := Record{}
		for k, value := range v {
			if n, ok := number(k); ok {
				r[Key(n)] = value
			}
		}
		return r
	}
	return nil
}

func number(v any) (uint16, bool) {
	var n uint64
	switch v := v.(type) {
	case uint16:
		return v, true
	case uint64:
		n = v
	case int:
		if v < 0 {
			return 0, false
		}
		n = uint64(v)
	case json.Number:
		var err error
		n, err = strconv.ParseUint(string(v), 10, 16)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return uint16(n), n <= 65535
}

// Expand restores known names while retaining unknown fields under numeric names.
func Expand(record Record, schema Schema, dictionary *Dictionary) map[string]any {
	out := make(map[string]any, len(record))
	for key, value := range record {
		field, known := schema[key]
		name := field.Name
		if !known {
			name = strconv.Itoa(int(key))
		}
		if field.Encoding == "uuidv7" {
			switch id := value.(type) {
			case UUIDv7:
				value = id.String()
			case []byte:
				if len(id) == 16 {
					uuid := UUIDv7(id)
					if uuid.Valid() {
						value = uuid.String()
					}
				}
			}
		}
		if symbol, ok := value.(Symbol); ok {
			value = symbol.Raw()
		}
		if id, ok := number(value); ok {
			if label, exists := field.Enums[id]; exists {
				value = label
			} else if field.Domain != "" {
				value = dictionary.Resolve(field.Domain, Ref(id))
			}
		}
		if field.Object != nil {
			if r := asRecord(value); r != nil {
				value = Expand(r, field.Object, dictionary)
			}
		}
		if field.Array != nil {
			var items []any
			switch v := value.(type) {
			case []Record:
				for _, r := range v {
					items = append(items, Expand(r, field.Array, dictionary))
				}
			case []any:
				for _, r := range v {
					if record := asRecord(r); record != nil {
						items = append(items, Expand(record, field.Array, dictionary))
					} else {
						items = append(items, r)
					}
				}
			}
			if items != nil {
				value = items
			}
		}
		out[name] = jsonCompatible(value)
	}
	return out
}

func jsonCompatible(v any) any {
	switch v := v.(type) {
	case map[any]any:
		r := map[string]any{}
		for k, item := range v {
			r[fmt.Sprint(k)] = jsonCompatible(item)
		}
		return r
	case []any:
		r := make([]any, len(v))
		for i, item := range v {
			r[i] = jsonCompatible(item)
		}
		return r
	default:
		return v
	}
}

func (s Symbol) PILValue() any { return s.Raw() }
