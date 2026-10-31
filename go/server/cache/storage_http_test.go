package cache

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestStorageSnapshotLiveSealedAndReadOnly(t *testing.T) {
	l := testLayout(t, false)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Minute)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Append(Write, testFrame(t, l, start, 1, false)); err != nil {
		t.Fatal(err)
	}
	check := func() storageSnapshot {
		t.Helper()
		h := &HTTP{Store: s, Token: "secret"}
		req := httptest.NewRequest("GET", "/storage", nil)
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result storageSnapshot
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		var events uint64
		var bytes float64
		for _, sample := range result.Samples {
			events += sample.Events
			bytes += sample.Bytes
			if sample.Hour != start.UnixMilli() {
				t.Fatal(sample)
			}
		}
		if events != 1 || result.Allocated == 0 || math.Abs(bytes+float64(result.Unattributed)-float64(result.Allocated)) > 1 {
			t.Fatalf("bad snapshot %+v", result)
		}
		return result
	}
	check()
	now = start.Add(2 * time.Hour)
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	before := len(s.branches[Write].runs)
	check()
	if before != len(s.branches[Write].runs) {
		t.Fatal("snapshot mutated storage")
	}
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{{"GET", "/storage", "", 401}, {"POST", "/storage", "secret", 405}, {"GET", "/storage?root=/", "secret", 400}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		(&HTTP{Store: s, Token: "secret"}).ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code)
		}
	}
}
func TestStorageEmptyEndpoint(t *testing.T) {
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	w := httptest.NewRecorder()
	p.operationHandler("").ServeHTTP(w, httptest.NewRequest("GET", "/storage", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result storageSnapshot
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Samples == nil || len(result.Samples) != 0 {
		t.Fatal(result, err)
	}
}

func TestStorageInventoryAggregatesMinorsAndIgnoresClassification(t *testing.T) {
	layout := testLayout(t, false) // No level or access fields at all.
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := at.Add(30 * time.Minute)
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "real", Tenant: "test", Layout: layout, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	inspect := func() storageSnapshot {
		t.Helper()
		w := httptest.NewRecorder()
		p.operationHandler("").ServeHTTP(w, httptest.NewRequest("GET", "/storage", nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got storageSnapshot
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var own uint64
		for _, node := range got.Nodes {
			own += node.OwnAllocated
		}
		if own != got.Allocated {
			t.Fatal("allocation counted twice", own, got.Allocated)
		}
		return got
	}
	empty := inspect()
	if len(empty.Nodes) != 4 || len(empty.Samples) != 0 {
		t.Fatal("empty registered resource missing or invented leaf", empty.Nodes)
	}
	first := p.api.Store
	if _, err = first.Append(Write, testFrame(t, layout, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	def := layout.Definition
	def.Version++
	def.Resource.Minor++
	secondLayout, err := Compile(def)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.install(secondLayout); err != nil {
		t.Fatal(err)
	}
	var second *Store
	for _, api := range p.apis {
		if api.Store.layout.Digest == secondLayout.Digest {
			second = api.Store
		}
	}
	if second == nil {
		t.Fatal("missing minor")
	}
	if _, err = second.Append(Write, testFrame(t, secondLayout, at, 2, false)); err != nil {
		t.Fatal(err)
	}
	// A directory/filename alone cannot register a physical branch or resource.
	if err = os.WriteFile(filepath.Join(first.dir, "error.read.fake"), []byte("not cataloged"), 0600); err != nil {
		t.Fatal(err)
	}
	check := func() {
		got := inspect()
		leaves := 0
		events := uint64(0)
		for _, node := range got.Nodes {
			if node.Kind == "branch" {
				leaves++
				if !slices.Equal(node.Path, []string{"real", "payment", "transactions.v1", "write"}) {
					t.Fatal(node)
				}
			}
		}
		for _, sample := range got.Samples {
			events += sample.Events
			if len(sample.Path) != 4 || sample.Path[3] != "write" {
				t.Fatal(sample)
			}
		}
		if leaves != 1 || events != 2 || got.Shared == 0 {
			t.Fatal(leaves, events, got.Shared)
		}
	}
	check()
	now = at.Add(2 * time.Hour)
	if err = first.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = second.Flush(); err != nil {
		t.Fatal(err)
	}
	check()
}
