package logging

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"

	pilcodec "github.com/AuthenticityLabs/pill-packer/go/codec"
	"trie-trunk/go/codec"
	Keys "trie-trunk/go/logger/client/keys"
	"trie-trunk/go/logger/pil"
	"trie-trunk/go/models"
	"trie-trunk/go/server/cache"
)

// The request logger's vocabulary is an optional producer definition, not a
// restriction on the generic PIL logger or on collection storage.
const (
	slotEventId = iota
	slotCategory
	slotLevel
	slotService
	slotVersion
	slotResource
	slotOperation
	slotClientTransactionId
	slotClientId
	slotDuration
	slotHttpMethod
	slotHttpStatus
	slotResponseBytes
	slotMessage
	slotError
	slotCheckpoints
	slotUri
	slotClientIp
	slotDiagnostic
	slotDropped
	slotFields
	slotAccess
)

var pilLogFields = []pil.Field{
	{Name: "event_id", Type: "UUID"}, {Name: "category", Type: "STRING"},
	{Name: "level", Type: "STRING"}, {Name: "service", Type: "STRING"},
	{Name: "version", Type: "STRING"}, {Name: "resource", Type: "STRING"},
	{Name: "operation", Type: "STRING"}, {Name: "client_transaction_id", Type: "STRING"},
	{Name: "client_id", Type: "STRING"}, {Name: "duration", Type: "U64"},
	{Name: "http_method", Type: "STRING"}, {Name: "http_status", Type: "U16"},
	{Name: "response_bytes", Type: "U64"}, {Name: "message", Type: "STRING"},
	{Name: "error", Type: "OBJECT"}, {Name: "checkpoints", Type: "OBJECT"},
	{Name: "uri", Type: "STRING"}, {Name: "client_ip", Type: "STRING"},
	{Name: "diagnostic", Type: "STRING"}, {Name: "dropped", Type: "U64"},
	{Name: "@fields", Type: "OBJECT"},
	{Name: "access", Type: "STRING", Values: []string{"read", "write"}},
}

// BuildPILDefinition compiles this convenience logger's vocabulary into a unified
// map. The caller supplies collection identity, timezone and storage policy.
// Application reference IDs are resolved at compilation, not sent as legacy IDs.
func BuildPILDefinition(binding pil.Binding, dictionary *codec.Dictionary, adaptive string) ([]byte, error) {
	binding.Format = pil.StorageFormat
	if binding.Roles.Event == "" {
		binding.Roles.Event = "event_id"
	}
	if binding.Roles.Duration == "" {
		binding.Roles.Duration = "duration"
	}
	if binding.Roles.Event != "event_id" || binding.Roles.Duration != "duration" || binding.Roles.Object != "" || binding.Roles.Revision != "" {
		return nil, fmt.Errorf("request logger requires event_id/duration roles and does not invent object identities")
	}
	d := pil.Definition{Identity: binding.Service + "/" + binding.Resource, Version: uint64(binding.Major)<<20 | uint64(binding.Minor), Resource: binding, Fields: append([]pil.Field(nil), pilLogFields...), AdaptiveIntegers: adaptive}
	if adaptive != "" && adaptive != "none" {
		d.PIL = 1
	}
	schema := BuildSchema()
	domains := codec.Define(1, schema, dictionary).Domains
	for i, f := range d.Fields {
		for _, s := range schema {
			if s.Name != f.Name || f.Type != "STRING" {
				continue
			}
			labels := map[string]bool{}
			for _, v := range s.Enums {
				labels[v] = true
			}
			for _, v := range domains[s.Domain] {
				labels[v] = true
			}
			for v := range labels {
				d.Fields[i].Values = append(d.Fields[i].Values, v)
			}
			sort.Strings(d.Fields[i].Values)
		}
	}
	return pil.CompileDefinition(d)
}

// NewPIL registers the caller's definition before returning a request logger.
// It preserves the builder and HTTP middleware lifecycle but writes positional
// values directly into the negotiated PIL encoder. NewStream writes framed dictionary PIL.
func NewPIL(ctx context.Context, transport pil.Config, config Config) (*Logger, error) {
	if transport.Branch != "read" && transport.Branch != "write" {
		return nil, fmt.Errorf("request logger requires read or write access in the transport routing configuration")
	}
	l, err := newLogger(config)
	if err != nil {
		return nil, err
	}
	layout, err := cache.ParseDefinition(transport.Definition)
	if err != nil {
		return nil, err
	}
	l.pilPlan, err = compilePILEntry(layout, config.Dictionary)
	if err != nil {
		return nil, err
	}
	l.pilPlan.access = transport.Branch
	l.pil, err = pil.Open(ctx, transport)
	if err != nil {
		return nil, err
	}
	return l, nil
}

type pilEntryPlan struct {
	scratch    []pil.Value
	numeric    [26]byte
	core       [22]int
	access     string
	owned      map[string]bool
	layout     *cache.Layout
	slots      map[string]int
	refs       []map[uint16]pil.Value
	texts      []map[string]pil.Value
	dictionary *codec.Dictionary
}

func compilePILEntry(l *cache.Layout, dictionary *codec.Dictionary) (*pilEntryPlan, error) {
	if l.Definition.Resource.Roles.Event != "event_id" || l.Definition.Resource.Roles.Duration != "duration" || l.Object >= 0 || l.Revision >= 0 {
		return nil, fmt.Errorf("request logger requires event_id/duration roles; use generic PIL logger for object mutations")
	}
	p := &pilEntryPlan{layout: l, slots: map[string]int{}, refs: make([]map[uint16]pil.Value, len(l.Widths)), owned: map[string]bool{}, texts: make([]map[string]pil.Value, len(l.Widths)), dictionary: dictionary, scratch: make([]pil.Value, len(l.Widths))}
	for i, f := range l.Definition.Fields {
		p.slots[f.Name] = i
		p.texts[i] = map[string]pil.Value{}
		for n, label := range f.Values {
			p.texts[i][label] = cache.Uint(uint64(n), l.Widths[i])
		}
	}
	for slot, f := range pilLogFields {
		i, ok := p.slots[f.Name]
		if !ok || l.Definition.Fields[i].Type != f.Type {
			return nil, fmt.Errorf("request logger requires %s: %s", f.Name, f.Type)
		}
		p.core[slot] = i
		p.owned[f.Name] = true
	}
	definition := codec.Define(1, BuildSchema(), dictionary)
	for _, f := range definition.Fields {
		i, ok := p.slots[f.Name]
		if !ok {
			continue
		}
		refs := map[uint16]pil.Value{}
		for n, label := range f.Enums {
			refs[n] = p.text(i, label)
		}
		for n, label := range definition.Domains[f.Domain] {
			refs[n] = p.text(i, label)
		}
		p.refs[i] = refs
	}
	return p, nil
}
func (p *pilEntryPlan) text(i int, s string) pil.Value {
	if v, ok := p.texts[i][s]; ok {
		return v
	}
	return cache.Text(s)
}

// Access is independent of level and of the physical diagnostic partition.
// HTTP methods classify individual requests; configured access covers non-HTTP actions.
func (p *pilEntryPlan) accessFor(e *Entry) string {
	if method, ok := e.HttpMethod.Raw().(string); ok {
		switch method {
		case "GET", "HEAD", "OPTIONS":
			return "read"
		case "POST", "PUT", "PATCH", "DELETE":
			return "write"
		}
	} else {
		switch HttpMethod(e.HttpMethod.Id()) {
		case GET, HEAD, OPTIONS:
			return "read"
		case POST, PUT, PATCH, DELETE:
			return "write"
		}
	}
	return p.access
}

func (p *pilEntryPlan) values(e *Entry) ([]pil.Value, error) {
	values := p.scratch
	clear(values)
	put := func(slot int, v pil.Value) { values[p.core[slot]] = v }
	put(slotEventId, pil.Value{State: pil.Direct, Bytes: e.Id[:]})
	if access := p.accessFor(e); access != "" {
		put(slotAccess, p.text(p.core[slotAccess], access))
	}
	binary.LittleEndian.PutUint64(p.numeric[:8], uint64(e.Duration.Microseconds()))
	put(slotDuration, pil.Value{State: pil.Direct, Bytes: p.numeric[:8]})
	var symbolErr error
	symbol := func(slot int, s codec.Symbol) {
		if s.IsZero() {
			return
		}
		if label, ok := s.Raw().(string); ok {
			put(slot, p.text(p.core[slot], label))
			return
		}
		v, ok := p.refs[p.core[slot]][s.Id()]
		if !ok {
			symbolErr = fmt.Errorf("undefined %s reference %d", p.layout.Definition.Fields[p.core[slot]].Name, s.Id())
			return
		}
		put(slot, v)
	}
	symbol(slotCategory, codec.Ref(uint16(e.Category)))
	symbol(slotLevel, codec.Ref(uint16(e.Level)))
	symbol(slotService, e.Service)
	symbol(slotResource, e.Resource)
	symbol(slotOperation, e.Operation)
	symbol(slotHttpMethod, e.HttpMethod)
	symbol(slotMessage, e.Message)
	if symbolErr != nil {
		return nil, symbolErr
	}
	for _, v := range []struct {
		slot int
		text string
	}{
		{slotVersion, e.Version}, {slotClientTransactionId, e.ClientTransactionId}, {slotClientId, e.ClientId},
		{slotUri, e.Uri}, {slotClientIp, e.ClientIp}, {slotDiagnostic, e.Diagnostic},
	} {
		if v.text != "" {
			put(v.slot, p.text(p.core[v.slot], v.text))
		}
	}
	if e.present&(1<<Keys.HttpStatus) != 0 {
		binary.LittleEndian.PutUint16(p.numeric[8:10], e.HttpStatus)
		put(slotHttpStatus, pil.Value{State: pil.Direct, Bytes: p.numeric[8:10]})
	}
	if e.present&(1<<Keys.ResponseBytes) != 0 {
		binary.LittleEndian.PutUint64(p.numeric[10:18], e.ResponseBytes)
		put(slotResponseBytes, pil.Value{State: pil.Direct, Bytes: p.numeric[10:18]})
	}
	if e.Dropped > 0 {
		binary.LittleEndian.PutUint64(p.numeric[18:26], e.Dropped)
		put(slotDropped, pil.Value{State: pil.Direct, Bytes: p.numeric[18:26]})
	}
	if e.Error != nil {
		// Complex values are independent, named PIL objects, never legacy numeric-key
		// objects requiring an out-of-band logger dictionary to interpret them.
		raw, err := pilcodec.MarshalObject(codec.Expand(e.Error.Record(), models.BuildSchema(), p.dictionary))
		if err != nil {
			return nil, err
		}
		put(slotError, pil.Value{State: pil.Offset, Bytes: raw})
	}
	if len(e.Checkpoints) > 0 {
		type checkpoint struct {
			Name     any    `cbor:"name"`
			Duration int64  `cbor:"duration"`
			Count    uint64 `cbor:"count"`
		}
		items := make([]checkpoint, len(e.Checkpoints))
		for i, c := range e.Checkpoints {
			label := p.dictionary.Resolve("checkpoint", c.Name)
			if _, ok := label.(string); !ok {
				return nil, fmt.Errorf("undefined checkpoint reference %d", c.Name.Id())
			}
			items[i] = checkpoint{label, c.Duration.Microseconds(), c.Count}
		}
		raw, err := pilcodec.MarshalObject(items)
		if err != nil {
			return nil, err
		}
		put(slotCheckpoints, pil.Value{State: pil.Offset, Bytes: raw})
	}
	var extras map[string]codec.Value
	for name, v := range e.Fields {
		if i, ok := p.slots[name]; ok {
			// Convenience fields cannot overwrite logger-owned metadata, even if absent.
			if p.owned[name] {
				return nil, fmt.Errorf("extension shadows logger field %s", name)
			}
			raw, err := pilcodec.MarshalObject(v)
			if err != nil {
				return nil, err
			}
			values[i], err = p.layout.ValueFromObject(i, raw)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", name, err)
			}
		} else {
			if extras == nil {
				extras = map[string]codec.Value{}
			}
			extras[name] = v
		}
	}
	if len(extras) > 0 {
		raw, err := pilcodec.MarshalObject(extras)
		if err != nil {
			return nil, err
		}
		put(slotFields, pil.Value{State: pil.Offset, Bytes: raw})
	}
	return values, nil
}
