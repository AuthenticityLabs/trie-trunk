package cache

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"
	"time"
)

func TestHourDirectoryIndependentWidening(t *testing.T) {
	origin := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	first := origin + 65536*TimeHourMS
	r := timeRun(first, 1<<32, 65536)
	r.PopStart = 1<<40 + 72
	r.PAC.Placement.MemberStart = 1<<40 + 4
	d, err := buildHourDirectory([]Run{r}, first, first+TimeHourMS, r.End, [16]byte{1}, origin, [6]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.raw[40:46], []byte{5, 6, 6, 3, 3, 3}) {
		t.Fatal("widths", d.raw[40:46])
	}
	leaf, ok := d.leaf(first)
	if !ok || leaf.Start != r.Start-1 || leaf.Pods != r.PopStart || leaf.Sods != r.PAC.Placement.MemberStart-4 || leaf.End != r.End {
		t.Fatal(leaf)
	}
	address := collectionAddress{65536, 65536}
	if got, e := d.resolveReference(address); e != nil || got != r.End-1 {
		t.Fatal(got, e)
	}
	if _, e := d.resolveReference(collectionAddress{65536, 65537}); e == nil {
		t.Fatal("out-of-hour record accepted")
	}
	h := indexHeader(first, 65536)
	if h[12] != 3 || validateIndexHeader(h, r) != nil {
		t.Fatal("literal count did not widen")
	}
}

func TestCollectionReferenceIndependentWidening(t *testing.T) {
	p := newCollectionPage(objectBinding{})
	id := collectionTestID(1)
	refs := []collectionAddress{{0, 1}, {0, 65535}, {0, 65536}, {65535, 1}, {65536, 1}, {1 << 24, 1 << 24}}
	for n, ref := range refs {
		if err := p.put(id, uint64(n), ref.packed()); err != nil {
			t.Fatal(err)
		}
		p.seal()
		if err := p.validate(objectBinding{}); err != nil {
			t.Fatal(err)
		}
		found := p.find(id[:])
		count := 0
		p.visit(found, func(rev, v uint64) {
			if v != refs[rev].packed() {
				t.Fatal("widening changed reference", rev, v)
			}
			count++
		})
		if count != n+1 {
			t.Fatal("widening lost history", count)
		}
	}
	if p[30] != 4 || p[31] != 4 {
		t.Fatal("missing independent widening")
	}
}

func TestPACCompressionWrapperBoundaries(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var frames [][]byte
	for i := 0; i < 100; i++ {
		frames = append(frames, testFrame(t, l, at.Add(time.Duration(i)*time.Second), uint32(i+1), false))
	}
	p, err := factorPAC(l, frames, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	p.Bytes, err = p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := compressPAC(p.Bytes, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) >= len(p.Bytes) || order.Uint32(compressed[4:8])&2 == 0 {
		t.Fatal("expected compressed body")
	}
	if order.Uint32(compressed[8:12]) != uint32(len(p.Bytes)) || order.Uint32(compressed[12:16]) != uint32(len(p.Members)) {
		t.Fatal("compression controls")
	}
	// Independently inflate just the compressed body: it contains the complete
	// semantic header and records, with no patching from the outer header.
	z, err := gzip.NewReader(bytes.NewReader(compressed[PACCompressedHeaderSize:]))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(z)
	z.Close()
	if err != nil || !bytes.Equal(body, p.Bytes) {
		t.Fatal("inner header was not compressed", err)
	}
	decoded, err := expandPAC(compressed)
	if err != nil || !bytes.Equal(decoded, p.Bytes) {
		t.Fatal("compression round trip", err)
	}
	for _, bad := range [][]byte{compressed[:len(compressed)-1], append(bytes.Clone(compressed), 0)} {
		if _, err = expandPAC(bad); err == nil {
			t.Fatal("invalid stored length accepted")
		}
	}
	for _, extra := range [][]byte{{0}, compressed[PACCompressedHeaderSize:]} {
		bad := append(bytes.Clone(compressed), extra...)
		order.PutUint32(bad[:4], uint32(len(bad)))
		if _, err = expandPAC(bad); err == nil {
			t.Fatal("trailing compressed data accepted")
		}
	}
	mismatch := bytes.Clone(compressed)
	order.PutUint32(mismatch[12:16], uint32(len(p.Members)+1))
	if _, err = expandPAC(mismatch); err == nil {
		t.Fatal("outer/inner count mismatch accepted")
	}
	bad := bytes.Clone(compressed)
	bad[len(bad)-8] ^= 1
	if _, err = expandPAC(bad); err == nil {
		t.Fatal("bad gzip checksum accepted")
	}
}

func TestCollectionWideningSplitsWithoutMovingPinnedReaders(t *testing.T) {
	i := testCollectionIndex(t)
	k := objectBinding{}
	var posts []collectionPosting
	for object := 0; object < 60; object++ {
		for revision := 0; revision < 8; revision++ {
			posts = append(posts, collectionPosting{collectionTestID(object), uint64(revision), collectionAddress{0, uint32(object*9 + revision + 1)}})
		}
	}
	if err := i.mergeRetained(k, 2, 1000, posts); err != nil {
		t.Fatal(err)
	}
	old := i.pin()
	defer releaseCollection(old)
	if len(old.Sources[k].Fences) != 1 {
		t.Fatal("expected one nearly full narrow page")
	}
	id := collectionTestID(30)
	wide := collectionAddress{65536, 65536}
	if err := i.mergeRetained(k, 2, 1001, []collectionPosting{{id, 9, wide}}); err != nil {
		t.Fatal(err)
	}
	if len(i.view.Sources[k].Fences) <= 1 {
		t.Fatal("widening did not split the full page")
	}
	prior, err := i.readPage(old.Sources[k].Fences[0].Page, k)
	if err != nil {
		t.Fatal(err)
	}
	if prior[30] != 2 || prior[31] != 3 || prior.reference(prior.find(id[:]), 9) != 0 {
		t.Fatal("pinned page changed")
	}
	reopened, err := loadObjectIndex(i.file)
	if err != nil {
		t.Fatal(err)
	}
	for _, post := range append(posts, collectionPosting{id, 9, wide}) {
		got, ok, err := reopened.lookup(post.Object[:], k, post.Revision)
		if err != nil || !ok || got != post.Address {
			t.Fatal("reference lost across widened publication", post, got, ok, err)
		}
	}
}
