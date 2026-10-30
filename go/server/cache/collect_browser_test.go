package cache

import (
	"encoding/json"
	"fmt"
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
func TestCollectionBrowser(t *testing.T) {
	target := os.Getenv("TRUNK_COLLECTION_BROWSER_URL")
	if target == "" {
		t.Skip("set TRUNK_COLLECTION_BROWSER_URL and TRUNK_COLLECTION_BROWSER_WEB")
	}
	web := os.Getenv("TRUNK_COLLECTION_BROWSER_WEB")
	if web == "" {
		t.Fatal("missing frontend build")
	}
	layout := testLayout(t, false)
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: layout})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	at := time.Now().Add(-30 * time.Second)
	for i := uint32(1); i <= 10000; i++ {
		if _, e = p.api.Store.Append(Write, testFrame(t, layout, at.Add(time.Duration(i)*time.Millisecond), i, false)); e != nil {
			t.Fatal(e)
		}
	}
	type measurement struct {
		Bytes      int
		Frames     int
		Checkpoint *int64
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
		if r.URL.Path == "/__test/collections" {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(measurements)
			return
		}
		if r.URL.Path == "/collect" {
			rec := httptest.NewRecorder()
			started := time.Now()
			api.ServeHTTP(rec, r)
			packs := collectionLivePacks(t, rec.Body.Bytes(), layout)
			frames := 0
			for _, pack := range packs {
				frames += len(pack.Members)
			}
			from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("from"))
			checkpoint := from.UnixMilli()
			m := measurement{Bytes: rec.Body.Len(), Checkpoint: &checkpoint, Frames: frames, ElapsedMs: float64(time.Since(started)) / float64(time.Millisecond)}
			mu.Lock()
			measurements = append(measurements, m)
			mu.Unlock()
			for key, values := range rec.Header() {
				w.Header()[key] = values
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
	fmt.Println("Collection fixture:", server.URL)
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
