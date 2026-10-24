package serve

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTapsAuthenticationAndMethods(t *testing.T) {
	h, err := New(t.TempDir(), "secret", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, name := range []string{"vault", "billing", ".trunk", "Invalid"} {
		if err := os.Mkdir(filepath.Join(h.Root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	h.slots <- struct{}{}
	for _, test := range []struct {
		method, path, token, accept string
		status                      int
	}{
		{"GET", "/taps", "", "", 401},
		{"GET", "/taps", "Bearer wrong", "", 401},
		{"GET", "/taps", "Bearer secret", "application/json", 200},
		{"GET", "/taps", "Bearer secret", "", 200},
		{"GET", "/taps", "Bearer secret", "application/x-ndjson", 406},
		{"POST", "/taps", "Bearer secret", "", 405},
		{"GET", "/taps/nested", "Bearer secret", "", 404},
		{"GET", "/taps?root=/etc", "Bearer secret", "", 404},
	} {
		r := httptest.NewRequest(test.method, test.path, nil)
		r.Header.Set("Authorization", test.token)
		r.Header.Set("Accept", test.accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%+v: %d %s", test, w.Code, w.Body.String())
		}
		if test.status == 200 && (w.Body.String() != "{\"taps\":[\"billing\",\"vault\"]}\n" || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal(w.Header(), w.Body.String())
		}
		if test.status == 405 && w.Header().Get("Allow") != "GET" {
			t.Fatal("missing GET allow header")
		}
	}
	w := httptest.NewRecorder()
	h.serve(w, httptest.NewRequest(http.MethodGet, "/taps", nil), false)
	if w.Code != 200 {
		t.Fatal("local socket listing failed", w.Code)
	}
	<-h.slots
}

func TestTapsEmptyAndReadFailure(t *testing.T) {
	h, err := New(filepath.Join(t.TempDir(), "missing"), "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/taps", nil))
	if w.Code != 200 || w.Body.String() != "{\"taps\":[]}\n" {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := os.WriteFile(h.Root, nil, 0644); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/taps", nil))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}

// New clients use /caches; older clients retain the original response key.
func TestCachesCatalog(t *testing.T) {
	h, err := New(t.TempDir(), "secret", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := os.Mkdir(filepath.Join(h.Root, "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/caches", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "{\"caches\":[\"demo\"]}\n" {
		t.Fatal(w.Code, w.Body.String())
	}
}
