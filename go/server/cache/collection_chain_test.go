package cache

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func collectionPosts(id [16]byte, start, end int) []collectionPosting {
	out := make([]collectionPosting, 0, end-start)
	for r := start; r < end; r++ {
		out = append(out, collectionPosting{id, uint64(r), collectionAddress{17, uint32(r + 1)}})
	}
	return out
}
func TestCollectionFiveByteLayout(t *testing.T) {
	p := newCollectionPage(objectBinding{})
	id := collectionTestID(1)
	if p.referenceWidth() != 5 || p.parentReferences() != 2 || p.perExtension() != 6 || p.maxCapacity() != 734 {
		t.Fatal("layout capacity")
	}
	for r := 0; r < 734; r++ {
		if e := p.put(id, uint64(65536+r), collectionAddress{9, uint32(r + 1)}.packed()); e != nil {
			t.Fatal(r, e)
		}
	}
	if p.used() != 123 || p.base(0) != 65536 || p.refAt(0, 0) != p.at(0)+22 || p.refAt(0, 1) != p.at(0)+27 {
		t.Fatal("parent layout")
	}
	p.seal()
	if e := p.validate(objectBinding{}); e != nil {
		t.Fatal(e)
	}
	if e := p.put(id, 65536+734, collectionAddress{9, 735}.packed()); e != errCollectionPageFull {
		t.Fatal(e)
	}
}
func TestCollectionChainBoundaryRecoveryAndPinnedReaders(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	id := collectionTestID(2)
	if e := i.append(k, 4, 0, 735, collectionPosts(id, 0, 734)); e != nil {
		t.Fatal(e)
	}
	old := i.pin()
	defer releaseCollection(old)
	for end := 735; end <= 2300; end += 313 {
		start := int(i.through[k] - 1)
		if e := i.append(k, 4, i.through[k], uint64(end+1), collectionPosts(id, start, end)); e != nil {
			t.Fatal(e)
		}
	}
	total := int(i.through[k] - 1)
	if len(i.view.Sources[k].Overflow) < 2 {
		t.Fatal("no continuation chain")
	}
	verify := func(store *objectIndex, v *collectionView, want int) {
		t.Helper()
		count := 0
		if e := store.walkObject(v, id[:], k, func(p *collectionPage, n int) bool {
			p.visit(n, func(r, a uint64) {
				if r != uint64(count) || unpackCollection(a) != (collectionAddress{17, uint32(count + 1)}) {
					t.Fatalf("revision %d count %d address %x", r, count, a)
				}
				count++
			})
			return true
		}); e != nil {
			t.Fatal(e)
		}
		if count != want {
			t.Fatal(count, want)
		}
	}
	verify(i, old, 734)
	verify(i, i.view, total)
	reopened, e := loadObjectIndex(i.file)
	if e != nil {
		t.Fatal(e)
	}
	verify(reopened, reopened.view, total)
	for _, r := range []int{0, 732, 733, 734, 1465, total - 1} {
		a, ok, e := reopened.lookup(id[:], k, uint64(r))
		if e != nil || !ok || a.Record != uint32(r+1) {
			t.Fatal(r, a, ok, e)
		}
	}
	// All continuations remain protected while the old root is pinned.
	protected := map[uint32]bool{}
	for _, v := range []*collectionView{old, i.view} {
		for id := range v.Sources[k].Overflow {
			protected[id] = true
		}
	}
	for _, p := range i.freePages() {
		if protected[p] {
			t.Fatal("reachable continuation reclaimed", p)
		}
	}
}
func TestCollectionChainedSplitAndWiden(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	id := collectionTestID(400)
	if e := i.mergeRetained(k, 4, 10001, collectionPosts(id, 0, 1500)); e != nil {
		t.Fatal(e)
	}
	old := i.pin()
	defer releaseCollection(old)
	// Insert neighbors around the linked UUID, forcing directory splits.
	for _, n := range []int{399, 401, 200, 600} {
		if e := i.mergeRetained(k, 4, 10001, collectionPosts(collectionTestID(n), 0, 20)); e != nil {
			t.Fatal(e)
		}
	}
	wide := collectionAddress{1 << 24, 1 << 24}
	if e := i.mergeRetained(k, 4, 10002, []collectionPosting{{id, 1500, wide}}); e != nil {
		t.Fatal(e)
	}
	reopened, e := loadObjectIndex(i.file)
	if e != nil {
		t.Fatal(e)
	}
	for r := 0; r < 1500; r++ {
		a, ok, e := reopened.lookup(id[:], k, uint64(r))
		if e != nil || !ok || a.Record != uint32(r+1) {
			t.Fatal(r, a, ok, e)
		}
	}
	if a, ok, e := reopened.lookup(id[:], k, 1500); e != nil || !ok || a != wide {
		t.Fatal(a, ok, e)
	}
	if e := i.walkObject(old, id[:], k, func(p *collectionPage, n int) bool {
		if p[30] != 2 || p[31] != 3 {
			t.Error("old page widened")
		}
		return true
	}); e != nil {
		t.Fatal(e)
	}
}
func TestCollectionChainRandomGapsAndRetention(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	id := collectionTestID(3)
	rng := rand.New(rand.NewSource(18))
	revs := rng.Perm(2400)
	for start := 0; start < len(revs); start += 100 {
		var posts []collectionPosting
		for _, r := range revs[start:min(start+100, len(revs))] {
			rev := uint64(65536 + r*3)
			posts = append(posts, collectionPosting{id, rev, collectionAddress{4, uint32(r + 1)}})
		}
		if e := i.mergeRetained(k, 4, 10000, posts); e != nil {
			t.Fatal(start, e)
		}
	}
	j, e := loadObjectIndex(i.file)
	if e != nil {
		t.Fatal(e)
	}
	for r := 0; r < 2400; r++ {
		a, ok, e := j.lookup(id[:], k, uint64(65536+r*3))
		if e != nil || !ok || a.Record != uint32(r+1) {
			t.Fatal(r, a, ok, e)
		}
	}
	if _, ok, e := j.lookup(id[:], k, 65537); e != nil || ok {
		t.Fatal("gap", ok, e)
	}
	// The same streaming path used by retention rebuilds links after removing the head.
	next := testCollectionIndex(t)
	var batch []collectionPosting
	e = j.each(k, func(id [16]byte, r uint64, a collectionAddress) error {
		if r >= 70000 {
			batch = append(batch, collectionPosting{id, r, a})
		}
		if len(batch) == 100 {
			e := next.mergeRetained(k, 4, 10000, batch)
			batch = nil
			return e
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if len(batch) > 0 {
		if e = next.mergeRetained(k, 4, 10000, batch); e != nil {
			t.Fatal(e)
		}
	}
	next, e = loadObjectIndex(next.file)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok, e := next.lookup(id[:], k, 65536); e != nil || ok {
		t.Fatal(ok, e)
	}
	if _, ok, e := next.lookup(id[:], k, 72733); e != nil || !ok {
		t.Fatal(ok, e)
	}
}
func TestCollectionChainPublicationFailure(t *testing.T) {
	for _, stage := range []string{"before-root", "after-root"} {
		t.Run(stage, func(t *testing.T) {
			i := testCollectionIndex(t)
			k := objectBinding{}
			id := collectionTestID(1)
			if e := i.append(k, 4, 0, 735, collectionPosts(id, 0, 734)); e != nil {
				t.Fatal(e)
			}
			i.fault = func(at string) error {
				if at == stage {
					return fmt.Errorf("injected")
				}
				return nil
			}
			if e := i.append(k, 4, 735, 736, collectionPosts(id, 734, 735)); e == nil {
				t.Fatal("fault missed")
			}
			if _, ok, e := i.lookup(id[:], k, 734); e != nil || ok {
				t.Fatal("unpublished", ok, e)
			}
			j, e := loadObjectIndex(i.file)
			if e != nil {
				t.Fatal(e)
			}
			if _, ok, e := j.lookup(id[:], k, 734); e != nil || ok != (stage == "after-root") {
				t.Fatal(ok, e)
			}
			for r := uint64(732); r <= 733; r++ {
				if _, ok, e := j.lookup(id[:], k, r); e != nil || !ok {
					t.Fatal("boundary lost", r, ok, e)
				}
			}
		})
	}
}
func TestCollectionRevisionU24AndNoEmptyGapPages(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	id := collectionTestID(1)
	posts := []collectionPosting{{id, 0, collectionAddress{0, 1}}, {id, collectionMaxRevision, collectionAddress{0, 2}}}
	if e := i.append(k, 4, 0, 3, posts); e != nil {
		t.Fatal(e)
	}
	if len(i.view.Sources[k].Overflow) != 1 {
		t.Fatal("reserved pages for revision gap")
	}
	if _, ok, e := i.lookup(id[:], k, collectionMaxRevision); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if e := i.append(k, 4, 3, 4, []collectionPosting{{id, collectionMaxRevision + 1, collectionAddress{0, 3}}}); e == nil {
		t.Fatal("overflow accepted")
	}
}

func TestCollectionChainedReadersDuringPublication(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	id := collectionTestID(1)
	if e := i.append(k, 4, 0, 1501, collectionPosts(id, 0, 1500)); e != nil {
		t.Fatal(e)
	}
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	i.fault = func(stage string) error {
		if stage == "before-root" {
			close(entered)
			<-resume
		}
		return nil
	}
	go func() { done <- i.append(k, 4, 1501, 1502, collectionPosts(id, 1500, 1501)) }()
	<-entered
	read := make(chan error, 1)
	go func() {
		r, _, ok, e := i.latest(id[:], k)
		if e == nil && (!ok || r != 1499) {
			e = fmt.Errorf("reader saw unpublished tail %d", r)
		}
		read <- e
	}()
	select {
	case e := <-read:
		if e != nil {
			t.Error(e)
		}
	case <-time.After(time.Second):
		t.Error("reader waited for collection writer")
	}
	close(resume)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if r, _, ok, e := i.latest(id[:], k); e != nil || !ok || r != 1500 {
		t.Fatal(r, ok, e)
	}
}
func TestCollectionCorruptContinuationRejected(t *testing.T) {
	for _, kind := range []string{"cycle", "slot", "owner"} {
		t.Run(kind, func(t *testing.T) {
			i := testCollectionIndex(t)
			k := objectBinding{}
			id := collectionTestID(1)
			if e := i.append(k, 4, 0, 736, collectionPosts(id, 0, 735)); e != nil {
				t.Fatal(e)
			}
			rootID := i.view.Sources[k].Fences[0].Page
			p, e := i.readPage(rootID, k)
			if e != nil {
				t.Fatal(e)
			}
			link := p.link(0)
			switch kind {
			case "cycle":
				p.setLink(0, collectionLink{rootID, byte(p.at(0) / 32)})
				p.seal()
				_, e = i.file.WriteAt(p[:], int64(rootID)*4096)
			case "slot":
				p.setLink(0, collectionLink{link.Page, 127})
				p.seal()
				_, e = i.file.WriteAt(p[:], int64(rootID)*4096)
			case "owner":
				q, err := i.readPage(link.Page, k)
				if err != nil {
					t.Fatal(err)
				}
				q[q.at(0)] ^= 1
				q.seal()
				_, e = i.file.WriteAt(q[:], int64(link.Page)*4096)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = loadObjectIndex(i.file); e == nil {
				t.Fatal("corrupt continuation accepted")
			}
		})
	}
}
func BenchmarkCollectionChainAccess(b *testing.B) {
	for _, count := range []int{734, 735, 5000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			i := testCollectionIndex(b)
			k := objectBinding{}
			id := collectionTestID(1)
			if e := i.append(k, 4, 0, uint64(count+1), collectionPosts(id, 0, count)); e != nil {
				b.Fatal(e)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				if r, _, ok, e := i.latest(id[:], k); e != nil || !ok || r != uint64(count-1) {
					b.Fatal(r, ok, e)
				}
			}
		})
	}
}

func TestCollectionWideningInsideLinkedHistory(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	id := collectionTestID(1)
	missing := map[uint64]bool{100: true, 800: true, 1450: true}
	var posts []collectionPosting
	for _, post := range collectionPosts(id, 0, 2000) {
		if !missing[post.Revision] {
			posts = append(posts, post)
		}
	}
	if e := i.mergeRetained(k, 4, 3000, posts); e != nil {
		t.Fatal(e)
	}
	old := i.pin()
	defer releaseCollection(old)
	wide := collectionAddress{1 << 24, 1 << 24}
	for _, rev := range []uint64{800, 100, 1450} {
		if e := i.mergeRetained(k, 4, 3000, []collectionPosting{{id, rev, wide}}); e != nil {
			t.Fatal(rev, e)
		}
	}
	j, e := loadObjectIndex(i.file)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	e = j.visit(id[:], k, func(r uint64, a collectionAddress) {
		want := collectionAddress{17, uint32(r + 1)}
		if missing[r] {
			want = wide
		}
		if r != uint64(count) || a != want {
			t.Errorf("rev %d count %d got %+v want %+v", r, count, a, want)
		}
		count++
	})
	if e != nil || count != 2000 {
		t.Fatal(count, e)
	}
	e = i.walkObject(old, id[:], k, func(p *collectionPage, n int) bool {
		p.visit(n, func(r, a uint64) {
			if missing[r] {
				t.Error("old chain acquired new revision", r)
			}
		})
		return true
	})
	if e != nil {
		t.Fatal(e)
	}
}
