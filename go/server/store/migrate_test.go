package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"trie-trunk/go/codec"
)

func TestMigratePreservesSourceAndPopulation(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy2", true: "format3"}[modern], func(t *testing.T) { testMigratePreservesSourceAndPopulation(t, modern) })
	}
}
func testMigratePreservesSourceAndPopulation(t *testing.T, modern bool) {
	c, rev, data, now := fixture(t)
	if modern {
		d, err := codec.ParseDefinition(data, rev)
		if err != nil {
			t.Fatal(err)
		}
		d.Storage = &codec.StoragePolicy{Format: 3, Boundary: "resource", Bucket: "hour"}
		rev, data, err = d.Prepare()
		if err != nil {
			t.Fatal(err)
		}
	}
	m := mustOpen(t, c, rev, data)
	for i := 0; i < 31; i++ {
		_, b := payload(t, now.Add(-time.Duration(i)*time.Hour), 100+uint64(i), 5)
		if _, e := m.Append(rev, b); e != nil {
			t.Fatal(e)
		}
	}
	if e := m.Close(); e != nil {
		t.Fatal(e)
	}
	original := map[string][]byte{}
	filepath.WalkDir(c.Root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			t.Fatal(e)
		}
		if !d.IsDir() {
			b, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			original[p] = b
		}
		return nil
	})
	dest := filepath.Join(t.TempDir(), "events")
	report, e := Migrate(context.Background(), MigrationOptions{Source: c.Root, Destination: dest, Timezone: "America/New_York"})
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Schemas) != 1 || report.Schemas[0].Events != 31 {
		t.Fatal(report)
	}
	for p, b := range original {
		got, e := os.ReadFile(p)
		if e != nil || string(got) != string(b) {
			t.Fatalf("source changed: %s %v", p, e)
		}
	}
	c.Root = dest
	n, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer n.Close()
	if n.schemas[report.Schemas[0].New.Digest].definition.Storage.Format != 4 {
		t.Fatal("migration did not select REF16 format")
	}
	var count int
	e = n.walkRows(context.Background(), n.schemas[report.Schemas[0].New.Digest], func(_ *partition, _ row, _ []byte) error { count++; return nil })
	if e != nil || count != 31 {
		t.Fatal(count, e)
	}
	if _, e = Migrate(context.Background(), MigrationOptions{Source: report.Source, Destination: dest}); e == nil {
		t.Fatal("overwrote destination")
	}
}
func TestMigrateRefusesLiveWriter(t *testing.T) {
	c, rev, data, _ := fixture(t)
	m := mustOpen(t, c, rev, data)
	defer m.Close()
	dest := filepath.Join(t.TempDir(), "events")
	if _, e := Migrate(context.Background(), MigrationOptions{Source: c.Root, Destination: dest}); e == nil {
		t.Fatal("migrated live writer")
	}
	if _, e := os.Stat(dest); !os.IsNotExist(e) {
		t.Fatal("published failed migration")
	}
}
