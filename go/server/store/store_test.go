package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"trie-trunk/go/codec"
)

func fixture(t *testing.T) (Config, codec.Revision, []byte, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 23, 12, 16, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Now: func() time.Time { return now }, BatchRecords: 2}
	d := testDefinition()
	d.Version = 3<<20 | 42
	rev, data, e := d.Prepare()
	if e != nil {
		t.Fatal(e)
	}
	return c, rev, data, &now
}

func payload(t *testing.T, at time.Time, duration uint64, level uint64) (codec.UUIDv7, []byte) {
	t.Helper()
	id, e := codec.NewUUIDv7(at)
	if e != nil {
		t.Fatal(e)
	}
	b, e := codec.MarshalCompact(codec.Record{8: id, 12: duration, 2: uint64(1), 3: level, 14: uint64(401), 19: "/secrets"}, codec.CBOR)
	if e != nil {
		t.Fatal(e)
	}
	return id, b
}

func mustOpen(t *testing.T, c Config, rev codec.Revision, d []byte) *Manager {
	t.Helper()
	m, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	if e = m.Install(rev, d); e != nil {
		t.Fatal(e)
	}
	return m
}

func TestRoundTripDedupRecoveryAndLateCorrections(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	at := now.Add(-15 * time.Minute)
	id, a := payload(t, at, 11, 5)
	_, b := payload(t, at.Add(-time.Second), 23, 5)
	for _, data := range [][]byte{a, b} {
		if dup, e := m.Append(rev, data); e != nil || dup {
			t.Fatal(dup, e)
		}
	}
	if dup, e := m.Append(rev, a); e != nil || !dup {
		t.Fatal(dup, e)
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	count := 0
	var last codec.UUIDv7
	if e := m.Scan(rev, at, func(r codec.Record) error {
		got := r[8].(codec.UUIDv7)
		if count > 0 && bytes.Compare(last[:], got[:]) >= 0 {
			t.Fatal("run not sorted")
		}
		last = got
		count++
		if r[19] != "/secrets" || r[3] != uint64(5) {
			t.Fatal(r)
		}
		return nil
	}); e != nil || count != 2 {
		t.Fatal(count, e)
	}
	sums, e := m.Summaries(rev, at)
	if e != nil || len(sums) != 1 {
		t.Fatal(sums, e)
	}
	var leaf uint32
	for k, v := range sums[0].Groups {
		leaf = k
		if v.Count != 2 || v.Sum != 34 || v.Min != 11 || v.Max != 23 {
			t.Fatal(v)
		}
	}
	if leaf == 0 {
		t.Fatal("expected shared trie path")
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	m = mustOpen(t, c, rev, d)
	if dup, e := m.Append(rev, a); e != nil || !dup {
		t.Fatal(dup, e)
	}
	_, late := payload(t, at.Add(time.Second), 7, 5)
	if _, e = m.Append(rev, late); e != nil {
		t.Fatal(e)
	}
	if e = m.Flush(); e != nil {
		t.Fatal(e)
	}
	sums, e = m.Summaries(rev, at)
	if e != nil || len(sums) != 2 {
		t.Fatal(len(sums), e)
	}
	if sums[1].Groups[leaf].Count != 1 || sums[1].Groups[leaf].Sum != 7 {
		t.Fatal(sums)
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	m = mustOpen(t, c, rev, d)
	sums, e = m.Summaries(rev, at)
	if e != nil || len(sums) != 2 {
		t.Fatal(len(sums), e)
	}
	path := filepath.Join(versionDir(c.Root, "0", rev.Version), dayPath(dayOf(id)))
	for _, name := range []string{"log.idx", "log.pop", "log.sum"} {
		if _, e = os.Stat(filepath.Join(path, name)); e != nil {
			t.Fatal(e)
		}
	}
}

func TestNMinusTwoAndRestartRebuild(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	at := *now
	_, b := payload(t, at, 3, 5)
	if _, e := m.Append(rev, b); e != nil {
		t.Fatal(e)
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	s, e := m.Summaries(rev, at)
	if e != nil || len(s) != 0 {
		t.Fatal(s, e)
	}
	m.Close()
	*now = now.Add(10 * time.Minute)
	m = mustOpen(t, c, rev, d)
	s, e = m.Summaries(rev, at)
	if e != nil || len(s) != 1 {
		t.Fatal(s, e)
	}
}

func TestSchemaConflictLimitsAndLock(t *testing.T) {
	c, rev, d, _ := fixture(t)
	m := mustOpen(t, c, rev, d)
	if _, e := Open(c); e == nil {
		t.Fatal("second writer acquired lock")
	}
	other := testDefinition()
	other.Version = rev.Version
	other.Dictionary = "different"
	r, b, _ := other.Prepare()
	if e := m.Install(r, b); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e := m.Install(rev, append(d, 0)); e == nil {
		t.Fatal("accepted bad digest")
	}
	m.Close()
}

func TestPartialTailRecoveryAndCorruption(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	id, b := payload(t, *now, 3, 5)
	m.Append(rev, b)
	if e := m.Close(); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(versionDir(c.Root, "0", rev.Version), dayPath(dayOf(id)), "log.idx")
	stat, _ := os.Stat(path)
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.Write([]byte{0, 0, 0, 30, 0, 0, 0, 0, 1})
	f.Close()
	m = mustOpen(t, c, rev, d)
	m.Close()
	after, _ := os.Stat(path)
	if after.Size() != stat.Size() {
		t.Fatal("partial tail not removed")
	}
	f, _ = os.OpenFile(path, os.O_RDWR, 0600)
	f.WriteAt([]byte{255}, fileHeaderSize+8)
	f.Close()
	if _, e = Open(c); e == nil {
		t.Fatal("corrupt complete block accepted")
	}
}

func TestEvictionTrieBoundAndConcurrentAppend(t *testing.T) {
	c, rev, d, now := fixture(t)
	c.MaxNodes = 1
	c.MaxPartitions = 1
	m := mustOpen(t, c, rev, d)
	for i := 0; i < 3; i++ {
		_, b := payload(t, now.Add(-time.Duration(i)*24*time.Hour), 3, 5)
		if _, e := m.Append(rev, b); e != nil {
			t.Fatal(e)
		}
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	count := 0
	if e := m.Scan(rev, *now, func(r codec.Record) error {
		count++
		if r[3] != uint64(5) {
			t.Fatal(r)
		}
		return nil
	}); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}

func TestInvalidRecords(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	_, valid := payload(t, *now, 3, 5)
	r, _ := codec.Decode(valid, codec.CBOR)
	cases := []codec.Record{{8: []byte{1}, 12: 3}, {8: r[8], 12: -1}, {8: r[8], 12: 0, 3: 999}, {8: r[8], 12: 0, 999: 1}}
	for _, v := range cases {
		b, _ := codec.MarshalCompact(v, codec.CBOR)
		if _, e := m.Append(rev, b); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	_, future := payload(t, now.Add(time.Hour), 1, 5)
	if _, e := m.Append(rev, future); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}

func TestRecoverAfterIndexCommitBeforeSummary(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	_, b := payload(t, now.Add(-time.Hour), 3, 5)
	m.Append(rev, b)

	for _, p := range m.partitions {
		if _, e := p.commitPending(); e != nil {
			t.Fatal(e)
		}
		p.idx.Sync()
		p.closeFiles()
	}
	m.lock.Close()
	m.closed = true
	m = mustOpen(t, c, rev, d)
	s, e := m.Summaries(rev, *now)
	if e != nil || len(s) != 1 {
		t.Fatal(s, e)
	}
	for _, a := range s[0].Groups {
		if a.Count != 1 {
			t.Fatal(a)
		}
	}
}

func TestVersionSplitsAndGroups(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	other := testDefinition()
	other.Version = rev.Version + 1
	r, data, _ := other.Prepare()
	if e := m.Install(r, data); e != nil {
		t.Fatal(e)
	}
	_, b := payload(t, now.Add(-time.Hour), 2, 5)
	for _, revision := range []codec.Revision{rev, r} {
		if _, e := m.Append(revision, b); e != nil {
			t.Fatal(e)
		}
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	for _, revision := range []codec.Revision{rev, r} {
		s, e := m.Summaries(revision, *now)
		if e != nil || len(s) != 1 {
			t.Fatal(e, s)
		}
	}
}

func BenchmarkAppend(b *testing.B) {
	now := time.Now().UTC()
	m, e := Open(Config{Root: b.TempDir(), Now: func() time.Time { return now }, BatchRecords: 1024})
	if e != nil {
		b.Fatal(e)
	}
	defer m.Close()
	d := testDefinition()
	rev, data, _ := d.Prepare()
	if e = m.Install(rev, data); e != nil {
		b.Fatal(e)
	}
	primeId, _ := codec.NewUUIDv7(now)
	prime, _ := codec.MarshalCompact(codec.Record{8: primeId, 12: uint64(10), 2: uint64(1), 3: uint64(3)}, codec.CBOR)
	if _, e = m.Append(rev, prime); e != nil {
		b.Fatal(e)
	}
	if e = m.Flush(); e != nil {
		b.Fatal(e)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, _ := codec.NewUUIDv7(now)
		payload, _ := codec.MarshalCompact(codec.Record{8: id, 12: uint64(10), 2: uint64(1), 3: uint64(3)}, codec.CBOR)
		if _, e = m.Append(rev, payload); e != nil {
			b.Fatal(e)
		}
	}
	if e = m.Flush(); e != nil {
		b.Fatal(e)
	}
	b.StopTimer()
}

func TestConcurrentWriters(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	records := make([][]byte, 64)
	for i := range records {
		_, records[i] = payload(t, *now, uint64(i), 5)
	}
	done := make(chan error, len(records))
	for _, data := range records {
		go func(b []byte) { _, e := m.Append(rev, b); done <- e }(data)
	}
	for range records {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	n := 0
	if e := m.Scan(rev, *now, func(codec.Record) error { n++; return nil }); e != nil || n != len(records) {
		t.Fatal(n, e)
	}
}

func TestRejectMalformedNestedError(t *testing.T) {
	c, rev, d, now := fixture(t)
	m := mustOpen(t, c, rev, d)
	id, _ := codec.NewUUIDv7(*now)
	for _, bad := range []any{"error", map[uint16]any{0: 999}, map[uint16]any{3: "500"}} {
		b, _ := codec.MarshalCompact(codec.Record{8: id, 12: 1, 16: bad}, codec.CBOR)
		if _, e := m.Append(rev, b); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
}

func TestCapacityDoesNotLoseCommittedRows(t *testing.T) {
	c, rev, d, now := fixture(t)
	c.MaxRuns = 1
	c.BatchRecords = 1
	m := mustOpen(t, c, rev, d)
	_, a := payload(t, *now, 1, 5)
	_, b := payload(t, *now, 2, 5)
	if _, e := m.Append(rev, a); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Append(rev, b); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	n := 0
	if e := m.Scan(rev, *now, func(codec.Record) error { n++; return nil }); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func FuzzDecodeRecord(f *testing.F) {
	d := testDefinition()
	rev, _, _ := d.Prepare()
	s, e := compile(d, rev)
	if e != nil {
		f.Fatal(e)
	}
	id, _ := codec.NewUUIDv7(time.Now())
	b, _ := codec.MarshalCompact(codec.Record{8: id, 12: 0}, codec.CBOR)
	f.Add(b)
	f.Add([]byte{0xa1, 0x08, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = s.decode(b) })
}

func TestOrderedInsertionAndOutOfOrderReplay(t *testing.T) {
	c, rev, d, now := fixture(t)
	c.BatchRecords = 8
	m := mustOpen(t, c, rev, d)
	var records [][]byte
	for _, delta := range []time.Duration{0, -time.Second, -3 * time.Second, -2 * time.Second, time.Second} {
		_, b := payload(t, now.Add(delta), 1, 5)
		records = append(records, b)
		if dup, e := m.Append(rev, b); e != nil || dup {
			t.Fatal(dup, e)
		}
	}
	for _, p := range m.partitions {
		for i := 1; i < len(p.pending); i++ {
			if bytes.Compare(p.pending[i-1].Id[:], p.pending[i].Id[:]) >= 0 {
				t.Fatal("active index not sorted")
			}
		}
	}
	if dup, e := m.Append(rev, records[2]); e != nil || !dup {
		t.Fatal(dup, e)
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	_, late := payload(t, now.Add(-4*time.Second), 1, 5)
	if dup, e := m.Append(rev, late); e != nil || dup {
		t.Fatal(dup, e)
	}
	if dup, e := m.Append(rev, records[2]); e != nil || !dup {
		t.Fatal(dup, e)
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	m.Close()
	m = mustOpen(t, c, rev, d)
	if dup, e := m.Append(rev, late); e != nil || !dup {
		t.Fatal(dup, e)
	}
}

func TestTailSearchAndBinaryFallback(t *testing.T) {
	c, rev, d, now := fixture(t)
	c.BatchRecords = 64
	m := mustOpen(t, c, rev, d)
	var records [][]byte
	for i := 0; i < 32; i++ {
		_, b := payload(t, now.Add(time.Duration(i-40)*time.Second), 1, 5)
		records = append(records, b)
		if _, e := m.Append(rev, b); e != nil {
			t.Fatal(e)
		}
	}
	for _, b := range records {
		if dup, e := m.Append(rev, b); e != nil || !dup {
			t.Fatal("pending replay", dup, e)
		}
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	for _, b := range records {
		if dup, e := m.Append(rev, b); e != nil || !dup {
			t.Fatal("committed replay", dup, e)
		}
	}
	_, between := payload(t, now.Add(-20500*time.Millisecond), 1, 5)
	if dup, e := m.Append(rev, between); e != nil || dup {
		t.Fatal(dup, e)
	}
	if e := m.Flush(); e != nil {
		t.Fatal(e)
	}
	n := 0
	if e := m.Scan(rev, *now, func(codec.Record) error { n++; return nil }); e != nil || n != 33 {
		t.Fatal(n, e)
	}
}

func testDefinition() codec.Definition {
	return codec.Definition{Version: 4, Timezone: "UTC", Fields: codec.Schema{
		8: {Name: "id", Encoding: "uuidv7"}, 12: {Name: "duration", Unit: "us"},
		2:  {Name: "category", Enums: map[uint16]string{1: "SUMMARY"}},
		3:  {Name: "level", Enums: map[uint16]string{3: "INFO", 5: "ERROR"}},
		14: {Name: "http_status"}, 19: {Name: "uri"}, 21: {Name: "fields"},
		16: {Name: "error", Object: codec.Schema{0: {Name: "code", Enums: map[uint16]string{1: "UNEXPECTED"}}, 3: {Name: "status"}}},
	}, Path: []codec.Key{3, 14, 2}, Aggregations: []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}}
}
