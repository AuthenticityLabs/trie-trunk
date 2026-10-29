package pil

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	pilcodec "github.com/AuthenticityLabs/pill-packer/go/codec"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/cache"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func definition(t *testing.T) []byte {
	t.Helper()
	b, e := CompileDefinition(Definition{Identity: "payment/transactions", Version: 1 << 20, Fields: []Field{{Name: "event_id", Type: "UUID"}, {Name: "state", Type: "STRING", Values: []string{"pending", "authorized"}}}, Resource: Binding{Format: StorageFormat, Service: "payment", Resource: "transactions", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}, Path: []string{"state"}}})
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestRegistrationBeforeWriteAndRecovery(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	c := cache.Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Now: func() time.Time { return now }}
	endpoint, e := cache.OpenEndpoint(c)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { endpoint.Close() }()
	var calls []string
	var bodies [][]byte
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == "POST" {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, b)
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		w := httptest.NewRecorder()
		endpoint.Handler("secret").ServeHTTP(w, r)
		return w.Result(), nil
	})}
	config := Config{URL: "https://logger.test", Cache: "demo", Token: "secret", Definition: definition(t), HTTPClient: client}
	logger, e := Open(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	if fmt.Sprint(calls) != "[HEAD /definitions PUT /definitions]" {
		t.Fatal(calls)
	}
	l, _ := cache.ParseDefinition(config.Definition)
	id, _ := codec.NewUUIDv7(now.Add(-time.Minute))
	raw, e := l.Encode(nil, []Value{{State: Direct, Bytes: id[:]}, {State: Direct, Bytes: []byte{1}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = logger.WriteFrames(ctx, raw); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(bodies[0], raw) {
		t.Fatal("PIL changed during transport")
	}
	// Server restart retains its immutable map and staged bytes. Warm negotiation
	// sends no definition body; replaying the same event remains idempotent.
	if e = endpoint.Close(); e != nil {
		t.Fatal(e)
	}
	endpoint, e = cache.OpenEndpoint(c)
	if e != nil {
		t.Fatal(e)
	}
	calls = nil
	if e = logger.Register(ctx); e != nil {
		t.Fatal(e)
	}
	if fmt.Sprint(calls) != "[HEAD /definitions]" {
		t.Fatal(calls)
	}
	if e = logger.WriteFrames(ctx, raw); e != nil {
		t.Fatal(e)
	}
	if e = endpoint.Close(); e != nil {
		t.Fatal(e)
	}
	c.Layout = l
	s, e := cache.Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	count := 0
	if e = s.SearchLive(ctx, cache.Query{Branch: cache.Write, From: now.Add(-time.Hour), To: now.Add(time.Hour)}, func(b []byte) error {
		count++
		if !bytes.Equal(b, raw) {
			t.Fatal("storage changed PIL")
		}
		return nil
	}); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestFailedRegistrationAndAcknowledgement(t *testing.T) {
	calls := 0
	config := Config{URL: "https://logger.test", Cache: "demo", Definition: definition(t)}
	config.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		w := httptest.NewRecorder()
		w.Header().Set("PIL-Version", "2")
		w.Header().Set("Definition-Version", "wrong")
		w.WriteHeader(204)
		return w.Result(), nil
	})}
	if l, e := Open(context.Background(), config); e == nil || l != nil || calls != 1 {
		t.Fatal("accepted wrong registration", e, calls)
	}
	config.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("PIL-Version", "2")
		w.Header().Set("Definition-Version", r.Header.Get("Definition-Version"))
		if r.Method == "HEAD" {
			w.WriteHeader(204)
		} else {
			calls++
			w.WriteHeader(200)
		}
		return w.Result(), nil
	})}
	l, e := Open(context.Background(), config)
	if e != nil {
		t.Fatal(e)
	}
	if e = l.WriteValues(context.Background(), []Value{{State: Absent}, {State: Offset, Bytes: []byte("new state")}}); e == nil {
		t.Fatal("accepted missing durability ack")
	}
	before := calls
	if e = l.WriteValues(context.Background(), []Value{{State: Absent}, {State: Null}}); e == nil || calls != before {
		t.Fatal("write without renegotiation")
	}
}

func TestPreferredDefinitionReordersAndRemaps(t *testing.T) {
	sourceDef := Definition{Identity: "payment/transactions", Version: 1 << 20, Fields: []Field{{Name: "event_id", Type: "UUID"}, {Name: "state", Type: "STRING", Values: []string{"pending", "authorized"}}, {Name: "note", Type: "STRING"}, {Name: "@fields", Type: "OBJECT"}}, Resource: Binding{Format: StorageFormat, Service: "payment", Resource: "transactions", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}}}
	source, _ := cache.Compile(sourceDef)
	targetDef := sourceDef
	targetDef.Version++
	targetDef.Resource.Minor = 1
	targetDef.PIL = 1
	targetDef.AdaptiveIntegers = "16"
	targetDef.Fields = []Field{{Name: "state", Type: "STRING", Values: []string{"authorized", "pending"}}, {Name: "event_id", Type: "UUID"}, {Name: "attempts", Type: "U16"}, {Name: "@fields", Type: "OBJECT"}, {Name: "optional", Type: "STRING"}}
	target, e := cache.Compile(targetDef)
	if e != nil {
		t.Fatal(e)
	}
	c := cache.Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Layout: target}
	endpoint, e := cache.OpenEndpoint(c)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { endpoint.Close() }()
	var calls []string
	var delivered []byte
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/ingest" {
			delivered, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(delivered))
		}
		w := httptest.NewRecorder()
		endpoint.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	logger, e := Open(context.Background(), Config{URL: "https://logger.test", Cache: "demo", Definition: source.Bytes, HTTPClient: client})
	if e != nil {
		t.Fatal(e)
	}
	if fmt.Sprint(calls) != "[HEAD /definitions PUT /definitions POST /definitions]" {
		t.Fatal(calls)
	}
	if !bytes.Equal(logger.AcceptedDefinition(), target.Bytes) {
		t.Fatal("did not accept preferred definition")
	}
	id, _ := codec.NewUUIDv7(time.Now())
	values := []Value{{State: Direct, Bytes: id[:]}, {State: Direct, Bytes: []byte{1}}, {State: Offset, Bytes: []byte("retained")}, {State: Offset, Bytes: nativeObject(t, map[string]any{"attempts": 9})}}
	original, e := source.Encode(nil, values)
	if e != nil {
		t.Fatal(e)
	}
	if e = logger.WriteFrames(context.Background(), original); e != nil {
		t.Fatal(e)
	}
	var f cache.Frame
	if e = target.Scan(delivered, &f); e != nil {
		t.Fatal(e)
	}
	if f.Values[0].State != Direct || f.Values[0].Bytes[0] != 0 || !bytes.Equal(f.Values[1].Bytes, id[:]) || f.Values[2].Bytes[0] != 9 || f.Values[4].State != Absent {
		t.Fatal("changed value semantics", f.Values)
	}
	if !bytes.Contains(f.Values[3].Bytes, []byte("note")) || !bytes.Contains(f.Values[3].Bytes, []byte("retained")) {
		t.Fatal("lost unpromoted field")
	}
	// Warm negotiation uses the cached preferred map without another body transfer.
	calls = nil
	if e = logger.Register(context.Background()); e != nil {
		t.Fatal(e)
	}
	if fmt.Sprint(calls) != "[HEAD /definitions POST /definitions]" {
		t.Fatal(calls)
	}
	if e = logger.WriteValues(context.Background(), values); e != nil {
		t.Fatal(e)
	}
	// New logger fetches preferred map after a known producer announcement.
	calls = nil
	if _, e = Open(context.Background(), Config{URL: "https://logger.test", Cache: "demo", Definition: source.Bytes, HTTPClient: client}); e != nil {
		t.Fatal(e)
	}
	if fmt.Sprint(calls) != "[HEAD /definitions GET /definitions POST /definitions]" {
		t.Fatal(calls)
	}
	// Restart retains both producer recognition and preferred layout.
	if e = endpoint.Close(); e != nil {
		t.Fatal(e)
	}
	c.Layout = nil
	endpoint, e = cache.OpenEndpoint(c)
	if e != nil {
		t.Fatal(e)
	}
	calls = nil
	if e = logger.Register(context.Background()); e != nil {
		t.Fatal(e)
	}
	if fmt.Sprint(calls) != "[HEAD /definitions POST /definitions]" {
		t.Fatal(calls)
	}
}

func TestREF16PreferredDefinitionNegotiation(t *testing.T) {
	d := Definition{Identity: "payment/transactions", Version: 1 << 20,
		Fields:   []Field{{Name: "event_id", Type: "UUID"}, {Name: "classification", Type: "REF16"}},
		Resource: Binding{Format: StorageFormat, Service: "payment", Resource: "transactions", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}}}
	source, err := cache.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	d.Version++
	d.Resource.Minor++
	d.Resource.References = []ReferenceTranslation{{Source: fmt.Sprintf("%x", source.Digest), Field: "classification", Values: map[uint16]uint16{1: 65535}}}
	target, err := cache.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cache.Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: source}
	endpoint, err := cache.OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { endpoint.Close() }()
	var frames [][]byte
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/ingest" {
			raw, _ := io.ReadAll(r.Body)
			frames = append(frames, raw)
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		w := httptest.NewRecorder()
		endpoint.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	logger, err := Open(context.Background(), Config{URL: "https://logger.test", Cache: "demo", Definition: source.Bytes, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err = endpoint.Prefer(target); err != nil {
		t.Fatal(err)
	}
	if err = logger.Register(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = logger.WriteValues(context.Background(), []Value{{State: Absent}, cache.Uint(1, 2)}); err != nil {
		t.Fatal(err)
	}
	var decoded cache.Frame
	if err = target.Scan(frames[0], &decoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Values[1].Bytes, []byte{255, 255}) {
		t.Fatal("reference not translated")
	}
	if err = logger.WriteValues(context.Background(), []Value{{State: Absent}, cache.Uint(2, 2)}); err == nil || len(frames) != 1 {
		t.Fatal("unmapped reference sent")
	}
	// The same immutable mapping is returned after listener restart; the
	// original producer definition remains the logger's input contract.
	endpoint.Close()
	cfg.Layout = nil
	endpoint, err = cache.OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = logger.Register(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = logger.WriteValues(context.Background(), []Value{{State: Absent}, cache.Uint(1, 2)}); err != nil {
		t.Fatal(err)
	}
	if err = target.Scan(frames[1], &decoded); err != nil || !bytes.Equal(decoded.Values[1].Bytes, []byte{255, 255}) {
		t.Fatal("restart changed mapping", err)
	}
}

func TestNegotiatedCompressionAndRawFallback(t *testing.T) {
	rawDefinition, e := CompileDefinition(Definition{Identity: "payment/receipts", Version: 1 << 20, Fields: []Field{{Name: "event_id", Type: "UUID"}, {Name: "body", Type: "STRING"}}, Resource: Binding{Format: StorageFormat, Service: "payment", Resource: "receipts", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}}})
	if e != nil {
		t.Fatal(e)
	}
	l, _ := cache.ParseDefinition(rawDefinition)
	for _, disable := range []bool{false, true} {
		endpoint, e := cache.OpenEndpoint(cache.Config{Root: t.TempDir(), Cache: "demo", Tenant: "local"})
		if e != nil {
			t.Fatal(e)
		}
		var received []byte
		var contentEncoding string
		client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/ingest" {
				contentEncoding = r.Header.Get("Content-Encoding")
				received, _ = io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(received))
			}
			w := httptest.NewRecorder()
			endpoint.ServeHTTP(w, r)
			return w.Result(), nil
		})}
		logger, e := Open(context.Background(), Config{URL: "https://logger.test", Cache: "demo", Definition: rawDefinition, HTTPClient: client, DisableCompression: disable})
		if e != nil {
			t.Fatal(e)
		}
		id, _ := codec.NewUUIDv7(time.Now())
		v := []Value{{State: Direct, Bytes: id[:]}, {State: Offset, Bytes: bytes.Repeat([]byte("timeout from upstream; retry later. "), 128)}}
		if e = logger.WriteValues(context.Background(), v); e != nil {
			t.Fatal(e)
		}
		if (contentEncoding == "gzip") == disable {
			t.Fatal("compression negotiation/config ignored")
		}
		expanded := received
		if contentEncoding == "gzip" {
			z, err := gzip.NewReader(bytes.NewReader(received))
			if err != nil {
				t.Fatal(err)
			}
			expanded, e = io.ReadAll(z)
			z.Close()
		}
		want, _ := l.Encode(nil, v)
		if e != nil || !bytes.Equal(expanded, want) {
			t.Fatal("logger round trip", e)
		}
		id, _ = codec.NewUUIDv7(time.Now())
		v[0].Bytes = id[:]
		v[1].Bytes = []byte("ok")
		if e = logger.WriteValues(context.Background(), v); e != nil {
			t.Fatal(e)
		}
		if contentEncoding != "" {
			t.Fatal("unexpected HTTP compression")
		}
		logger.Close()
		if e = endpoint.Close(); e != nil {
			t.Fatal(e)
		}
	}
}

func TestCapacityRolloverRetriesOnlyUnacceptedSuffix(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	endpoint, e := cache.OpenEndpoint(cache.Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer endpoint.Close()
	source, e := cache.ParseDefinition(definition(t))
	if e != nil {
		t.Fatal(e)
	}
	d := source.Definition
	d.Resource.Minor++
	d.Version++
	next, e := cache.Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	var bodies [][]byte
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/ingest" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			bodies = append(bodies, raw)
			if len(bodies) == 1 {
				first, err := source.ReadFrame(bytes.NewReader(raw), nil)
				if err != nil {
					return nil, err
				}
				r.Body = io.NopCloser(bytes.NewReader(first))
				ack := httptest.NewRecorder()
				endpoint.ServeHTTP(ack, r)
				if ack.Code != 200 {
					t.Fatal(ack.Code, ack.Body.String())
				}
				if err = endpoint.Prefer(next); err != nil {
					return nil, err
				}
				w := httptest.NewRecorder()
				w.Header().Set("PIL-Capacity-Rollover", "1")
				w.Header().Set("Trunk-Received", "1")
				w.WriteHeader(412)
				return w.Result(), nil
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		w := httptest.NewRecorder()
		endpoint.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	logger, e := Open(ctx, Config{URL: "https://logger.test", Cache: "demo", Branch: "write", Definition: source.Bytes, HTTPClient: client, DisableCompression: true})
	if e != nil {
		t.Fatal(e)
	}
	defer logger.Close()
	frames := make([][]byte, 2)
	for i := range frames {
		id, e := codec.NewUUIDv7(now.Add(-time.Minute + time.Duration(i)*time.Millisecond))
		if e != nil {
			t.Fatal(e)
		}
		frames[i], e = source.Encode(nil, []Value{{State: Direct, Bytes: id[:]}, {State: Direct, Bytes: []byte{1}}})
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = logger.WriteFrames(ctx, frames...); e != nil {
		t.Fatal(e)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[1], frames[1]) {
		t.Fatal("acknowledged prefix retransmitted")
	}
	if logger.layout.Digest != next.Digest {
		t.Fatal("logger did not retain new minor")
	}
}

func nativeObject(t *testing.T, v any) []byte {
	t.Helper()
	b, e := pilcodec.MarshalObject(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
