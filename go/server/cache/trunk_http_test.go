package cache

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func decodeTrunk(t *testing.T, raw []byte) (trunkCatalog, map[string][]byte, int) {
	t.Helper()
	if !bytes.HasPrefix(raw, []byte{'T', 'P', 'I', 'L', 2}) {
		t.Fatal("missing bootstrap preamble")
	}
	input := bytes.NewReader(raw[5:])
	files := map[string][]byte{}
	definitions := 0
	var catalog trunkCatalog
	for {
		tag, err := input.ReadByte()
		if err != nil {
			t.Fatal(err)
		}
		if tag == 255 {
			end, _ := input.ReadByte()
			if end != 254 || input.Len() != 0 {
				t.Fatal("incomplete bootstrap")
			}
			break
		}
		layout := trunkControl
		switch tag {
		case 1:
			layout = defineControl
			definitions++
		case 2:
			layout = bindControl
		case 10:
		default:
			t.Fatal("unexpected bootstrap tag", tag)
		}
		frame, err := layout.ReadFrame(input, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tag == 10 {
			var f Frame
			if err = layout.Scan(frame, &f); err != nil {
				t.Fatal(err)
			}
			var m struct {
				Kind    string       `pil:"kind"`
				Path    string       `pil:"path"`
				Offset  uint64       `pil:"offset"`
				Bytes   []byte       `pil:"bytes"`
				Catalog trunkCatalog `pil:"catalog"`
			}
			if err = decoder.Unmarshal(f.Values[0].Bytes, &m); err != nil {
				t.Fatal(err)
			}
			switch m.Kind {
			case "file":
				if uint64(len(files[m.Path])) != m.Offset {
					t.Fatal("unordered file chunks")
				}
				files[m.Path] = append(files[m.Path], m.Bytes...)
			case "catalog":
				catalog = m.Catalog
			default:
				t.Fatal("invalid trunk message")
			}
		}
	}
	return catalog, files, definitions
}
func TestTrunkBootstrapIncludesEveryDefinitionAndActualFolderPaths(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err = p.api.Store.Append(Write, testFrame(t, l, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	if err = p.api.Store.Flush(); err != nil {
		t.Fatal(err)
	}
	next := l.Definition
	next.Version++
	next.Resource.Minor++
	newer, err := Compile(next)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.install(newer); err != nil {
		t.Fatal(err)
	}
	get := func(method, path, accept, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Accept", accept)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		p.Handler("secret").ServeHTTP(w, r)
		return w
	}
	w := get("GET", "/trunk", "application/pil", "secret")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Encoding") != "" {
		t.Fatal("bootstrap must remain uncompressed")
	}
	catalog, files, count := decodeTrunk(t, w.Body.Bytes())
	if count != 2 || len(catalog.Sources) != 5 || len(catalog.Collections) != 1 {
		t.Fatal(count, catalog)
	}
	seen := map[string]trunkFile{}
	for _, f := range catalog.Files {
		seen[f.Path] = f
		if f.Kind == "definition" {
			continue
		}
		if f.Bootstrap {
			raw := files[f.Path]
			sum := sha256.Sum256(raw)
			if uint64(len(raw)) != f.Size || fmt.Sprintf("%x", sum) != f.Digest {
				t.Fatal("invalid file identity", f.Path)
			}
		}
	}
	for _, name := range []string{"demo/resource.tri", "demo/payment/transactions.v1/0/resource.rx", "demo/payment/transactions.v1/1/resource.rx", "demo/payment/transactions.v1/0/state.tri", "demo/payment/transactions.v1/1/state.tri", "demo/payment/transactions.v1/0/write.tri"} {
		if _, ok := seen[name]; !ok {
			t.Fatal("missing registered path", name)
		}
	}
	if _, ok := seen["demo/registry.tri"]; ok {
		t.Fatal("obsolete hierarchy name")
	}
	rawState := get("GET", "/demo/payment/transactions.v1/0/state.tri", "application/octet-stream", "secret")
	if rawState.Code != 200 || !bytes.Equal(rawState.Body.Bytes(), files["demo/payment/transactions.v1/0/state.tri"]) {
		t.Fatal("state file traversal changed bytes")
	}
	definition := get("GET", "/demo/payment/transactions.v1/1/resource.rx", "application/pil", "secret")
	if definition.Code != 200 || !bytes.Equal(definition.Body.Bytes(), newer.Bytes) {
		t.Fatal("minor path returned the wrong definition")
	}
	scoped := get("GET", "/trunk/demo/payment/transactions.v1/0/", "application/pil", "secret")
	if scoped.Code != 200 {
		t.Fatal(scoped.Code, scoped.Body.String())
	}
	minor, _, count := decodeTrunk(t, scoped.Body.Bytes())
	if count != 1 || minor.Scope != "demo/payment/transactions.v1/0" {
		t.Fatal(count, minor)
	}
	for _, f := range minor.Files {
		if !strings.HasPrefix(f.Path, minor.Scope+"/") {
			t.Fatal("folder traversal escaped its scope", f)
		}
	}
	for _, tc := range []struct {
		method, path, accept, token string
		status                      int
	}{{"GET", "/trunk", "application/pil", "bad", 401}, {"POST", "/trunk", "application/pil", "secret", 405}, {"GET", "/trunk", "application/json", "secret", 406}, {"GET", "/trunk?path=etc", "application/pil", "secret", 400}, {"GET", "/trunk/missing", "application/pil", "secret", 404}, {"GET", "/trunk/demo/../etc", "application/pil", "secret", 400}} {
		if got := get(tc.method, tc.path, tc.accept, tc.token); got.Code != tc.status {
			t.Fatal(tc, got.Code, got.Body.String())
		}
	}
	if dir := os.Getenv("PIL_TRUNK_FIXTURE_DIR"); dir != "" {
		if err = os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "bootstrap.pil"), w.Body.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "bootstrap.version"), []byte(w.Header().Get("Definition-Version")), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
