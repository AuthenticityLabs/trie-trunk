package cache

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestCollectionIndependentPageFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/slotted.page")
	if err != nil {
		t.Fatal(err)
	}
	var p collectionPage
	copy(p[:], raw)
	if err = p.validate(objectBinding{}); err != nil {
		t.Fatal(err)
	}
	var queries []struct {
		Key     string
		History [][]any
	}
	raw, err = os.ReadFile("testdata/slotted.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &queries); err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		id, _ := hex.DecodeString(q.Key)
		n := p.find(id)
		if n < 0 {
			t.Fatal("missing fixture key")
		}
		count := 0
		p.visit(n, func(rev, ref uint64) {
			want := q.History[count]
			r := uint64(want[0].(float64))
			a, _ := strconv.ParseUint(want[1].(string), 10, 64)
			if rev != r || ref != a {
				t.Fatal("fixture history mismatch", rev, ref, r, a)
			}
			count++
		})
		if count != len(q.History) {
			t.Fatal("history count")
		}
	}
}
func collectionTestID(n int) (id [16]byte) { order.PutUint64(id[8:], uint64(n+1)); return }
func TestCollectionSplitsPinnedReadersAndReuse(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	rng := rand.New(rand.NewSource(10))
	perm := rng.Perm(2000)
	for start := 0; start < len(perm); start += 128 {
		end := min(start+128, len(perm))
		var posts []collectionPosting
		for n := start; n < end; n++ {
			posts = append(posts, collectionPosting{collectionTestID(perm[n]), 0, collectionAddress{0, uint32(perm[n] + 1)}})
		}
		// Private build accepts interleaved row references, as retention does.
		if err := i.mergeRetained(k, 2, 2001, posts); err != nil {
			t.Fatal(err)
		}
	}
	pinned := i.pin()
	defer releaseCollection(pinned)
	oldFences := pinned.Sources[k].Fences
	for start := 0; start < 2000; start += 128 {
		var posts []collectionPosting
		for n := start; n < min(start+128, 2000); n++ {
			posts = append(posts, collectionPosting{collectionTestID(n), 1, collectionAddress{0, uint32(2001 + n)}})
		}
		if err := i.mergeRetained(k, 2, 4001, posts); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 2000; n++ {
		id := collectionTestID(n)
		f := oldFences[collectionRoute(oldFences, id[:])]
		p, err := i.readPage(f.Page, k)
		if err != nil {
			t.Fatal(err)
		}
		at := p.find(id[:])
		if at < 0 || p.reference(at, 0) != uint64(n+1) || p.reference(at, 1) != 0 {
			t.Fatal("pinned page reused")
		}
		a, ok, err := i.lookup(id[:], k, 1)
		if err != nil || !ok || a.Record != uint32(2001+n) {
			t.Fatal("new lookup", n, a, ok, err)
		}
	}
	reopened, err := loadObjectIndex(i.file)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2000; n++ {
		id := collectionTestID(n)
		if _, ok, err := reopened.lookup(id[:], k, 1); err != nil || !ok {
			t.Fatal(n, ok, err)
		}
	}
}
func TestCollectionReaderDuringDurableWrite(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	id := collectionTestID(1)
	if err := i.append(k, 2, 0, 2, []collectionPosting{{id, 0, collectionAddress{0, 1}}}); err != nil {
		t.Fatal(err)
	}
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	i.fault = func(stage string) error {
		if stage == "before-root" {
			close(entered)
			<-resume
		}
		return nil
	}
	go func() { done <- i.append(k, 2, 2, 3, []collectionPosting{{id, 1, collectionAddress{0, 2}}}) }()
	<-entered
	lookup := make(chan error, 1)
	go func() {
		r, _, ok, e := i.latest(id[:], k)
		if e == nil && (!ok || r != 0) {
			e = fmt.Errorf("unpublished revision")
		}
		lookup <- e
	}()
	select {
	case err := <-lookup:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("reader blocked on writer")
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r, _, ok, e := i.latest(id[:], k); e != nil || !ok || r != 1 {
		t.Fatal("publication", r, ok, e)
	}
}
func TestCollectionRecentHistoryBypassesDisk(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	now := at.Add(time.Minute)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }}
	p, err := OpenEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	s := p.api.Store
	raw := testFrame(t, l, at, 1, false)
	if _, err = s.Append(Write, raw); err != nil {
		t.Fatal(err)
	}
	now = at.Add(80 * time.Minute)
	// Reads must not seal the pending event as a side effect.
	h, err := p.snapshotHistory(l.Definition.Identity, 1, testObject(42), at, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.locations) != 1 || h.locations[0].row != ProvisionalRow {
		t.Fatal("read sealed pending")
	}
	h.close()
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	original := s.objects.file
	blocked, err := os.Open(original.Name())
	if err != nil {
		t.Fatal(err)
	}
	blocked.Close()
	s.objects.file = blocked
	defer func() { s.objects.file = original }()
	count := 0
	err = p.History(context.Background(), l.Definition.Identity, 1, testObject(42), at, now, func(_ EventReference, got []byte) error {
		count++
		if !bytes.Equal(raw, got) {
			t.Fatal("frame changed")
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatal("hot path read collection file", count, err)
	}
	count = 0
	err = p.History(context.Background(), l.Definition.Identity, 1, testObject(43), at, now, func(_ EventReference, _ []byte) error { count++; return nil })
	if err != nil || count != 0 {
		t.Fatal("complete-window negative lookup", count, err)
	}
	// A wider request must not silently mistake the resident suffix for all history.
	if err = p.History(context.Background(), l.Definition.Identity, 1, testObject(42), at.Add(-48*time.Hour), now, func(_ EventReference, _ []byte) error { return nil }); err == nil {
		t.Fatal("incomplete resident window treated as complete")
	}
}
func TestCollectionRevisionOverflowRejectedBeforeStaging(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	now := at.Add(time.Minute)
	s, err := Open(Config{Root: t.TempDir(), Cache: "test", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Append(Write, testFrame(t, l, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(Write, testFrame(t, l, at.Add(time.Millisecond), 1<<24, false)); err == nil {
		t.Fatal("revision overflow accepted")
	}
	if s.branches[Write].live.count(0) != 1 {
		t.Fatal("failed event staged")
	}
	if _, err = s.Append(Write, testFrame(t, l, at.Add(2*time.Millisecond), 70000, false)); err != nil {
		t.Fatal("U24 revision rejected", err)
	}
	now = at.Add(2 * time.Hour)
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	locations, err := s.ObjectLocations(testObject(42))
	if err != nil || len(locations) != 2 {
		t.Fatal("high revision replay", locations, err)
	}
	if len(s.objects.view.Sources[objectBinding{l.Definition.Resource.Minor, s.branches[Write].generation}].Overflow) != 1 {
		t.Fatal("gap allocated empty pages")
	}
}

func BenchmarkCollectionPlatform100K(b *testing.B) {
	i := testCollectionIndex(b)
	k := objectBinding{}
	const count = 100000
	started := time.Now()
	rng := rand.New(rand.NewSource(10))
	perm := rng.Perm(count)
	for start := 0; start < count; start += 1024 {
		var posts []collectionPosting
		end := min(count, start+1024)
		for n := start; n < end; n++ {
			posts = append(posts, collectionPosting{collectionTestID(perm[n]), 0, collectionAddress{0, uint32(n + 1)}})
		}
		if err := i.append(k, 2, i.through[k], uint64(end+1), posts); err != nil {
			b.Fatal(err)
		}
	}
	elapsed := time.Since(started)
	st, _ := i.file.Stat()
	fences := len(i.sources[k].Fences)
	b.Logf("100K UUIDs: build=%s file=%d bytes current fences=%d (%d packed bytes)", elapsed, st.Size(), fences, fences*20)
	var ids [10000][16]byte
	for n := range ids {
		ids[n] = collectionTestID(rng.Intn(count))
	}
	b.ResetTimer()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		if _, ok, err := i.lookup(ids[n%len(ids)][:], k, 0); err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}
