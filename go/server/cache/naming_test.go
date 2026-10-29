package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObsoleteSharedTrieNameCannotSilentlyResetReferences(t *testing.T) {
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: testLayout(t, false)}
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	dir, root := s.dir, s.root
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, "registry.map"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "state.tri"), filepath.Join(dir, "resource.tri")); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(c); err == nil {
		s.Close()
		t.Fatal("opened obsolete shared trie as a fresh state trie")
	} else if !strings.Contains(err.Error(), "recreate the cache") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.tri")); !os.IsNotExist(err) {
		t.Fatal("created a replacement state trie", err)
	}
	after, err := os.ReadFile(filepath.Join(root, "registry.map"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed open changed the catalog", err)
	}
}

func TestResourceLayoutAndLocalLoss(t *testing.T) {
	l := testLayout(t, false)
	raw, err := json.Marshal(l.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"nuts"`)) || bytes.Contains(raw, []byte(`"domain"`)) || !bytes.Contains(raw, []byte(`"service":"payment"`)) {
		t.Fatal(string(raw))
	}
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(48 * time.Hour) }}
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	for b := Read; b <= Trace; b++ {
		if _, err = s.Append(b, testFrame(t, l, at, 1, false)); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	lost := testFrame(t, l, at.Add(-time.Minute), 2, false)
	var late *LateEventError
	if _, err = s.Append(Write, lost); !errors.As(err, &late) {
		t.Fatal(err)
	}
	paths := []string{"registry.cfg", "registry.map", "resource.tri", "payment/transactions.v1/collection.idx", "payment/transactions.v1/collection.state"}
	for _, name := range []string{"resource.rx", "state.tri", "resource.stats", "lost.log"} {
		paths = append(paths, "payment/transactions.v1/0/"+name)
	}
	for b := Read; b <= Trace; b++ {
		for _, ext := range []string{"idx", "pods", "tri", "seed"} {
			paths = append(paths, "payment/transactions.v1/0/"+b.Name()+"."+ext)
		}
		if b.Aggregates() {
			paths = append(paths, "payment/transactions.v1/0/"+b.Name()+".sum")
		}
	}
	// stats are published on Close, then read back with all other metadata.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err = os.Stat(filepath.Join(s.root, path)); err != nil {
			t.Fatal(path, err)
		}
	}
	if _, err = os.Stat(filepath.Join(s.root, "lost.log")); !os.IsNotExist(err) {
		t.Fatal("loss file escaped the resource minor")
	}
	s, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if err = s.Rejections(func(r Rejection) error {
		n++
		if !bytes.Equal(r.PIL, lost) || r.DefinitionVersion == "" || r.Branch != "write" {
			t.Fatal("lost provenance or original event")
		}
		return nil
	}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if locations, err := s.ObjectLocations(testObject(42)); err != nil || len(locations) != 1 {
		t.Fatal("object lookup after restart", err)
	}
}
