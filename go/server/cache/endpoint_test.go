package cache

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointRegistration(t *testing.T) {
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "local"}
	p, e := OpenEndpoint(c)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if _, e = os.Stat(filepath.Join(c.Root, c.Cache, "registry.map")); !os.IsNotExist(e) {
		t.Fatal("registered before first request", e)
	}
	if second, e := OpenEndpoint(c); e == nil {
		second.Close()
		t.Fatal("empty endpoint did not reserve its writer lease")
	}
	l := testLayout(t, false)
	request := func(method string, raw []byte, digest string, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/definitions", bytes.NewReader(raw))
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("Definition-Version", digest)
		r.Header.Set("Content-Type", "application/pil")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		p.operationHandler("secret").ServeHTTP(w, r)
		return w
	}
	digest := fmt.Sprintf("%x", l.Digest)
	if w := request("PUT", l.Bytes, digest, "bad"); w.Code != 401 || p.api != nil {
		t.Fatal("unauthorized registration", w.Code)
	}
	if w := request("HEAD", nil, digest, "secret"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request("PUT", []byte("bad"), digest, "secret"); w.Code != 400 || p.api != nil {
		t.Fatal("malformed registration", w.Code)
	}
	if w := request("PUT", l.Bytes, digest, "secret"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("PUT", l.Bytes, digest, "secret"); w.Code != 204 {
		t.Fatal("idempotent registration", w.Code)
	}
	other, _ := GenericDefinition()
	if w := request("PUT", other.Bytes, fmt.Sprintf("%x", other.Digest), "secret"); w.Code != 204 {
		t.Fatal("independent collection registration", w.Code)
	}
	if p.api.Store.layout.Digest != l.Digest {
		t.Fatal("changed immutable definition")
	}
}
func TestEndpointBlindFirstWrite(t *testing.T) {
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "local"})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	raw, _ := ingressEncoder.Marshal(map[string]any{"message": "hello"})
	r := httptest.NewRequest("POST", "/ingest", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/cbor")
	w := httptest.NewRecorder()
	p.serveHTTP(w, r)
	if w.Code != 200 || p.api == nil || !p.api.Store.generic() {
		t.Fatal(w.Code, w.Body.String())
	}
}
