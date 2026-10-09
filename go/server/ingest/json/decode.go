package json

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

const (
	jsonId codec.Key = iota
	jsonDuration
	jsonFields
)

func (h *Handler) revision() (codec.Revision, error) {
	h.once.Do(func() {
		h.rev, h.err = h.Store.Register(codec.Definition{
			Dictionary: Protocol,
			Timezone:   "UTC",
			Fields: codec.Schema{
				jsonId:       {Name: "id", Encoding: "uuidv7"},
				jsonDuration: {Name: "duration", Unit: "us"},
				jsonFields:   {Name: "fields"},
			},
			Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"},
		})
	})
	return h.rev, h.err
}

func readJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, store.ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := make(map[string]any)
		for d.More() {
			if len(object) >= 256 {
				return nil, store.ErrInvalid
			}
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, store.ErrInvalid
			}
			if _, exists := object[name]; exists {
				return nil, store.ErrInvalid
			}
			value, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, store.ErrInvalid
		}
		return object, nil
	case json.Delim('['):
		values := make([]any, 0)
		for d.More() {
			if len(values) >= 1024 {
				return nil, store.ErrInvalid
			}
			value, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return nil, store.ErrInvalid
		}
		return values, nil
	default:
		if n, ok := token.(json.Number); ok {
			text := string(n)
			if strings.ContainsAny(text, ".eE") {
				return strconv.ParseFloat(text, 64)
			}
			if strings.HasPrefix(text, "-") {
				return strconv.ParseInt(text, 10, 64)
			}
			return strconv.ParseUint(text, 10, 64)
		}
		return token, nil
	}
}

func jsonUUID(value any) (codec.UUIDv7, bool) {
	var id codec.UUIDv7
	text, ok := value.(string)
	if !ok || len(text) != 36 || text[8] != '-' || text[13] != '-' || text[18] != '-' || text[23] != '-' {
		return id, false
	}
	_, err := hex.Decode(id[:], []byte(text[:8]+text[9:13]+text[14:18]+text[19:23]+text[24:]))
	return id, err == nil && id.Valid()
}

func encodeJSON(line []byte) ([]byte, error) {
	if len(line) == 0 || len(line) > codec.MaxFramePayload || !utf8.Valid(line) {
		return nil, store.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(line))
	d.UseNumber()
	value, err := readJSON(d, 1)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed JSON record", store.ErrInvalid)
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, store.ErrInvalid
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, store.ErrInvalid
	}
	id, haveId := jsonUUID(fields["id"])
	if haveId {
		delete(fields, "id")
	} else {
		id, err = codec.NewUUIDv7(time.Now())
		if err != nil {
			return nil, err
		}
	}
	var duration uint64
	if n, ok := fields["duration"].(uint64); ok {
		duration = n
		delete(fields, "duration")
	}
	return codec.MarshalCompact(codec.Record{jsonId: id, jsonDuration: duration, jsonFields: fields}, codec.CBOR)
}
