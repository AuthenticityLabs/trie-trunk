package cache

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func exportViewNative(t *testing.T, dir string, h *HTTP, from, to time.Time) {
	t.Helper()
	r := httptest.NewRequest("GET", "/collect?format=native&tap="+url.QueryEscape(h.tap(Write))+"&from="+url.QueryEscape(from.Format(time.RFC3339))+"&to="+url.QueryEscape(to.Format(time.RFC3339)), nil)
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Accept", "application/pil")
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
	w := httptest.NewRecorder()
	h.serveHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(fmt.Sprint(w.Code), w.Body.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "native.pil"), w.Body.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}
