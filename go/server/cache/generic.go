package cache

// The blind receiver keeps incoming CBOR intact. It does not infer a permanent
// schema from an example, nor put arbitrary attributes into the shared trie.
import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"trie-trunk/go/codec"
)

var genericLayout, genericLayoutError = compileGenericDefinition()

// GenericDefinition is the built-in blind-registration definition. The optional
// source_definition digest binds numeric CBOR keys/values to the sender's map.
func GenericDefinition() (*Layout, error) {
	if genericLayoutError != nil {
		return nil, genericLayoutError
	}
	return ParseDefinition(genericLayout.Bytes)
}
func compileGenericDefinition() (*Layout, error) {
	return Compile(Definition{Identity: "default/logs", Version: 1 << 20,
		Fields:   []Field{{Name: "event_id", Type: "UUID"}, {Name: "payload", Type: "CBOR"}, {Name: "source_definition", Type: "BYTES"}},
		Resource: Binding{Format: Format, Service: "default", Resource: "logs", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}},
	})
}
func (s *Store) generic() bool {
	return genericLayoutError == nil && genericLayout.Digest == s.layout.Digest
}

// Retained only to inspect previously written compatibility records.
func (s *Store) sourcePath(digest [32]byte) string {
	return filepath.Join(s.dir, fmt.Sprintf("source-%x.map", digest))
}

func (s *Store) wrapCBOR(raw, identity, source []byte) ([]byte, error) {
	var id codec.UUIDv7
	if identity == nil {
		var e error
		id, e = codec.NewUUIDv7(s.config.Now())
		if e != nil {
			return nil, e
		}
	} else {
		var b []byte
		if e := decoder.Unmarshal(identity, &b); e != nil {
			var text string
			if e = decoder.Unmarshal(identity, &text); e != nil {
				return nil, fmt.Errorf("event_id must be a UUIDv7")
			}
			if len(text) != 36 || text[8] != '-' || text[13] != '-' || text[18] != '-' || text[23] != '-' {
				return nil, fmt.Errorf("invalid event_id")
			}
			b, e = hex.DecodeString(strings.ReplaceAll(text, "-", ""))
			if e != nil {
				return nil, e
			}
		}
		if len(b) != 16 {
			return nil, fmt.Errorf("invalid event_id")
		}
		copy(id[:], b)
		if !id.Valid() {
			return nil, fmt.Errorf("event_id must be UUIDv7")
		}
	}
	values := []Value{{State: Direct, Bytes: id[:]}, {State: Offset, Bytes: raw}, {State: Absent}}
	if source != nil {
		values[2] = Value{State: Offset, Bytes: source}
	}
	return s.layout.Encode(nil, values)
}

// Layout and content-addressed producer maps are immutable during inspection.
func (s *Store) inspectRejection(raw []byte) (map[string]any, error) {
	fields, e := s.layout.inspect(raw)
	if e != nil || !s.generic() {
		return fields, e
	}
	var f Frame
	if e = s.layout.Scan(raw, &f); e != nil {
		return nil, e
	}
	if f.Values[2].State != Offset || len(f.Values[2].Bytes) != 32 {
		return fields, nil
	}
	var digest [32]byte
	copy(digest[:], f.Values[2].Bytes)
	data, e := os.ReadFile(s.sourcePath(digest))
	if e != nil {
		return nil, e
	}
	if sha256.Sum256(data) != digest {
		return nil, fmt.Errorf("source map digest mismatch")
	}
	var d codec.Definition
	if e = decoder.Unmarshal(data, &d); e != nil {
		return nil, e
	}
	dictionary, e := codec.NewDictionary("source", d.Domains)
	if e != nil {
		return nil, e
	}
	record, e := codec.Decode(f.Values[1].Bytes, codec.CBOR)
	if e != nil {
		return nil, e
	}
	fields["payload"] = codec.Expand(record, d.Fields, dictionary)
	return fields, nil
}
