package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"github.com/pelletier/go-toml/v2"
)

// TransformConfig is explicit and resource-scoped. No name inference or implicit
// coercion occurs. An input Definition remains immutable; output uses the target.
type TransformConfig struct {
	Target string          `toml:"target"`
	Steps  []TransformStep `toml:"steps"`
}
type TransformStep struct {
	Op        string            `toml:"op"`
	From      string            `toml:"from"`
	To        string            `toml:"to"`
	Fields    []string          `toml:"fields"`
	Separator string            `toml:"separator"`
	Value     any               `toml:"value"`
	Type      string            `toml:"type"`
	Layout    string            `toml:"layout"`
	Values    map[string]string `toml:"values"`
}

func (s *Store) loadTransforms() error {
	path := filepath.Join(s.dir, "transforms.cfg")
	raw, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if len(raw) > 1<<20 {
		return fmt.Errorf("transforms.cfg exceeds 1 MiB")
	}
	var c TransformConfig
	if e = toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&c); e != nil {
		return e
	}
	if c.Target != fmtDigestNative(s.layout.Digest) {
		return fmt.Errorf("transform target must match exact Definition digest")
	}
	for _, step := range c.Steps {
		switch step.Op {
		case "rename", "copy", "drop", "set", "default", "concat", "convert", "map":
		default:
			return fmt.Errorf("unknown transform %q", step.Op)
		}
		if step.To == "@fields" || step.From == "@fields" {
			return fmt.Errorf("cannot transform the reserved extension container")
		}
		if step.Op != "drop" && step.To == "" {
			return fmt.Errorf("transform target field required")
		}
		if step.Op == "rename" || step.Op == "copy" || step.Op == "drop" || step.Op == "convert" || step.Op == "map" {
			if step.From == "" {
				return fmt.Errorf("transform source field required")
			}
		}
		if step.To == s.layout.Definition.Resource.Roles.Event || step.From == s.layout.Definition.Resource.Roles.Event {
			return fmt.Errorf("event_id cannot be transformed")
		}
		if step.Op != "drop" && fieldIndex(s.layout, step.To) < 0 && fieldIndex(s.layout, "@fields") < 0 {
			return fmt.Errorf("undefined transform target %s", step.To)
		}
		if step.Op == "convert" && step.Type != "string" && step.Type != "uint" && step.Type != "int" && step.Type != "float" && step.Type != "bool" && step.Type != "timestamp_ms" {
			return fmt.Errorf("unsupported explicit conversion")
		}
	}
	s.transforms = &c
	return nil
}
func (s *Store) transform(values []Value) ([]byte, error) {
	fields := map[string]pil.RawObject{}
	for i, v := range values {
		if v.State == Absent {
			continue
		}
		f := s.layout.Definition.Fields[i]
		if f.Name == "@fields" {
			if v.State == Null {
				return nil, fmt.Errorf("@fields must be a map")
			}
			var ext map[string]pil.RawObject
			if e := decoder.Unmarshal(v.Bytes, &ext); e != nil {
				return nil, e
			}
			for k, v := range ext {
				fields[k] = v
			}
			continue
		}
		raw, e := fieldObject(f, v)
		if e != nil {
			return nil, e
		}
		fields[f.Name] = raw
	}
	put := func(key string, value any) error {
		raw, e := encoder.Marshal(value)
		if e == nil {
			fields[key] = raw
		}
		return e
	}
	for _, step := range s.transforms.Steps {
		raw, present := fields[step.From]
		switch step.Op {
		case "rename", "copy":
			if !present {
				continue
			}
			if _, exists := fields[step.To]; exists && step.To != step.From {
				return nil, fmt.Errorf("transform collision: %s", step.To)
			}
			fields[step.To] = raw
			if step.Op == "rename" && step.From != step.To {
				delete(fields, step.From)
			}
		case "drop":
			delete(fields, step.From)
		case "set", "default":
			if _, exists := fields[step.To]; exists && step.Op == "default" {
				continue
			}
			if e := put(step.To, step.Value); e != nil {
				return nil, e
			}
		case "concat":
			parts := make([]string, len(step.Fields))
			for i, k := range step.Fields {
				v, ok := fields[k]
				if !ok {
					return nil, fmt.Errorf("missing concat field %s", k)
				}
				if e := decoder.Unmarshal(v, &parts[i]); e != nil {
					return nil, fmt.Errorf("concat requires strings: %s", k)
				}
			}
			if e := put(step.To, strings.Join(parts, step.Separator)); e != nil {
				return nil, e
			}
		case "map":
			if !present {
				continue
			}
			var v string
			if e := decoder.Unmarshal(raw, &v); e != nil {
				return nil, e
			}
			mapped, ok := step.Values[v]
			if !ok {
				return nil, fmt.Errorf("unmapped transform value %q", v)
			}
			if e := put(step.To, mapped); e != nil {
				return nil, e
			}
		case "convert":
			if !present {
				continue
			}
			var value any
			if e := decoder.Unmarshal(raw, &value); e != nil {
				return nil, e
			}
			text := fmt.Sprint(value)
			var out any
			var e error
			switch step.Type {
			case "string":
				switch value.(type) {
				case string, int64, uint64, float64, bool:
					out = text
				default:
					e = fmt.Errorf("string conversion requires scalar")
				}
			case "uint":
				out, e = strconv.ParseUint(text, 10, 64)
			case "int":
				out, e = strconv.ParseInt(text, 10, 64)
			case "float":
				out, e = strconv.ParseFloat(text, 64)
			case "bool":
				out, e = strconv.ParseBool(text)
			case "timestamp_ms":
				var at time.Time
				at, e = time.Parse(step.Layout, text)
				out = at.UnixMilli()
			}
			if e != nil {
				return nil, e
			}
			if e = put(step.To, out); e != nil {
				return nil, e
			}
		}
	}
	out := make([]Value, len(values))
	ext := map[string]pil.RawObject{}
	for k, v := range fields {
		i := fieldIndex(s.layout, k)
		if i < 0 {
			ext[k] = v
			continue
		}
		if k == "@fields" {
			return nil, fmt.Errorf("transform cannot assign reserved @fields")
		}
		value, e := s.layout.ValueFromObject(i, v)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", k, e)
		}
		out[i] = value
	}
	if len(ext) > 0 {
		i := fieldIndex(s.layout, "@fields")
		if i < 0 {
			return nil, fmt.Errorf("undefined fields forbidden")
		}
		raw, e := encoder.Marshal(ext)
		if e != nil {
			return nil, e
		}
		out[i] = Value{Offset, raw}
	}
	return s.layout.Encode(nil, out)
}
