package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBlindJSONLAndSyslogPersistNativeObjects(t *testing.T) {
	for _, tc := range []struct {
		kind, raw string
		count     int
	}{
		{"application/json", `{"wide":18446744073709551615,"nullable":null,"empty":"","nested":{"ok":false}}`, 1},
		{"application/x-ndjson", "{\"value\":0}\n{\"list\":[null,true,{\"x\":\"東京\"}]}\n", 2},
		{"application/syslog", "<34>1 2026-10-27T12:00:00Z host app - - - message\r\nsecond line\n", 2},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			config := Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Now: func() time.Time { return now }}
			endpoint, err := OpenEndpoint(config)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/ingest", bytes.NewBufferString(tc.raw))
			r.Header.Set("Content-Type", tc.kind)
			w := httptest.NewRecorder()
			endpoint.serveHTTP(w, r)
			if w.Code != 200 {
				endpoint.Close()
				t.Fatal(w.Code, w.Body.String())
			}
			var receipt struct {
				Received int `json:"received"`
			}
			if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Received != tc.count {
				t.Fatal(w.Body.String())
			}
			if err = endpoint.Close(); err != nil {
				t.Fatal(err)
			}
			endpoint, err = OpenEndpoint(config)
			if err != nil {
				t.Fatal(err)
			}
			defer endpoint.Close()
			l, _ := GenericDefinition()
			api := endpoint.apis[fmtDigestNative(l.Digest)]
			if api == nil {
				t.Fatal("missing blind resource")
			}
			count := 0
			err = api.Store.SearchLive(context.Background(), Query{Branch: Write, From: now.Add(-time.Hour), To: now.Add(time.Hour)}, func(raw []byte) error {
				var frame Frame
				if e := l.Scan(raw, &frame); e != nil {
					return e
				}
				var payload any
				if e := decoder.Unmarshal(frame.Values[1].Bytes, &payload); e != nil {
					return e
				}
				if l.Definition.Fields[1].Type != "OBJECT" {
					t.Fatal("non-PIL payload")
				}
				if tc.kind == "application/json" {
					m := payload.(map[any]any)
					if m["wide"] != uint64(^uint64(0)) || m["nullable"] != nil || m["empty"] != "" {
						t.Fatal(m)
					}
				}
				count++
				return nil
			})
			if err != nil || count != tc.count {
				t.Fatal(count, err)
			}
		})
	}
}

func TestBlindJSONRejectsDuplicateAndOutOfRangeValues(t *testing.T) {
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, raw := range []string{`{"x":1,"x":2}`, `{"nested":{"x":null,"x":false}}`, `{"x":18446744073709551616}`, `{"event_id":"bad"}`} {
		r := httptest.NewRequest("POST", "/ingest", bytes.NewBufferString(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		p.serveHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code, raw, w.Body.String())
		}
	}
}
