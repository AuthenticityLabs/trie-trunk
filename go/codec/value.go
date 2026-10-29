package codec

import (
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"
)

// Value is immutable extension data. Constructors copy containers; values cannot
// contain arbitrary Go objects, pointers or cycles. The zero value is null.
type Value struct{ data any }

func String(v string) Value { return Value{v} }
func Int(v int64) Value     { return Value{v} }
func Uint(v uint64) Value   { return Value{v} }
func Bool(v bool) Value     { return Value{v} }

// Float rejects values unsupported by JSON. Non-finite numbers become null.
func Float(v float64) Value {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return Value{}
	}
	return Value{v}
}
func List(values ...Value) Value {
	items := make([]Value, len(values))
	copy(items, values)
	return Value{items}
}
func Object(values map[string]Value) Value {
	r := make(map[string]Value, len(values))
	for k, v := range values {
		r[k] = v
	}
	return Value{r}
}
func (v Value) MarshalJSON() ([]byte, error) { return json.Marshal(v.data) }

// LimitText limits UTF-8 bytes without splitting a code point.
func LimitText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return strings.Clone(s)
}

// Sanitize bounds nested extension data and masks sensitive keys at every level.
func (v Value) Sanitize(maxText, maxItems, depth int, sensitive map[string]bool) Value {
	if depth <= 0 {
		return String("[truncated]")
	}
	switch data := v.data.(type) {
	case string:
		return String(LimitText(data, maxText))
	case []Value:
		if len(data) > maxItems {
			data = data[:maxItems]
		}
		r := make([]Value, len(data))
		for i, item := range data {
			r[i] = item.Sanitize(maxText, maxItems, depth-1, sensitive)
		}
		return Value{r} // Already privately owned; do not copy again.
	case map[string]Value:
		// Reject an oversized object as a whole; sorting an unbounded key set
		// just to choose a few survivors defeats the memory/CPU limit.
		if len(data) > maxItems {
			return String("[truncated]")
		}
		r := make(map[string]Value)
		for k, item := range data {
			if maxText > 0 && len(k) > maxText {
				continue
			}
			if sensitive[strings.ToLower(k)] {
				r[k] = String("*******")
			} else {
				r[k] = item.Sanitize(maxText, maxItems, depth-1, sensitive)
			}
		}
		return Value{r} // Already privately owned; do not copy again.
	default:
		return v
	}
}

func (v Value) PILValue() any { return v.data }
