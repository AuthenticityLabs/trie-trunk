package cache

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationEditorUsesHierarchyAndPreservesOtherSettings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "caches")
	p, err := OpenEndpoint(Config{Root: root, Cache: "demo", Tenant: "test", Layout: testLayout(t, false)})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	s := p.api.Store
	s.mu.Lock()
	_, err = s.openBranch(Write)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	n := s.layout.Definition.Resource
	resource := []string{"demo", n.Service, n.Resource + ".v1"}
	branch := append(append([]string{}, resource...), "write")
	trunk := configurationPath(root, nil)
	if err = os.WriteFile(trunk, []byte("version=1\n[policy.pac]\ngzip_level=8\n[policy.staging]\nmax_bytes=100\nmax_records=20\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resourceFile := configurationPath(root, resource)
	if err = os.WriteFile(resourceFile, []byte("version=1\n[policy.replication]\nindex_period='4h'\n[policy.staging]\nmax_bytes=0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(method string, scope []string, revision, body, token string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(scope)
		r := httptest.NewRequest(method, "/configurations?scope="+url.QueryEscape(string(raw)), bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("If-Match", `"`+revision+`"`)
		w := httptest.NewRecorder()
		p.operationHandler("secret").ServeHTTP(w, r)
		return w
	}
	if w := call("GET", resource, "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	get := func(scope []string) configurationView {
		t.Helper()
		w := call("GET", scope, "", "", "secret")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var v configurationView
		if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	initial := get(resource)
	if initial.Effective["pac.gzip_level"] != float64(8) || initial.Effective["staging.max_bytes"] != float64(0) || initial.Effective["staging.max_records"] != float64(20) {
		t.Fatal(initial.Effective)
	}
	if len(initial.Transforms) != 1 || initial.Transforms[0].Source != "" {
		t.Fatal(initial.Transforms)
	}
	w := call("PUT", resource, initial.Revision, `{"category":"compression","values":{"pac.gzip_level":9}}`, "secret")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	after := get(resource)
	if after.Local["replication.index_period"] != "4h" || after.Local["staging.max_bytes"] != float64(0) || after.Local["pac.gzip_level"] != float64(9) {
		t.Fatal(after.Local)
	}
	// A saved override does not claim to alter the running store.
	if s.policy.PAC.level() != 1 {
		t.Fatal("save changed active policy")
	}
	if w = call("PUT", resource, initial.Revision, `{"category":"compression","values":{"pac.gzip_level":4}}`, "secret"); w.Code != 412 {
		t.Fatal(w.Code)
	}
	before, _ := os.ReadFile(resourceFile)
	for _, body := range []string{
		`{"category":"compression","values":{"pac.gzip_level":10}}`,
		`{"category":"compression","values":{"object_index":true}}`,
		`{"category":"staging","values":{"staging.max_bytes":-1}}`,
		`{"category":"replication","values":{"replication.provisional_tail":"1m"}}`,
		`{"category":"compression","values":{"pac.gzip_level":4.5}}`,
		`{"category":"compression","values":{"pac.gzip_level":false}}`,
	} {
		if w = call("PUT", resource, after.Revision, body, "secret"); w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	unchanged, _ := os.ReadFile(resourceFile)
	if !bytes.Equal(before, unchanged) {
		t.Fatal("invalid update changed file")
	}
	bv := get(branch)
	if w = call("PUT", branch, bv.Revision, `{"category":"staging","values":{"staging.max_records":20}}`, "secret"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w = call("GET", []string{"demo", "..", ".."}, "", "", "secret"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	// Removing the last staging override must restore inheritance, including count.
	if w = call("PUT", resource, after.Revision, `{"category":"staging","values":{"staging.max_bytes":null}}`, "secret"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	inherited := get(resource)
	if inherited.Effective["staging.max_bytes"] != float64(100) || inherited.Effective["staging.max_records"] != float64(20) {
		t.Fatal(inherited.Effective)
	}
	// Runtime and UI resolve the same policy after restart/reload.
	if err = s.ReloadPolicy(); err != nil {
		t.Fatal(err)
	}
	if s.policy.PAC.level() != 9 || s.policy.Staging.MaxBytes != 100 || s.policy.Replication.IndexPeriod != "4h" {
		t.Fatal(s.policy)
	}
}
