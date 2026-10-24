package cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCollectionIndex(t testing.TB) *objectIndex {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "collection.idx"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return newObjectIndex(f)
}
func TestCollectionSlottedRevisions(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	var id [16]byte
	copy(id[:], testObject(42).Bytes)
	revisions := []uint64{10, 14, 12, 9, 497}
	for n, rev := range revisions {
		from := i.through[k]
		row := uint32(n + 1)
		if err := i.append(k, 2, from, uint64(row)+1, []collectionPosting{{id, rev, collectionAddress{0, row}}}); err != nil {
			t.Fatal(err)
		}
	}
	for n, rev := range revisions {
		a, ok, err := i.lookup(id[:], k, rev)
		if err != nil || !ok || a.Record != uint32(n+1) {
			t.Fatal(rev, a, ok, err)
		}
	}
	if _, ok, err := i.lookup(id[:], k, 11); err != nil || ok {
		t.Fatal("gap", ok, err)
	}
	j, err := loadObjectIndex(i.file)
	if err != nil {
		t.Fatal(err)
	}
	if rev, _, ok, err := j.latest(id[:], k); err != nil || !ok || rev != 497 {
		t.Fatal(rev, ok, err)
	}
	if err = i.append(k, 2, i.through[k], 7, []collectionPosting{{id, 498, collectionAddress{0, 6}}}); err != nil {
		t.Fatal(err)
	}
	if err = i.append(k, 2, i.through[k], 7, []collectionPosting{{id, 65536, collectionAddress{0, 6}}}); err == nil {
		t.Fatal("U16 overflow accepted")
	}
}
func TestCollectionPublicationRecovery(t *testing.T) {
	for _, stage := range []string{"before-root", "after-root"} {
		t.Run(stage, func(t *testing.T) {
			i := testCollectionIndex(t)
			k := objectBinding{}
			var id [16]byte
			if err := i.append(k, 2, 0, 2, []collectionPosting{{id, 0, collectionAddress{0, 1}}}); err != nil {
				t.Fatal(err)
			}
			i.fault = func(at string) error {
				if at == stage {
					return fmt.Errorf("injected")
				}
				return nil
			}
			if err := i.append(k, 2, 2, 3, []collectionPosting{{id, 1, collectionAddress{0, 2}}}); err == nil {
				t.Fatal("fault missed")
			}
			if _, ok, err := i.lookup(id[:], k, 1); err != nil || ok {
				t.Fatal("unpublished memory", ok, err)
			}
			j, err := loadObjectIndex(i.file)
			if err != nil {
				t.Fatal(err)
			}
			_, ok, err := j.lookup(id[:], k, 1)
			if err != nil || ok != (stage == "after-root") {
				t.Fatal("durable publication", ok, err)
			}
			if stage == "before-root" {
				if err = j.append(k, 2, 2, 3, []collectionPosting{{id, 1, collectionAddress{0, 2}}}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCollectionWriteContractAndRebuild(t *testing.T) {
	l := testLayout(t, false)
	d := l.Definition
	d.Fields = append([]Field(nil), d.Fields...)
	d.Fields[l.Revision].Type = "U16"
	var err error
	l, err = Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(time.Minute)
	c := Config{Root: t.TempDir(), Cache: "test", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }}
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	frame := func(n uint32, revision Value, object Value) []byte {
		raw, e := l.Encode(nil, []Value{uuid(at.UnixMilli()+int64(n), n), object, revision, {}, {}, {}, {}})
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	first := frame(1, Uint(254, 2), testObject(42))
	if _, err = s.Append(Write, first); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(Write, frame(2, Uint(254, 2), testObject(42))); err == nil {
		t.Fatal("staged duplicate revision accepted")
	}
	if _, err = s.Append(Write, frame(3, Uint(255, 2), Value{})); err == nil {
		t.Fatal("missing identity accepted")
	}
	token, err := s.Token(testObject(42))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(Write, frame(4, Value{}, testObject(42))); err != nil {
		t.Fatal(err)
	}
	if valid, err := s.StillCurrent(testObject(42), token); err != nil || valid {
		t.Fatal("revision-less write did not invalidate replay", valid, err)
	}
	if len(s.branches[Write].pendingWindow(testObject(42).Bytes, time.UnixMilli(0), time.UnixMilli((1<<48)-1))) != 1 {
		t.Fatal("revision-less write entered pending replay")
	}
	if duplicate, err := s.Append(Write, first); err != nil || !duplicate {
		t.Fatal("identical retry rejected", duplicate, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(Write, frame(5, Uint(254, 2), testObject(42))); err == nil {
		t.Fatal("recovered staged duplicate accepted")
	}
	if _, err = s.Append(Write, frame(6, Uint(255, 2), testObject(42))); err != nil {
		t.Fatal(err)
	}
	now = at.Add(2 * time.Hour)
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(Write, frame(7, Uint(255, 2), testObject(42))); err == nil {
		t.Fatal("committed duplicate revision accepted")
	}
	s.mu.Lock()
	b := s.branches[Write]
	saved := b.time
	b.time = timeDirectory{} // Resident replay does not read the sealed time trie.
	rows, err := s.objectRows(b, testObject(42), at, now)
	b.time = saved
	s.mu.Unlock()
	if err != nil || len(rows) != 2 {
		t.Fatal("independent lookup", rows, err)
	}
	path := s.objects.file.Name()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte{0xff}, 4096+24)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	locations, err := s.ObjectLocations(testObject(42))
	if err != nil || len(locations) != 2 {
		t.Fatal("rebuild", locations, err)
	}
	count := 0
	if err = s.SearchLive(context.Background(), Query{Branch: Write, From: at, To: now}, func([]byte) error { count++; return nil }); err != nil || count != 3 {
		t.Fatal("revision-less write lost from journal", count, err)
	}
	count = 0
	if err = s.History(context.Background(), testObject(42), at, now, func(uint64, []byte) error { count++; return nil }); err != nil || count != 2 {
		t.Fatal("revision-less write entered replay", count, err)
	}
	if !bytes.Equal(s.layout.Digest[:], l.Digest[:]) {
		t.Fatal("definition changed")
	}
}

func BenchmarkCollectionSlottedAccess(b *testing.B) {
	for _, count := range []int{1, 9, 61, 489} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			i := testCollectionIndex(b)
			k := objectBinding{}
			var id [16]byte
			posts := make([]collectionPosting, count)
			for n := range posts {
				posts[n] = collectionPosting{id, uint64(n), collectionAddress{0, uint32(n + 1)}}
			}
			if err := i.append(k, 2, 0, uint64(count+1), posts); err != nil {
				b.Fatal(err)
			}
			b.Run("Latest", func(b *testing.B) {
				b.ReportAllocs()
				for n := 0; n < b.N; n++ {
					rev, _, ok, err := i.latest(id[:], k)
					if err != nil || !ok || rev != uint64(count-1) {
						b.Fatal(rev, ok, err)
					}
				}
			})
			b.Run("Exact", func(b *testing.B) {
				b.ReportAllocs()
				for n := 0; n < b.N; n++ {
					if _, ok, err := i.lookup(id[:], k, uint64(n%count)); err != nil || !ok {
						b.Fatal(ok, err)
					}
				}
			})
		})
	}
}

func TestCollectionDuplicateRevisionAcrossMinors(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(time.Minute)
	cfg := Config{Root: t.TempDir(), Cache: "test", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }}
	p, err := OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	old := p.api.Store
	if _, err = old.Append(Write, testFrame(t, l, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	d := l.Definition
	d.Version++
	d.Resource.Minor++
	next, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Prefer(next); err != nil {
		t.Fatal(err)
	}
	raw, err := next.Encode(nil, []Value{uuid(at.Add(time.Second).UnixMilli(), 2), testObject(42), Uint(1, 8), {}, {}, {}, {}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.api.Store.Append(Write, raw); err == nil {
		t.Fatal("duplicate pending revision across minors accepted")
	}
	now = at.Add(2 * time.Hour)
	if err = old.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err = p.api.Store.Append(Write, raw); err == nil {
		t.Fatal("duplicate committed revision across minors accepted")
	}
}
