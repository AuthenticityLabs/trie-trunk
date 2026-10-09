package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
	"trie-trunk/go/codec"
)

func modernFixture(t *testing.T, zone string) (Config, codec.Revision, []byte, *time.Time) {
	c, _, _, now := fixture(t)
	c.MaxPartitions = 16
	d := testDefinition()
	d.Timezone = zone
	d.Storage = &codec.StoragePolicy{Format: 3, Boundary: "resource", Bucket: "hour"}
	d.Fields[4] = codec.Field{Name: "resource", Enums: map[uint16]string{1: "secrets", 2: "jobs"}}
	d.Path = []codec.Key{3, 4, 14}
	rev, data, err := d.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	return c, rev, data, now
}
func modernPayload(t *testing.T, at time.Time, resource uint64) (codec.UUIDv7, []byte) {
	id, data := payload(t, at, 100, 5)
	v, err := codec.Decode(data, codec.CBOR)
	if err != nil {
		t.Fatal(err)
	}
	if resource > 0 {
		v[4] = resource
	}
	data, err = codec.MarshalCompact(v, codec.CBOR)
	if err != nil {
		t.Fatal(err)
	}
	return id, data
}
func TestEventsMaturityLateArrivalAndRecovery(t *testing.T) {
	c, rev, data, now := modernFixture(t, "America/New_York")
	*now = time.Date(2026, 9, 23, 12, 10, 0, 0, time.UTC)
	m := mustOpen(t, c, rev, data)
	_, a := modernPayload(t, now.Add(-20*time.Minute), 1)
	_, b := modernPayload(t, now.Add(-21*time.Minute), 1)
	for _, v := range [][]byte{a, b} {
		if _, err := m.Append(rev, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	s := m.schemas[rev.Digest]
	scopes, err := s.scopes(c.Root, m.config.Tap)
	if err != nil || len(scopes) != 1 {
		t.Fatal(scopes, err)
	}
	dir := filepath.Join(versionDir(c.Root, m.config.Tap, rev.Version), scopes[0].Dir)
	before, err := os.ReadFile(filepath.Join(dir, "events.pop"))
	if err != nil {
		t.Fatal(err)
	}
	_, compact, err := readScope(dir)
	if err != nil || compact {
		t.Fatal("premature", err)
	}
	old, err := os.Stat(filepath.Join(dir, "events.idx"))
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(5 * time.Minute)
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	_, compact, err = readScope(dir)
	if err != nil || !compact {
		t.Fatal("not mature", err)
	}
	next, _ := os.Stat(filepath.Join(dir, "events.idx"))
	if old.Size()-next.Size() != 8 {
		t.Fatal(old.Size(), next.Size())
	}
	after, _ := os.ReadFile(filepath.Join(dir, "events.pop"))
	if !bytes.Equal(before, after) {
		t.Fatal("population rewritten")
	}
	_, late := modernPayload(t, now.Add(-22*time.Minute), 1)
	if duplicate, err := m.Append(rev, late); err != nil || duplicate {
		t.Fatal(duplicate, err)
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	count := 0
	if err = m.Scan(rev, *now, func(codec.Record) error { count++; return nil }); err != nil || count != 3 {
		t.Fatal(count, err)
	}
	sums, err := m.Summaries(rev, *now)
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for _, v := range sums {
		for _, g := range v.Groups {
			total += g.Count
		}
	}
	if total != 3 {
		t.Fatal("aggregate double count or loss", total)
	}
	if duplicate, err := m.Append(rev, a); err != nil || !duplicate {
		t.Fatal(duplicate, err)
	}
}
func TestEventsOwnersAndCrossOwnerDedup(t *testing.T) {
	c, rev, data, now := modernFixture(t, "UTC")
	m := mustOpen(t, c, rev, data)
	_, a := modernPayload(t, now.Add(-2*time.Hour), 1)
	for _, resource := range []uint64{1, 2, 0} {
		_, v := modernPayload(t, now.Add(-2*time.Hour), resource)
		if _, err := m.Append(rev, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Append(rev, a); err != nil {
		t.Fatal(err)
	}
	fields, err := codec.Decode(a, codec.CBOR)
	if err != nil {
		t.Fatal(err)
	}
	fields[4] = uint64(2)
	changed, err := codec.MarshalCompact(fields, codec.CBOR)
	if err != nil {
		t.Fatal(err)
	}
	if dup, err := m.Append(rev, changed); err != nil || !dup {
		t.Fatal("cross-owner duplicate", dup, err)
	}
	if err = m.Flush(); err != nil {
		t.Fatal(err)
	}
	scopes, err := m.schemas[rev.Digest].scopes(c.Root, m.config.Tap)
	if err != nil || len(scopes) != 3 {
		t.Fatal(scopes, err)
	}
	for _, sc := range scopes {
		_, compact, err := readScope(filepath.Join(versionDir(c.Root, m.config.Tap, rev.Version), sc.Dir))
		if err != nil || !compact {
			t.Fatal(err)
		}
	}
}
func TestEventsDSTBucketIdentity(t *testing.T) {
	c, rev, data, _ := modernFixture(t, "America/New_York")
	m := mustOpen(t, c, rev, data)
	s := m.schemas[rev.Digest]
	first, _ := codec.NewUUIDv7(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC))
	second, _ := codec.NewUUIDv7(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC))
	a, b := s.scope(first, 1), s.scope(second, 1)
	if a.Dir == b.Dir || !a.End.Equal(b.Start) {
		t.Fatal(a, b)
	}
}

func TestEventsCivilDayTransitions(t *testing.T) {
	for _, test := range []struct {
		zone, instant string
		hours         float64
	}{
		{"America/New_York", "2026-03-08T12:00:00Z", 23},
		{"America/New_York", "2026-11-01T12:00:00Z", 25},
		{"Australia/Lord_Howe", "2026-10-04T05:00:00Z", 23.5},
		{"America/Sao_Paulo", "2018-11-04T12:00:00Z", 23},
		{"Pacific/Apia", "2011-12-30T12:00:00Z", 24},
		{"+05:45", "2026-09-29T12:00:00Z", 24},
	} {
		t.Run(test.zone, func(t *testing.T) {
			_, rev, data, _ := modernFixture(t, test.zone)
			d, e := codec.ParseDefinition(data, rev)
			if e != nil {
				t.Fatal(e)
			}
			s, e := compile(d, rev)
			if e != nil {
				t.Fatal(e)
			}
			at, _ := time.Parse(time.RFC3339, test.instant)
			lo, hi := s.dayBounds(at)
			if hi.Sub(lo).Hours() != test.hours || at.Before(lo) || !at.Before(hi) {
				t.Fatal(lo, hi)
			}
			for current := lo; current.Before(hi); current = current.Add(15 * time.Minute) {
				id, e := codec.NewUUIDv7(current)
				if e != nil {
					t.Fatal(e)
				}
				sc := s.scope(id, 0)
				if current.Before(sc.Start) || !current.Before(sc.End) || sc.End.After(hi) {
					t.Fatal(current, sc)
				}
			}
		})
	}
}
