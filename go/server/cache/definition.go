// Package cache implements the collection-native storage contract. Legacy store
// formats remain in package store and are never reinterpreted in place.
package cache

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fxamacker/cbor/v2"
)

const Format uint32 = 6
const MaxFrame = 16 << 20

type Branch uint8

const (
	Read Branch = iota
	Write
	Error
	Debug
	Trace
)

func (b Branch) Valid() bool { return b <= Trace }
func (b Branch) Prefix() string {
	if !b.Valid() {
		return ""
	}
	return [...]string{"read", "write", "error", "debug", "trace"}[b]
}
func (b Branch) Name() string {
	if !b.Valid() {
		return ""
	}
	return [...]string{"read", "write", "error", "debug", "trace"}[b]
}
func (b Branch) Aggregates() bool { return b <= Error }
func ParseBranch(s string) (Branch, error) {
	for b := Read; b <= Trace; b++ {
		if s == b.Name() {
			return b, nil
		}
	}
	return 0, fmt.Errorf("unknown branch %q", s)
}

type Field struct {
	Name   string   `json:"name" cbor:"name"`
	Type   string   `json:"type" cbor:"type"`
	Values []string `json:"values,omitempty" cbor:"values,omitempty"`
}
type Roles struct {
	Shared   string `json:"shared,omitempty" cbor:"shared,omitempty"`
	Event    string `json:"event_id" cbor:"event_id"`
	Object   string `json:"id,omitempty" cbor:"id,omitempty"`
	Revision string `json:"revision_id,omitempty" cbor:"revision_id,omitempty"`
	Duration string `json:"duration,omitempty" cbor:"duration,omitempty"`
}
type Binding struct {
	Format       uint32                 `json:"format" cbor:"format"`
	Service      string                 `json:"service" cbor:"service"`
	Resource     string                 `json:"resource" cbor:"resource"`
	Major        uint32                 `json:"major" cbor:"major"`
	Minor        uint32                 `json:"minor" cbor:"minor"`
	Timezone     string                 `json:"timezone" cbor:"timezone"`
	Roles        Roles                  `json:"roles" cbor:"roles"`
	Path         []string               `json:"path,omitempty" cbor:"path,omitempty"`
	Aggregations []string               `json:"aggregations,omitempty" cbor:"aggregations,omitempty"`
	References   []ReferenceTranslation `json:"references,omitempty" cbor:"references,omitempty"`
}

// The standard PIL definition is also the storage map. Its resource section supplies
// storage roles and projections; PIL clients can read the same definition bytes.
type Definition struct {
	PIL              uint32  `json:"pil" cbor:"pil"`
	AdaptiveIntegers string  `json:"adaptive_integers,omitempty" cbor:"adaptive_integers,omitempty"`
	Identity         string  `json:"definition" cbor:"definition"`
	Version          uint64  `json:"version" cbor:"version"`
	Fields           []Field `json:"fields" cbor:"fields"`
	Resource         Binding `json:"resource" cbor:"resource"`
}
type Layout struct {
	statistics                                *statisticsLayout
	integers                                  *integerLayout
	Definition                                Definition
	Bytes                                     []byte
	Digest                                    [32]byte
	Widths                                    []int
	Event, Object, Revision, Duration, Shared int
	Path                                      []int
}

var encoder, _ = cbor.CoreDetEncOptions().EncMode()
var decoder, _ = (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden, TagsMd: cbor.TagsForbidden, MaxNestedLevels: 32, MaxArrayElements: 65536, MaxMapPairs: 65536}).DecMode()

func name(s string) bool {
	if len(s) == 0 || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for i, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_' || c == '.') {
			continue
		}
		return false
	}
	return true
}
func width(kind string) (int, error) {
	switch kind {
	case "BOOL", "I8", "U8":
		return 1, nil
	case "I16", "U16", "REF16":
		return 2, nil
	case "I32", "U32", "F32":
		return 4, nil
	case "I64", "U64", "F64", "TIMESTAMP_MS", "TIMESTAMP_US", "TIMESTAMP_NS":
		return 8, nil
	case "UUID":
		return 16, nil
	case "STRING", "BYTES", "CBOR":
		return 0, nil
	}
	if strings.HasPrefix(kind, "ARRAY<") && strings.HasSuffix(kind, ">") {
		element := kind[6 : len(kind)-1]
		if strings.HasPrefix(element, "ARRAY<") {
			return 0, fmt.Errorf("nested arrays unsupported")
		}
		_, err := width(element)
		return 0, err
	}
	return 0, fmt.Errorf("unsupported PIL type %q", kind)
}
func Compile(d Definition) (*Layout, error) {
	// Detach the compiled map from a caller's mutable definition slices.
	d.Fields = append([]Field(nil), d.Fields...)
	for i := range d.Fields {
		d.Fields[i].Values = append([]string(nil), d.Fields[i].Values...)
	}
	d.Resource.Path = append([]string(nil), d.Resource.Path...)
	d.Resource.Aggregations = append([]string(nil), d.Resource.Aggregations...)
	d.Resource.References = cloneReferences(d.Resource.References)
	n := d.Resource
	adaptive := d.AdaptiveIntegers != "" && d.AdaptiveIntegers != "none"
	if (d.PIL != 0 && d.PIL != 1) || (d.PIL == 1) != adaptive {
		return nil, fmt.Errorf("adaptive integers require PIL definition version 1; fixed layout requires 0")
	}
	if n.Format != Format || !name(n.Service) || !name(n.Resource) || n.Major == 0 || n.Major >= 1<<12 || n.Minor >= 1<<20 || n.Timezone == "" {
		return nil, fmt.Errorf("invalid cache definition binding")
	}
	if _, err := time.LoadLocation(n.Timezone); err != nil {
		return nil, fmt.Errorf("invalid timezone: %w", err)
	}
	if d.Identity != n.Service+"/"+n.Resource || d.Version != uint64(n.Major)<<20|uint64(n.Minor) {
		return nil, fmt.Errorf("definition identity/version disagrees with binding")
	}
	if len(d.Fields) == 0 || len(d.Fields) > 256 {
		return nil, fmt.Errorf("invalid definition field count")
	}
	l := &Layout{Definition: d, Object: -1, Revision: -1, Duration: -1, Shared: -1}
	byName := map[string]int{}
	for i, f := range d.Fields {
		if f.Name == "" || !utf8.ValidString(f.Name) {
			return nil, fmt.Errorf("invalid field name")
		}
		if _, ok := byName[f.Name]; ok {
			return nil, fmt.Errorf("duplicate field %q", f.Name)
		}
		byName[f.Name] = i
		if strings.HasPrefix(f.Name, "@") && (f.Name != "@fields" || f.Type != "CBOR") {
			return nil, fmt.Errorf("unsupported reserved field")
		}
		w, err := width(f.Type)
		if err != nil {
			return nil, err
		}
		if len(f.Values) > 0 {
			if len(f.Values) > 65536 {
				return nil, fmt.Errorf("dictionary exceeds prototype map limit")
			}
			if f.Type != "STRING" {
				return nil, fmt.Errorf("only STRING may have a dictionary")
			}
			seen := map[string]bool{}
			for _, v := range f.Values {
				if !utf8.ValidString(v) || seen[v] {
					return nil, fmt.Errorf("invalid or duplicate dictionary value")
				}
				seen[v] = true
			}
			w = 1
			if len(f.Values) > 256 {
				w = 2
			}
			if uint64(len(f.Values)) > 65536 {
				w = 4
			}
		}
		l.Widths = append(l.Widths, w)
	}
	if err := validateReferences(d, byName); err != nil {
		return nil, err
	}
	role := func(s string, required bool) (int, error) {
		if s == "" && !required {
			return -1, nil
		}
		i, ok := byName[s]
		if !ok {
			return -1, fmt.Errorf("unknown role field %q", s)
		}
		return i, nil
	}
	var err error
	if l.Event, err = role(n.Roles.Event, true); err != nil {
		return nil, err
	}
	if d.Fields[l.Event].Type != "UUID" {
		return nil, fmt.Errorf("event_id role must be UUID")
	}
	if l.Object, err = role(n.Roles.Object, false); err != nil {
		return nil, err
	}
	if l.Revision, err = role(n.Roles.Revision, false); err != nil {
		return nil, err
	}
	if l.Duration, err = role(n.Roles.Duration, false); err != nil {
		return nil, err
	}
	if l.Shared, err = role(n.Roles.Shared, false); err != nil {
		return nil, err
	}
	if l.Shared >= 0 && d.Fields[l.Shared].Type != "REF16" {
		return nil, fmt.Errorf("shared role must be REF16")
	}
	roles := map[int]bool{}
	for _, i := range []int{l.Event, l.Object, l.Revision, l.Duration, l.Shared} {
		if i < 0 {
			continue
		}
		if roles[i] {
			return nil, fmt.Errorf("role fields must be distinct")
		}
		roles[i] = true
	}
	for _, i := range []int{l.Object, l.Revision} {
		if i >= 0 {
			switch d.Fields[i].Type {
			case "UUID", "U8", "U16", "U32", "U64", "STRING", "BYTES":
			default:
				return nil, fmt.Errorf("unsupported identity type")
			}
			if len(d.Fields[i].Values) > 0 {
				return nil, fmt.Errorf("identity fields cannot be mapped")
			}
		}
	}
	if l.Duration >= 0 {
		switch d.Fields[l.Duration].Type {
		case "U8", "U16", "U32", "U64":
		default:
			return nil, fmt.Errorf("duration role must be an unsigned integer in microseconds")
		}
		if len(d.Fields[l.Duration].Values) > 0 {
			return nil, fmt.Errorf("duration role cannot be mapped")
		}
	}
	wanted := []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}
	if len(n.Aggregations) > 0 {
		if l.Duration < 0 || len(n.Aggregations) != len(wanted) {
			return nil, fmt.Errorf("unsupported aggregations")
		}
		for i, a := range n.Aggregations {
			if a != wanted[i] {
				return nil, fmt.Errorf("unsupported aggregations")
			}
		}
	}
	seen := map[int]bool{}
	for _, s := range n.Path {
		i, ok := byName[s]
		if !ok || roles[i] || seen[i] || l.Widths[i] == 0 {
			return nil, fmt.Errorf("invalid shared path field %q", s)
		}
		seen[i] = true
		l.Path = append(l.Path, i)
	}
	l.integers, err = compileIntegers(l)
	if err != nil {
		return nil, err
	}
	l.Bytes, err = encoder.Marshal(d)
	if err != nil {
		return nil, err
	}
	if len(l.Bytes) > MaxFrame {
		return nil, fmt.Errorf("map too large")
	}
	l.Digest = sha256.Sum256(l.Bytes)
	l.statistics = compileStatistics(l)
	return l, nil
}
func ParseDefinition(raw []byte) (*Layout, error) {
	if len(raw) > MaxFrame {
		return nil, fmt.Errorf("map too large")
	}
	var d Definition
	if err := decoder.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	l, err := Compile(d)
	if err != nil {
		return nil, err
	}
	l.Bytes = append([]byte(nil), raw...)
	l.Digest = sha256.Sum256(raw)
	return l, nil
}
