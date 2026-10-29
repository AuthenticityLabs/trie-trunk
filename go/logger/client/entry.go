package logging

import (
	"time"
	Keys "trie-trunk/go/logger/client/keys"

	"trie-trunk/go/codec"
	"trie-trunk/go/models"
)

type Checkpoint struct {
	Name     codec.Symbol
	Duration time.Duration
	Count    uint64
}

// Entry is a detached snapshot. Use Builder to populate presence information.
// Id is UUIDv7; durations encode as integer microseconds. Timestamp evaluation
// belongs to the log-view client, not the entry model.
type Entry struct {
	Category            Category
	Level               Level
	Service             codec.Symbol
	Version             string
	Resource            codec.Symbol
	Operation           codec.Symbol
	Id                  codec.UUIDv7
	ClientTransactionId string
	ClientId            string
	Duration            time.Duration
	HttpMethod          codec.Symbol
	HttpStatus          uint16
	ResponseBytes       uint64
	Message             codec.Symbol
	Error               *models.Error
	Checkpoints         []Checkpoint
	Uri                 string
	ClientIp            string
	Fields              map[string]codec.Value
	Diagnostic          string
	Dropped             uint64
	present             uint64
}

func (e Entry) Record() codec.Record {
	r := codec.Record{Keys.Category: uint16(e.Category), Keys.Level: uint16(e.Level), Keys.Id: e.Id, Keys.Duration: e.Duration.Microseconds()}
	for _, field := range []struct {
		key   codec.Key
		value codec.Symbol
	}{
		{Keys.Service, e.Service}, {Keys.Resource, e.Resource}, {Keys.Operation, e.Operation}, {Keys.HttpMethod, e.HttpMethod}, {Keys.Message, e.Message},
	} {
		if !field.value.IsZero() {
			r[field.key] = field.value
		}
	}
	for _, field := range []struct {
		key   codec.Key
		value string
	}{
		{Keys.Version, e.Version}, {Keys.ClientTransactionId, e.ClientTransactionId}, {Keys.ClientId, e.ClientId}, {Keys.Uri, e.Uri}, {Keys.ClientIp, e.ClientIp}, {Keys.Diagnostic, e.Diagnostic},
	} {
		if e.present&(1<<field.key) != 0 || field.value != "" {
			r[field.key] = field.value
		}
	}
	if e.present&(1<<Keys.HttpStatus) != 0 {
		r[Keys.HttpStatus] = e.HttpStatus
	}
	if e.present&(1<<Keys.ResponseBytes) != 0 {
		r[Keys.ResponseBytes] = e.ResponseBytes
	}
	if e.Error != nil {
		r[Keys.Error] = e.Error.Record()
	}
	if len(e.Checkpoints) > 0 {
		checkpoints := make([]codec.Record, len(e.Checkpoints))
		for i, c := range e.Checkpoints {
			checkpoints[i] = codec.Record{0: c.Name, 1: c.Duration.Microseconds(), 2: c.Count}
		}
		r[Keys.Checkpoints] = checkpoints
	}
	if len(e.Fields) > 0 {
		r[Keys.Fields] = e.Fields
	}
	if e.Dropped > 0 {
		r[Keys.Dropped] = e.Dropped
	}
	return r
}

func (e Entry) Encode(format codec.Format, dictionary *codec.Dictionary) ([]byte, error) {
	if format == codec.PIL {
		return codec.MarshalCompact(e.Record(), codec.PIL)
	}
	if format != codec.ReadableJSON {
		return e.encodeCompact(format)
	}
	return codec.Encode(e.Record(), entrySchema, dictionary, format)
}

var entrySchema = BuildSchema()

func (e Entry) clone() Entry {
	e.Checkpoints = append([]Checkpoint(nil), e.Checkpoints...)
	if e.Fields != nil {
		fields := make(map[string]codec.Value, len(e.Fields))
		for k, v := range e.Fields {
			fields[k] = v
		}
		e.Fields = fields
	}
	return e
}
