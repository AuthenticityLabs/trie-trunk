package serve

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaticCaching(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	for name, body := range map[string]string{
		"index.html":                "<!doctype html><title>snoot</title>",
		"assets/pil-CsnSr8fl.wasm":  "\x00asm\x01\x00\x00\x00",
		"assets/worker-BsEjiMu6.js": "self.onmessage=()=>{};",
		"assets/pil.wasm":           "\x00asm\x01\x00\x00\x00",
	} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	immutable := "public, max-age=31536000, immutable"
	handler := staticFiles(root)
	for _, test := range []struct {
		name, method, path, header, value string
		status                            int
		policy, mime                      string
	}{
		{"shell", "GET", "/", "", "", 200, "no-cache", "text/html"},
		{"wasm", "GET", "/assets/pil-CsnSr8fl.wasm", "", "", 200, immutable, "application/wasm"},
		{"worker", "GET", "/assets/worker-BsEjiMu6.js", "", "", 200, immutable, "javascript"},
		{"unversioned", "GET", "/assets/pil.wasm", "", "", 200, "no-cache", "application/wasm"},
		{"head", "HEAD", "/assets/pil-CsnSr8fl.wasm", "", "", 200, immutable, "application/wasm"},
		{"revalidation", "GET", "/assets/pil-CsnSr8fl.wasm", "If-Modified-Since", modified.Format(http.TimeFormat), 304, immutable, ""},
		{"shell revalidation", "GET", "/", "If-Modified-Since", modified.Format(http.TimeFormat), 304, "no-cache", ""},
		{"range", "GET", "/assets/pil-CsnSr8fl.wasm", "Range", "bytes=0-3", 206, immutable, "application/wasm"},
		{"invalid range", "GET", "/assets/pil-CsnSr8fl.wasm", "Range", "bytes=100-200", 416, "no-store", "text/plain"},
		{"missing versioned", "GET", "/assets/missing-12345678.wasm", "", "", 404, "no-store", "text/plain"},
		{"method", "POST", "/assets/pil-CsnSr8fl.wasm", "", "", 405, "no-store", "text/plain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, nil)
			if test.header != "" {
				r.Header.Set(test.header, test.value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.status || w.Header().Get("Cache-Control") != test.policy || !strings.Contains(w.Header().Get("Content-Type"), test.mime) {
				t.Fatalf("status=%d headers=%v", w.Code, w.Header())
			}
			if (test.method == "HEAD" || test.status == 304) && w.Body.Len() != 0 {
				t.Fatalf("unexpected response body: %q", w.Body.String())
			}
		})
	}
}
