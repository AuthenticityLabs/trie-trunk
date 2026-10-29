package cache

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// Compare event contents across the ingress-to-storage identity boundary.
func sameSeedData(t testing.TB, l *Layout, a, b []byte) bool {
	t.Helper()
	var x, y Frame
	if err := l.Scan(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := l.Scan(b, &y); err != nil {
		t.Fatal(err)
	}
	if eventTime(x.Values[l.Event].Bytes) != eventTime(y.Values[l.Event].Bytes) {
		return false
	}
	for i := range x.Values {
		if i == l.Event || i == l.Record || i == l.Time || i == l.Leaf || i == l.Shared {
			continue
		}
		if !l.equal(i, x.Values[i], y.Values[i]) {
			return false
		}
	}
	return true
}
func TestOrdinalIngressSealRestartAndIndexOnlyLeaf(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Minute)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }, ObjectIndex: true}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	off := false
	s.policy.SearchIndex = &off
	originals := [][]byte{testFrame(t, l, at.Add(time.Minute), 10, false), testFrame(t, l, at, 20, false)}
	for _, raw := range originals {
		if _, err = s.Append(Write, raw); err != nil {
			t.Fatal(err)
		}
	}
	b := s.branches[Write]
	pending := b.pendingWindow(nil, at, at.Add(time.Hour))
	if len(pending) != 2 {
		t.Fatal("pending count", len(pending))
	}
	var f Frame
	for _, p := range pending {
		if err = l.Scan(p.frame, &f); err != nil {
			t.Fatal(err)
		}
		id := f.Values[l.Event].Bytes
		if id[6] != 0x70 || id[7] != 0 || id[8] != 0x80 || !bytes.Equal(id[9:13], make([]byte, 4)) {
			t.Fatal("ingress entropy retained")
		}
		if f.Values[l.Record].State != Direct || len(f.Values[l.Record].Bytes) != 3 {
			t.Fatal("record is not fixed U24")
		}
		if f.Values[l.Leaf].State != Absent {
			t.Fatal("leaf duplicated in staged PIL")
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.policy.SearchIndex = &off
	// A third event shares a millisecond; recovery must not reuse a provisional ID.
	if _, err = s.Append(Write, testFrame(t, l, at, 30, false)); err != nil {
		t.Fatal(err)
	}
	if s.branches[Write].live.count(0) != 3 {
		t.Fatal("provisional identity collision after restart")
	}
	now = at.Add(2 * time.Hour)
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		b = s.branches[Write]
		if len(b.runs) != 1 {
			t.Fatal("hour count", len(b.runs))
		}
		run := b.runs[0]
		raw, e := readBytes(b.pods, run.PodsStart-b.podsRetired, run.PodsEnd-b.podsRetired)
		if e != nil {
			t.Fatal(e)
		}
		pac, e := decodeStoredPAC(l, run, raw)
		if e != nil {
			t.Fatal(e)
		}
		for n, member := range pac.Members {
			full, e := ResolvePAC(l, pac.Common, member)
			if e != nil {
				t.Fatal(e)
			}
			if e = l.Scan(full, &f); e != nil {
				t.Fatal(e)
			}
			if f.Values[l.Record].State != Direct || len(f.Values[l.Record].Bytes) != 3 || unsigned(f.Values[l.Record].Bytes) != uint64(n) {
				t.Fatalf("ordinal %d: %+v", n, f.Values[l.Record])
			}
			for _, i := range []int{l.Event, l.Time, l.Leaf, l.Path[0]} {
				if f.Values[i].State != Absent {
					t.Fatalf("duplicated field %s", l.Definition.Fields[i].Name)
				}
			}
			if b.rowLeaf(run.Start+uint64(n)) == 0 {
				t.Fatal("missing index leaf")
			}
		}
		count := 0
		e = s.Search(context.Background(), Query{Branch: Write, From: at, To: at.Add(time.Hour)}, func(_ uint64, raw []byte) error {
			if e := l.Scan(raw, &f); e != nil {
				return e
			}
			if unsigned(f.Values[l.Record].Bytes) != uint64(count) {
				t.Fatal("wrong materialized ordinal")
			}
			if f.Values[l.Path[0]].State != Direct {
				t.Fatal("state not resolved")
			}
			count++
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		if count != 3 {
			t.Fatal("search count", count)
		}
	}
	check()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	check()
}

func TestOrdinalTimeTrieAddressBounds(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	run := timeRun(at, 1, maxHourRecords)
	directory, err := buildTimeDirectory([]Run{run}, at, at+TimeHourMS, run.End, [16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	for _, ordinal := range []uint32{0, 1, 65535, 65536, 0xffffff} {
		row, e := directory.resolveReference(collectionAddress{0, ordinal + 1})
		if e != nil || row != run.Start+uint64(ordinal) {
			t.Fatal(ordinal, row, e)
		}
	}
	if _, e := directory.resolveReference(collectionAddress{0, (1 << 24) + 1}); e == nil {
		t.Fatal("accepted record overflow")
	}
}

func TestOrdinalCollectionBoundaryPreservesEncoding(t *testing.T) {
	p := newCollectionPage(objectBinding{})
	id := collectionTestID(1)
	for n, record := range []uint32{0, 1, 0xffffff} {
		a := collectionAddress{0, record + 1}
		if err := p.put(id, uint64(n), a.packed()); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.put(id, 4, 0); err == nil {
		t.Fatal("collection record zero must remain invalid")
	}
	if (collectionAddress{0, 1}).packed() != 1 || (collectionAddress{7, 9}).packed() != uint64(7)<<32|9 {
		t.Fatal("collection address encoding changed")
	}
	p.seal()
	if err := p.validate(objectBinding{}); err != nil {
		t.Fatal(err)
	}
	slot := p.find(id[:])
	for n, record := range []uint32{0, 1, 0xffffff} {
		raw := p.reference(slot, uint64(n))
		if raw == 0 || unpackCollection(raw) != (collectionAddress{0, record + 1}) {
			t.Fatal("lost record", record)
		}
	}
}
