package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
	"trie-trunk/go/codec"
)

var (
	ErrConflict = errors.New("immutable schema or aggregate already has different content")
	ErrLimit    = errors.New("storage capacity limit reached")
	ErrClosed   = errors.New("store closed")
	ErrInvalid  = errors.New("invalid log record")
)
var diskEncoder, _ = cbor.CoreDetEncOptions().EncMode()
var decoder, _ = (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, MaxNestedLevels: 16, MaxArrayElements: 1024, MaxMapPairs: 256, TagsMd: cbor.TagsForbidden, IndefLength: cbor.IndefLengthForbidden}).DecMode()

type token struct {
	Key   codec.Key
	Value []byte
}

type record struct {
	Id       codec.UUIDv7
	Duration uint64
	Path     []token
	Dynamic  []byte
	Leaf     uint32
	Prepared bool
}

type schema struct {
	hasDuration  bool
	aggregates   bool
	statistics   *trieStatistics
	definition   codec.Definition
	revision     codec.Revision
	id, duration codec.Key
	path         []codec.Key
	trie         *trie
	location     *time.Location
	boundary     codec.Key
	hasBoundary  bool
}

func compile(d codec.Definition, rev codec.Revision) (*schema, error) {
	if d.Timezone == "" || d.Storage == nil && d.Timezone != "UTC" {
		return nil, fmt.Errorf("schema requires an explicit supported timezone")
	}
	if d.Object != "" && (d.Storage == nil || (d.Storage.Format != 3 && d.Storage.Format != 4)) {
		return nil, fmt.Errorf("named objects require events storage format 3 or 4")
	}
	loc, err := loadLocation(d.Timezone)
	if err != nil {
		return nil, err
	}
	if d.Storage != nil && ((d.Storage.Format != 3 && d.Storage.Format != 4) || d.Storage.Bucket != "hour" && d.Storage.Bucket != "day" || d.Storage.Boundary == "") {
		return nil, fmt.Errorf("unsupported storage policy")
	}
	s := &schema{definition: d, revision: rev, location: loc}
	haveId, haveDuration := false, false
	for k, f := range d.Fields {
		if f.Name == "id" {
			if haveId || f.Encoding != "uuidv7" {
				return nil, ErrInvalid
			}
			s.id = k
			haveId = true
		}
		if f.Name == "duration" {
			if haveDuration || f.Unit != "us" {
				return nil, ErrInvalid
			}
			s.duration = k
			haveDuration = true
		}
	}
	if !haveId || !haveDuration && d.Object == "" {
		return nil, fmt.Errorf("schema requires uuidv7 id and duration in us")
	}
	if len(d.Path) > 32 {
		return nil, ErrLimit
	}
	seen := map[codec.Key]bool{}
	for _, k := range d.Path {
		if _, ok := d.Fields[k]; !ok || seen[k] || k == s.id || haveDuration && k == s.duration {
			return nil, fmt.Errorf("invalid trie path field")
		}
		seen[k] = true
		s.path = append(s.path, k)
		if d.Storage != nil && d.Fields[k].Name == d.Storage.Boundary {
			if s.hasBoundary {
				return nil, fmt.Errorf("ambiguous partition boundary")
			}
			s.boundary, s.hasBoundary = k, true
		}
	}
	wanted := []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}
	if len(d.Aggregations) != len(wanted) && (d.Object == "" || len(d.Aggregations) != 0) || len(d.Aggregations) > 0 && !haveDuration {
		return nil, fmt.Errorf("unsupported published aggregations")
	}
	for i, v := range d.Aggregations {
		if wanted[i] != v {
			return nil, fmt.Errorf("unsupported published aggregations")
		}
	}
	s.hasDuration, s.aggregates = haveDuration, len(d.Aggregations) > 0
	if d.Object != "" {
		s.statistics = newTrieStatistics()
	}
	return s, nil
}

func (s *schema) decode(data []byte) (record, error) {
	var out record
	if len(data) == 0 || len(data) > codec.MaxFramePayload {
		return out, codec.ErrFrameSize
	}
	var fields map[codec.Key]cbor.RawMessage
	if err := decoder.Unmarshal(data, &fields); err != nil {
		return out, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(fields) == 0 || len(fields) > 64 {
		return out, ErrInvalid
	}
	var id []byte
	if err := decoder.Unmarshal(fields[s.id], &id); err != nil || len(id) != 16 {
		return out, ErrInvalid
	}
	copy(out.Id[:], id)
	if !out.Id.Valid() {
		return out, ErrInvalid
	}
	if s.hasDuration {
		if err := decoder.Unmarshal(fields[s.duration], &out.Duration); err != nil || out.Duration > uint64((7*24*time.Hour)/time.Microsecond) {
			return out, ErrInvalid
		}
	}
	for k, raw := range fields {
		f, ok := s.definition.Fields[k]
		if !ok {
			return out, fmt.Errorf("%w: unknown field %d", ErrInvalid, k)
		}
		if err := s.validateField(f, raw); err != nil {
			return out, err
		}
	}
	delete(fields, s.id)

	for _, k := range s.path {
		raw, ok := fields[k]
		if !ok || raw[0]>>5 != 0 {
			continue
		}
		var v uint64
		if decoder.Unmarshal(raw, &v) != nil {
			return out, ErrInvalid
		}
		canonical, _ := diskEncoder.Marshal(v)
		out.Path = append(out.Path, token{k, canonical})
		delete(fields, k)
	}
	var err error
	out.Dynamic, err = diskEncoder.Marshal(fields)
	return out, err
}

func millis(id codec.UUIDv7) int64 {
	var b [8]byte
	copy(b[2:], id[:6])
	return int64(binary.BigEndian.Uint64(b[:]))
}

func dayOf(id codec.UUIDv7) time.Time {
	t := time.UnixMilli(millis(id)).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func bucketOf(id codec.UUIDv7) uint16 { return uint16((millis(id) / 1000 % 86400) / 300) }

func (s *schema) validateField(f codec.Field, raw cbor.RawMessage) error {
	if len(raw) == 0 || raw[0] == 0xf6 {
		return ErrInvalid
	}
	if f.Object != nil {
		var fields map[codec.Key]cbor.RawMessage
		if decoder.Unmarshal(raw, &fields) != nil || fields == nil {
			return ErrInvalid
		}
		for k, v := range fields {
			child, ok := f.Object[k]
			if !ok {
				return ErrInvalid
			}
			if e := s.validateField(child, v); e != nil {
				return e
			}
		}
		return nil
	}
	if f.Array != nil {
		var items []cbor.RawMessage
		if decoder.Unmarshal(raw, &items) != nil || items == nil {
			return ErrInvalid
		}
		for _, v := range items {
			if e := s.validateField(codec.Field{Object: f.Array}, v); e != nil {
				return e
			}
		}
		return nil
	}
	if f.Domain != "" || len(f.Enums) > 0 {
		if raw[0]>>5 == 0 {
			var ref uint16
			if decoder.Unmarshal(raw, &ref) != nil {
				return ErrInvalid
			}
			if _, ok := f.Enums[ref]; !ok {
				if _, ok = s.definition.Domains[f.Domain][ref]; !ok {
					return ErrInvalid
				}
			}
		} else {
			var literal string
			if decoder.Unmarshal(raw, &literal) != nil {
				return ErrInvalid
			}
		}
		return nil
	}
	switch f.Name {
	case "duration", "count", "response_bytes", "responseBytes", "dropped", "status", "http_status", "httpStatus":
		var n uint64
		if decoder.Unmarshal(raw, &n) != nil {
			return ErrInvalid
		}
		if (f.Name == "status" || f.Name == "http_status" || f.Name == "httpStatus") && n > 999 {
			return ErrInvalid
		}
	case "version", "client_id", "client_transaction_id", "uri", "client_ip", "diagnostic", "clientId", "clientTransactionId", "clientIp":
		var text string
		if decoder.Unmarshal(raw, &text) != nil {
			return ErrInvalid
		}
	case "id":
	default:
		var value any
		if decoder.Unmarshal(raw, &value) != nil {
			return ErrInvalid
		}
	}
	return nil
}
