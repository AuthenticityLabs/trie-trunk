package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func objectDefinition(name string) codec.Definition {
	return codec.Definition{Object: name, Version: 1 << 20, Timezone: "America/New_York",
		Storage: &codec.StoragePolicy{Format: 3, Boundary: "region", Bucket: "hour"},
		Fields: codec.Schema{
			0: {Name: "id", Encoding: "uuidv7"},
			1: {Name: "region", Enums: map[uint16]string{1: "east", 2: "west"}},
			2: {Name: "state", Enums: map[uint16]string{1: "open", 2: "closed"}},
			3: {Name: "value"},
		}, Path: []codec.Key{1, 2},
	}
}
func installObject(t *testing.T, m *Manager, name string) codec.Revision {
	t.Helper()
	rev, data, err := objectDefinition(name).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Install(rev, data); err != nil {
		t.Fatal(err)
	}
	return rev
}
func readStatistics(t *testing.T, m *Manager, rev codec.Revision) TrieStatistics {
	t.Helper()
	s := m.schemas[rev.Digest]
	data, err := os.ReadFile(filepath.Join(s.directory(m.config.Root, m.config.Tap), "statistics"))
	if err != nil {
		t.Fatal(err)
	}
	var stats TrieStatistics
	if err = json.Unmarshal(data, &stats); err != nil {
		t.Fatal(err)
	}
	return stats
}
func objectPayload(t *testing.T, at time.Time, region, state uint64) []byte {
	t.Helper()
	id, err := codec.NewUUIDv7(at)
	if err != nil {
		t.Fatal(err)
	}
	data, err := codec.MarshalCompact(codec.Record{0: id, 1: region, 2: state, 3: "dynamic"}, codec.PIL)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestObjectIdentityRegistryAndMinorAssignment(t *testing.T) {
	c, _, _, _ := fixture(t)
	c.Tap = "demo"
	m, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	a := installObject(t, m, "orders")
	b := installObject(t, m, "inventory")
	if a.Version != b.Version || a.Digest == b.Digest {
		t.Fatal(a, b)
	}
	if _, ok := m.Revision(a.Version); ok {
		t.Fatal("ambiguous version lookup")
	}
	for _, rev := range []codec.Revision{a, b} {
		if got, ok := m.FindRevision(rev); !ok || got != rev {
			t.Fatal(got, ok)
		}
	}
	changed := objectDefinition("orders")
	changed.Fields[3] = codec.Field{Name: "different"}
	conflict, raw, err := changed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Install(conflict, raw); !errors.Is(err, ErrConflict) {
		t.Fatal("immutable identity overwritten", err)
	}
	next, err := m.Register(changed)
	if err != nil || next.Version != 1<<20|1 {
		t.Fatal(next, err)
	}
	reused, err := m.Register(changed)
	if err != nil || reused != next {
		t.Fatal(reused, err)
	}
	for _, name := range []string{"events.rx", "events.tri", "statistics"} {
		if _, err = os.Stat(filepath.Join(c.Root, "demo", "orders.v1", "0", name)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(c.Root, "demo", "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry ObjectRegistry
	if err = json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	if registry.Tap != "demo" || len(registry.Objects) != 2 || registry.Objects[1].Name != "orders" || len(registry.Objects[1].Definitions) != 2 {
		t.Fatal(registry)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	catalogs, err := ReadCatalogs(context.Background(), c.Root, "demo")
	if err != nil || len(catalogs) != 3 {
		t.Fatal(catalogs, err)
	}
	if _, ok := m.FindRevision(a); !ok {
		t.Fatal("lost identity on reopen")
	}
	// A definition cannot be moved underneath an unrelated object's directory.
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(c.Root, "demo", "orders.v1"), filepath.Join(c.Root, "demo", "wrong.v1")); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadCatalogs(context.Background(), c.Root, "demo"); err == nil {
		t.Fatal("accepted mismatched object path")
	}
}

func TestTrieStatisticsRollingWindowRecoveryAndDuplicates(t *testing.T) {
	for _, format := range []uint32{3, 4} {
		t.Run(fmt.Sprint(format), func(t *testing.T) { testTrieStatisticsRollingWindowRecoveryAndDuplicates(t, format) })
	}
}
func testTrieStatisticsRollingWindowRecoveryAndDuplicates(t *testing.T, format uint32) {
	now := time.Date(2026, 9, 29, 12, 16, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Tap: "demo", Now: func() time.Time { return now }, BatchRecords: 2, MaxPartitions: 1, MaxAge: 60 * 24 * time.Hour}
	m, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	definition := objectDefinition("orders") // No log level or duration field at all.
	definition.Storage.Format = format
	rev, raw, err := definition.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Install(rev, raw); err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-statisticsWindow)
	data := [][]byte{
		objectPayload(t, cutoff.Add(-time.Millisecond), 1, 1), // outside window, same hour
		objectPayload(t, cutoff, 1, 1),
		objectPayload(t, now.Add(-3*time.Hour), 1, 1),
		objectPayload(t, now.Add(-2*time.Hour), 2, 1),
		objectPayload(t, now.Add(-time.Minute), 2, 2),
		objectPayload(t, now.Add(30*time.Second), 2, 2), // accepted skew, not yet in window
	}
	for _, b := range data {
		if dup, e := m.Append(rev, b); e != nil || dup {
			t.Fatal(dup, e)
		}
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	check := func(m *Manager, want uint64) {
		t.Helper()
		stats := readStatistics(t, m, rev)
		if stats.Events != want {
			t.Fatalf("events %d want %d: %+v", stats.Events, want, stats)
		}
		if !stats.From.Equal(now.Add(-statisticsWindow)) || !stats.Through.Equal(now) {
			t.Fatal(stats.From, stats.Through)
		}
	}
	check(m, 4)
	stats := readStatistics(t, m, rev)
	if stats.MeanDepth != 2 || stats.ActiveNodes != 5 || len(stats.Fields) != 2 {
		t.Fatal(stats)
	}
	if stats.Fields[0].Cardinality != 2 || math.Abs(stats.Fields[0].Entropy-1) > 1e-10 {
		t.Fatal(stats.Fields)
	}
	if stats.Fields[1].RepeatedValues != 1 || math.Abs(stats.Fields[1].ConditionalEntropy-0.5) > 1e-10 {
		t.Fatal(stats.Fields)
	}
	for _, b := range data {
		if dup, e := m.Append(rev, b); e != nil || !dup {
			t.Fatal(dup, e)
		}
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	check(m, 4)
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	// The snapshot is disposable. Recovery derives counts from committed indexes.
	if err = os.WriteFile(filepath.Join(c.Root, "demo", "orders.v1", "0", "statistics"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	check(m, 4)
	// Move past the oldest boundary row AND the previously future row.
	now = now.Add(time.Minute)
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	check(m, 4)
	late := objectPayload(t, now.Add(-3*time.Hour), 1, 2)
	if dup, e := m.Append(rev, late); e != nil || dup {
		t.Fatal(dup, e)
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	check(m, 5)
	now = now.Add(31 * 24 * time.Hour)
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	check(m, 0)
	if len(readStatistics(t, m, rev).Hours) != 0 {
		t.Fatal("expired buckets retained")
	}
}
