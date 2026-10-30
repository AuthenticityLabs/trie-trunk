package cache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Opt-in full-app fixture. Its endpoint and live records are disposable and
// inaccessible outside loopback. No controls are added to the shipped server.
func TestQueryDeltaBrowser(t *testing.T) {
	target := os.Getenv("TRUNK_QUERY_BROWSER_URL")
	if target == "" {
		t.Skip("set TRUNK_QUERY_BROWSER_URL and TRUNK_QUERY_BROWSER_WEB")
	}
	web := os.Getenv("TRUNK_QUERY_BROWSER_WEB")
	if web == "" {
		t.Fatal("missing frontend build")
	}
	layout := testLayout(t, false)
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: layout})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	at := time.Now().Add(-30 * time.Minute)
	for i := uint32(1); i <= 10000; i++ {
		if _, e = p.api.Store.Append(Write, testFrame(t, layout, at.Add(time.Duration(i)*time.Millisecond), i, false)); e != nil {
			t.Fatal(e)
		}
	}
	type measurement struct {
		Bytes      int
		Frames     int
		Checkpoint *int64
		Known      int
		ElapsedMs  float64
	}
	var measurements []measurement
	var mu sync.Mutex
	static := http.FileServer(http.Dir(web))
	api := p.Handler("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__test/append" && r.Method == "POST" {
			if _, e := p.api.Store.Append(Write, testFrame(t, layout, time.Now().Add(-time.Millisecond), 20000, false)); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__test/deltas" {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(measurements)
			return
		}
		if r.URL.Path == "/query-delta" {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var request queryDeltaRequest
			_ = decoder.Unmarshal(raw, &request)
			rec := httptest.NewRecorder()
			started := time.Now()
			api.ServeHTTP(rec, r)
			elapsed := time.Since(started)
			var delta queryDeltaResponse
			_ = decoder.Unmarshal(rec.Body.Bytes(), &delta)
			m := measurement{Bytes: rec.Body.Len(), Checkpoint: request.Checkpoint, Known: len(request.Known), ElapsedMs: float64(elapsed) / float64(time.Millisecond)}
			for _, b := range delta.Branches {
				m.Frames += len(b.Frames)
			}
			mu.Lock()
			measurements = append(measurements, m)
			mu.Unlock()
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}

		if r.URL.Path == "/" || r.URL.Path == "/index.html" || filepath.Ext(r.URL.Path) == ".js" || filepath.Ext(r.URL.Path) == ".css" || filepath.Ext(r.URL.Path) == ".wasm" || filepath.Ext(r.URL.Path) == ".png" || filepath.Ext(r.URL.Path) == ".svg" {
			static.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	if e = os.WriteFile(target, []byte(server.URL), 0600); e != nil {
		t.Fatal(e)
	}
	fmt.Println("Live-query fixture:", server.URL)
	timeout := time.After(8 * time.Minute)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-timeout:
			t.Fatal("browser verification timed out")
		case <-tick.C:
			if _, e := os.Stat(target + ".done"); e == nil {
				return
			}
		}
	}
}
