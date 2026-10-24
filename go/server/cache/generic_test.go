package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func TestGenericCBORIntakeRecovery(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	l, e := GenericDefinition()
	if e != nil {
		t.Fatal(e)
	}
	config := Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Layout: l, Now: func() time.Time { return now }}
	s, e := Open(config)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.Close() }()
	h := &HTTP{Store: s, Token: "secret"}
	request := func(raw []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/ingest", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/cbor")
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	id, _ := codec.NewUUIDv7(now.Add(-time.Minute))
	raw, _ := encoder.Marshal(map[string]any{"event_id": id.String(), "id": "object-123", "message": "hello", "nullable": nil, "nested": map[string]any{"value": true}, "list": []any{1, "two"}})
	if w := request(raw); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(raw); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"duplicate":true`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(s.branches[Write].rows) != 0 || s.branches[Write].live.count(0) != 1 {
		t.Fatal("must stage and deduplicate")
	}
	// The first sample cannot fix types or require fields in later sparse events.
	sparse, _ := encoder.Marshal(map[string]any{"message": 123})
	w := request(sparse)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt struct {
		EventID string `json:"event_id"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &receipt); e != nil || receipt.EventID == "" {
		t.Fatal("missing assigned identity", e)
	}
	if len(s.trie.nodes) > 1 {
		t.Fatal("blind payload added attribute trie nodes")
	}
	for _, bad := range [][]byte{append(append([]byte{}, raw...), 0), {0xff}} {
		if w = request(bad); w.Code != 400 {
			t.Fatal("accepted invalid/numeric unnegotiated CBOR", w.Code, w.Body.String())
		}
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(config)
	if e != nil {
		t.Fatal(e)
	}
	h.Store = s
	var payloads [][]byte
	collect := func() {
		payloads = nil
		if e = s.SearchLive(context.Background(), Query{Branch: Write, From: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), To: now.Add(time.Hour)}, func(raw []byte) error {
			var f Frame
			if e := l.Scan(raw, &f); e != nil {
				return e
			}
			payloads = append(payloads, append([]byte{}, f.Values[1].Bytes...))
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
	collect()
	if len(payloads) != 2 || !bytes.Equal(payloads[0], raw) || !bytes.Equal(payloads[1], sparse) {
		t.Fatal("payload/recovery changed input bytes")
	}
	now = now.Add(time.Hour)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	collect()
	if len(payloads) != 2 {
		t.Fatal("finalization lost data")
	}
	old, _ := codec.NewUUIDv7(now.Add(-2 * time.Hour))
	late, _ := encoder.Marshal(map[string]any{"event_id": old.String(), "message": "too late"})
	if w = request(late); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	rejected := 0
	if e = s.Rejections(func(r Rejection) error {
		var f Frame
		if e := l.Scan(r.PIL, &f); e != nil {
			return e
		}
		if !bytes.Equal(f.Values[1].Bytes, late) {
			t.Fatal("lost rejected payload")
		}
		rejected++
		return nil
	}); e != nil || rejected != 1 {
		t.Fatal(e, rejected)
	}
}

func TestStandardCBORHasNoCustomNegotiation(t *testing.T) {
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "local"})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	for _, raw := range [][]byte{{0xf6}, {0x81, 1}, {0xa1, 1, 2}, {0x9f, 1, 2, 0xff}, {0xc0, 0x61, 'x'}} {
		r := httptest.NewRequest("POST", "/ingest", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/cbor")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"HEAD", "PUT", "GET"} {
		r := httptest.NewRequest(method, "/ingest", nil)
		r.Header.Set("Trunk-Version", "1.0.0")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != 405 {
			t.Fatal(method, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/ingest", bytes.NewReader([]byte{0xa0}))
	r.Header.Set("Content-Type", "application/cbor")
	r.Header.Set("Trunk-Digest", "old")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
