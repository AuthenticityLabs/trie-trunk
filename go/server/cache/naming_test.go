package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	paths := []string{"registry.cfg", "registry.map", "registry.tri", "payment/transactions.v1/collection.idx", "payment/transactions.v1/collection.state"}
	for _, name := range []string{"resource.map", "resource.tri", "resource.stats", "lost.log"} {
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
