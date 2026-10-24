package cache

import (
	"encoding/hex"
	"fmt"
	"maps"
	"strings"
)

// ReferenceTranslation is an explicit application-level reference-table change,
// carried in the immutable NUT binding. It does not alter PIL frame layout. The
// administrator asserts semantic equivalence of each source/target reference.
// Missing keys are errors, including zero; no numeric identity is inferred.
type ReferenceTranslation struct {
	Source string            `json:"source_definition" cbor:"source_definition"`
	Field  string            `json:"field" cbor:"field"`
	Values map[uint16]uint16 `json:"values" cbor:"values"`
}

func referenceType(kind string) bool { return kind == "REF16" || kind == "ARRAY<REF16>" }
func cloneReferences(input []ReferenceTranslation) []ReferenceTranslation {
	result := append([]ReferenceTranslation(nil), input...)
	for i := range result {
		result[i].Values = maps.Clone(result[i].Values)
	}
	return result
}
func validateReferences(d Definition, fields map[string]int) error {
	seen := map[string]bool{}
	if len(d.Resource.References) > 65536 {
		return fmt.Errorf("too many reference translations")
	}
	for _, table := range d.Resource.References {
		digest, err := hex.DecodeString(table.Source)
		if err != nil || len(digest) != 32 || table.Source != strings.ToLower(table.Source) {
			return fmt.Errorf("reference translation requires a source definition digest")
		}
		i, exists := fields[table.Field]
		if !exists || !referenceType(d.Fields[i].Type) {
			return fmt.Errorf("reference translation requires a REF16 field")
		}
		key := table.Source + "/" + table.Field
		if seen[key] || len(table.Values) == 0 {
			return fmt.Errorf("duplicate or empty reference translation")
		}
		seen[key] = true
	}
	return nil
}

func referenceValues(source, target *Layout, field string) map[uint16][]byte {
	digest := fmt.Sprintf("%x", source.Digest)
	for _, table := range target.Definition.Resource.References {
		if table.Source != digest || table.Field != field {
			continue
		}
		values := make(map[uint16][]byte, len(table.Values))
		// One immutable backing allocation, reused for every scalar projection.
		backing := make([]byte, len(table.Values)*2)
		at := 0
		for from, to := range table.Values {
			order.PutUint16(backing[at:], to)
			values[from] = backing[at : at+2 : at+2]
			at += 2
		}
		return values
	}
	return nil
}

func translateReference(kind string, value Value, table map[uint16][]byte) (Value, error) {
	if value.State == Null || value.State == Absent {
		return value, nil
	}
	if kind == "REF16" {
		if value.State != Direct || len(value.Bytes) != 2 {
			return Value{}, fmt.Errorf("invalid REF16 value")
		}
		n := order.Uint16(value.Bytes)
		mapped, ok := table[n]
		if !ok {
			return Value{}, fmt.Errorf("unmapped REF16 value %d", n)
		}
		return Value{Direct, mapped}, nil
	}
	if value.State != Offset {
		return Value{}, fmt.Errorf("invalid REF16 array")
	}
	if err := validateArray(kind, value.Bytes); err != nil {
		return Value{}, err
	}
	at := 0
	_, _ = readExtended(value.Bytes, &at)
	out := append([]byte(nil), value.Bytes...)
	for i := at; i < len(out); i += 2 {
		n := order.Uint16(out[i:])
		mapped, ok := table[n]
		if !ok {
			return Value{}, fmt.Errorf("unmapped REF16 value %d", n)
		}
		copy(out[i:i+2], mapped)
	}
	return Value{Offset, out}, nil
}
