package cache

// Blind formats are decoded at ingress. Only native PIL objects reach storage.
import (
	"encoding/hex"
	"fmt"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"github.com/fxamacker/cbor/v2"
	"strings"
	"trie-trunk/go/codec"
)

var ingressDecoder, _ = (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, TagsMd: cbor.TagsForbidden, MaxNestedLevels: 32, MaxArrayElements: 65536, MaxMapPairs: 65536}).DecMode()

var genericLayout, genericLayoutError = compileGenericDefinition()

// GenericDefinition is the built-in blind-registration definition. Incoming
// values are represented by native PIL objects with text or integer keys.
func GenericDefinition() (*Layout, error) {
	if genericLayoutError != nil {
		return nil, genericLayoutError
	}
	return ParseDefinition(genericLayout.Bytes)
}
func compileGenericDefinition() (*Layout, error) {
	return Compile(Definition{Identity: "default/logs", Version: 1 << 20,
		Fields:   []Field{{Name: "event_id", Type: "UUID"}, {Name: "payload", Type: "OBJECT"}, {Name: "source_definition", Type: "BYTES"}},
		Resource: Binding{Format: Format, Service: "default", Resource: "logs", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}},
	})
}
func (s *Store) generic() bool {
	return genericLayoutError == nil && genericLayout.Digest == s.layout.Digest
}

func (s *Store) wrapBlind(value any) ([]byte, error) {
	var identity any
	switch m := value.(type) {
	case map[string]any:
		identity = m["event_id"]
	case map[any]any:
		identity = m["event_id"]
	}
	var id codec.UUIDv7
	if identity == nil {
		var err error
		id, err = codec.NewUUIDv7(s.config.Now())
		if err != nil {
			return nil, err
		}
	} else {
		var b []byte
		switch v := identity.(type) {
		case []byte:
			b = v
		case string:
			if len(v) != 36 || v[8] != '-' || v[13] != '-' || v[18] != '-' || v[23] != '-' {
				return nil, fmt.Errorf("invalid event_id")
			}
			var err error
			b, err = hex.DecodeString(strings.ReplaceAll(v, "-", ""))
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("event_id must be UUIDv7")
		}
		if len(b) != 16 {
			return nil, fmt.Errorf("invalid event_id")
		}
		copy(id[:], b)
		if !id.Valid() {
			return nil, fmt.Errorf("event_id must be UUIDv7")
		}
	}
	payload, err := pil.MarshalObject(value)
	if err != nil {
		return nil, err
	}
	return s.wrapObject(payload, id, nil)
}

func (s *Store) wrapObject(raw []byte, id codec.UUIDv7, source []byte) ([]byte, error) {
	values := []Value{{State: Direct, Bytes: id[:]}, {State: Offset, Bytes: raw}, {State: Absent}}
	if source != nil {
		values[2] = Value{State: Offset, Bytes: source}
	}
	return s.layout.Encode(nil, values)
}

func (s *Store) inspectRejection(raw []byte) (map[string]any, error) { return s.layout.inspect(raw) }

// The blind payload may repeat its promoted event_id. Remove that top-level
// identity from accepted storage too; rejection handling retains the input frame.
func (s *Store) stripBlindEventID(values []Value) error {
	if !s.generic() || values[1].State != Offset {
		return nil
	}
	var payload any
	if err := pil.UnmarshalObject(values[1].Bytes, &payload); err != nil {
		return err
	}
	switch m := payload.(type) {
	case map[string]any:
		if _, ok := m["event_id"]; !ok {
			return nil
		}
		delete(m, "event_id")
	case map[any]any:
		if _, ok := m["event_id"]; !ok {
			return nil
		}
		delete(m, "event_id")
	default:
		return nil
	}
	raw, err := pil.MarshalObject(payload)
	if err != nil {
		return err
	}
	values[1] = Value{State: Offset, Bytes: raw}
	return nil
}
