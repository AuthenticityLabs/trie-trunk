package cache

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
)

func TestCacheCatalog(t *testing.T) {
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	check := func(handler http.Handler, want []string) {
		t.Helper()
		for _, path := range []string{"/caches"} {
			for _, tc := range []struct {
				method, suffix, token, accept string
				status                        int
			}{
				{"GET", "", "", "", 401},
				{"GET", "", "wrong", "", 401},
				{"POST", "", "secret", "", 405},
				{"GET", "?root=/etc", "secret", "", 404},
				{"GET", "", "secret", "application/pil", 406},
				{"GET", "", "secret", "application/json", 200},
			} {
				r := httptest.NewRequest(tc.method, path+tc.suffix, nil)
				r.Header.Set("Authorization", "Bearer "+tc.token)
				r.Header.Set("Accept", tc.accept)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != tc.status {
					t.Fatalf("%s %+v: %d %s", path, tc, w.Code, w.Body.String())
				}
				if w.Code == 405 && w.Header().Get("Allow") != "GET" {
					t.Fatal("missing Allow")
				}
				if w.Code != 200 {
					continue
				}
				var got map[string][]string
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if len(got) != 2 || !reflect.DeepEqual(got[path[1:]], want) || len(got["collections"]) != 0 {
					t.Fatalf("catalog %v, want %v", got, want)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("catalog must not be cached")
				}
			}
		}
	}
	check(p.operationHandler("secret"), []string{})
	if err := p.install(testLayout(t, false)); err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for b := Read; b <= Trace; b++ {
		want = append(want, p.api.tap(b))
	}
	sort.Strings(want)
	check(p.operationHandler("secret"), want)
	// The direct single-store handler has the same contract (branch order).
	want = []string{}
	for b := Read; b <= Trace; b++ {
		want = append(want, p.api.tap(b))
	}
	check(&HTTP{Store: p.api.Store, Token: "secret"}, want)
}

func TestCollectionCatalogRequiresEnabledReplay(t *testing.T) {
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Layout: testLayout(t, false), ObjectIndex: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	api := &HTTP{Store: s}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("GET", "/caches", nil))
	var got struct {
		Collections []string `json:"collections"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Collections, []string{api.tap(Write)}) {
		t.Fatal("missing enabled collection", w.Body.String())
	}
	// A retained definition without indexing must prevent advertising complete replay.
	other, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Layout: testLayout(t, false)})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if got := collectionSources([]*HTTP{api, {Store: other}}); len(got) != 0 {
		t.Fatal("disabled definition advertised", got)
	}
}
