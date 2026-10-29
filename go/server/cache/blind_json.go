package cache

import (
	"encoding/json"
	"fmt"
)

// Decode at the transport edge without silently replacing duplicate properties
// or rounding JSON numbers before they reach PIL's exact integer representation.
func blindJSON(d *json.Decoder, depth int, budget *int) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	if depth > 32 || *budget <= 0 {
		return nil, fmt.Errorf("JSON nesting or value limit")
	}
	*budget--
	switch token {
	case json.Delim('{'):
		out := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("invalid JSON key")
			}
			if _, ok := out[name]; ok {
				return nil, fmt.Errorf("duplicate JSON key")
			}
			value, err := blindJSON(d, depth+1, budget)
			if err != nil {
				return nil, err
			}
			out[name] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("invalid JSON object")
		}
		return out, nil
	case json.Delim('['):
		out := []any{}
		for d.More() {
			value, err := blindJSON(d, depth+1, budget)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("invalid JSON array")
		}
		return out, nil
	default:
		switch token.(type) {
		case nil, string, json.Number, bool:
			return token, nil
		}
		return nil, fmt.Errorf("invalid JSON value")
	}
}
