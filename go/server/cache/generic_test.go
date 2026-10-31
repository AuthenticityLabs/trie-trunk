package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/fxamacker/cbor/v2"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

var ingressEncoder, _ = cbor.CoreDetEncOptions().EncMode()

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
		h.serveHTTP(w, r)
		return w
	}
	id, _ := codec.NewUUIDv7(now.Add(-time.Minute))
	raw, _ := ingressEncoder.Marshal(map[string]any{"event_id": id.String(), "id": "object-123", "message": "hello", "nullable": nil, "nested": map[string]any{"value": true}, "list": []any{1, "two"}})
	if w := request(raw); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(raw); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"duplicate":false`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(s.branches[Write].rows) != 0 || s.branches[Write].live.count(0) != 2 {
		t.Fatal("each receipt gets a provisional ordinal")
	}
	// The first sample cannot fix types or require fields in later sparse events.
	sparse, _ := ingressEncoder.Marshal(map[string]any{"message": 123})
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
	if len(payloads) != 3 || !sameAcceptedBlindPayload(payloads[0], raw) || !sameAcceptedBlindPayload(payloads[1], raw) || !sameBlindPayload(payloads[2], sparse) {
		t.Fatal("payload/recovery changed input bytes")
	}
	now = now.Add(time.Hour)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	collect()
	if len(payloads) != 3 {
		t.Fatal("finalization lost data")
	}
	old, _ := codec.NewUUIDv7(now.Add(-2 * time.Hour))
	late, _ := ingressEncoder.Marshal(map[string]any{"event_id": old.String(), "message": "too late"})
	if w = request(late); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	rejected := 0
	if e = s.Rejections(func(r Rejection) error {
		var f Frame
		if e := l.Scan(r.PIL, &f); e != nil {
			return e
		}
		if !sameBlindPayload(f.Values[1].Bytes, late) {
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
	for _, raw := range [][]byte{{0xf6}, {0x81, 1}, {0xa1, 1, 2}, {0x9f, 1, 2, 0xff}} {
		r := httptest.NewRequest("POST", "/ingest", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/cbor")
		w := httptest.NewRecorder()
		p.serveHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"HEAD", "PUT", "GET"} {
		r := httptest.NewRequest(method, "/ingest", nil)
		r.Header.Set("Trunk-Version", "1.0.0")
		w := httptest.NewRecorder()
		p.serveHTTP(w, r)
		if w.Code != 405 {
			t.Fatal(method, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/ingest", bytes.NewReader([]byte{0xa0}))
	r.Header.Set("Content-Type", "application/cbor")
	r.Header.Set("Trunk-Digest", "old")
	w := httptest.NewRecorder()
	p.serveHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func sameBlindPayload(pil, wire []byte) bool {
	var a, b any
	if decoder.Unmarshal(pil, &a) != nil || ingressDecoder.Unmarshal(wire, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func marshalBlindCBOR(v any) ([]byte, error) { return ingressEncoder.Marshal(v) }

func sameAcceptedBlindPayload(stored, wire []byte) bool {
	var a, b map[string]any
	if decoder.Unmarshal(stored, &a) != nil || ingressDecoder.Unmarshal(wire, &b) != nil {
		return false
	}
	if _, exists := a["event_id"]; exists {
		return false
	}
	delete(b, "event_id")
	return reflect.DeepEqual(a, b)
}
