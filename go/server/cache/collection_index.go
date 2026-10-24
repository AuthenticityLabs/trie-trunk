package cache

// The serving implementation of the slotted collection prototype. Readers pin
// immutable directories; writers replace pages and publish an alternate root.
// Sealed canonical writes are the redo source, so no second event journal exists.
import (
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
)

const collectionMagic = "COLLIDX0"

type objectBinding struct {
	Minor      uint32
	Generation [16]byte
}
type collectionAddress struct{ Hour, Record uint32 }

func (a collectionAddress) packed() uint64 { return uint64(a.Hour)<<32 | uint64(a.Record) }
func unpackCollection(v uint64) collectionAddress {
	return collectionAddress{uint32(v >> 32), uint32(v)}
}

type collectionPosting struct {
	Object   [16]byte
	Revision uint64
	Address  collectionAddress
}
type collectionFence struct {
	First [16]byte
	Page  uint32
}
type collectionSource struct {
	Width   byte
	Through uint64
	Fences  []collectionFence
	// Reachable continuation pages, rebuilt from links on open; not a UUID map.
	Overflow map[uint32]bool
}
type collectionView struct {
	Sequence uint64
	Sources  map[objectBinding]*collectionSource
	Metadata []uint32
	Readers  atomic.Int64
}
type objectIndex struct {
	file        *os.File
	writer      sync.Mutex
	publication sync.RWMutex
	view        *collectionView
	roots       [2]*collectionView
	retired     []*collectionView
	through     map[objectBinding]uint64
	sources     map[objectBinding]*collectionSource
	next        uint32
	failed      error
	// Test-only fault point, called before/after the root publication.
	fault func(string) error
}

func newObjectIndex(f *os.File) *objectIndex {
	v := &collectionView{Sources: map[objectBinding]*collectionSource{}}
	return &objectIndex{file: f, view: v, sources: v.Sources, through: map[objectBinding]uint64{}, next: 1}
}
func (i *objectIndex) pin() *collectionView {
	i.publication.RLock()
	v := i.view
	v.Readers.Add(1)
	i.publication.RUnlock()
	return v
}
func releaseCollection(v *collectionView) { v.Readers.Add(-1) }

// crc32's architecture dispatch makes its input escape in Go. Reuse the I/O
// buffer rather than allocating a 4 KiB page on every historical lookup.
type collectionReadError struct{ error }

var collectionReadBuffers = sync.Pool{New: func() any { return new(collectionPage) }}

func (i *objectIndex) readPageInto(p *collectionPage, id uint32, k objectBinding) error {
	_, err := i.file.ReadAt(p[:], int64(id)*collectionPageBytes)
	if err == nil {
		err = p.validate(k)
	}
	if err != nil {
		return &collectionReadError{err}
	}
	return nil
}
func (i *objectIndex) readPage(id uint32, k objectBinding) (collectionPage, error) {
	p := collectionReadBuffers.Get().(*collectionPage)
	err := i.readPageInto(p, id, k)
	out := *p
	collectionReadBuffers.Put(p)
	return out, err
}

func collectionRoute(f []collectionFence, id []byte) int {
	n := sort.Search(len(f), func(n int) bool { return bytes.Compare(f[n].First[:], id) > 0 }) - 1
	return n
}

// walkObject follows physical continuation addresses within one pinned view.
// Increasing revision bases prevent cycles without allocating a visited set.
func (i *objectIndex) walkObject(v *collectionView, id []byte, k objectBinding, fn func(*collectionPage, int) bool) error {
	s := v.Sources[k]
	if s == nil {
		return nil
	}
	n := collectionRoute(s.Fences, id)
	if n < 0 {
		return nil
	}
	p := collectionReadBuffers.Get().(*collectionPage)
	defer collectionReadBuffers.Put(p)
	e := i.readPageInto(p, s.Fences[n].Page, k)
	if e != nil {
		return e
	}
	n = p.find(id)
	if n < 0 {
		return nil
	}
	for {
		if !fn(p, n) {
			return nil
		}
		if !p.linked(n) {
			return nil
		}
		link := p.link(n)
		if !s.Overflow[link.Page] {
			return fmt.Errorf("continuation outside published collection view")
		}
		high := collectionLastRevision(p, n)
		e = i.readPageInto(p, link.Page, k)
		if e != nil {
			return e
		}
		n, e = continuationSlot(p, link, id)
		if e != nil {
			return e
		}
		if p.base(n) <= high {
			return fmt.Errorf("non-increasing collection chain")
		}
	}
}
func (i *objectIndex) visit(id []byte, k objectBinding, fn func(uint64, collectionAddress)) error {
	v := i.pin()
	defer releaseCollection(v)
	return i.walkObject(v, id, k, func(p *collectionPage, n int) bool {
		p.visit(n, func(r, a uint64) { fn(r, unpackCollection(a)) })
		return true
	})
}
func (i *objectIndex) lookup(id []byte, k objectBinding, revision uint64) (a collectionAddress, ok bool, err error) {
	v := i.pin()
	defer releaseCollection(v)
	err = i.walkObject(v, id, k, func(p *collectionPage, n int) bool {
		ref := p.reference(n, revision)
		if ref != 0 {
			a = unpackCollection(ref)
			ok = true
			return false
		}
		return revision >= p.base(n)
	})
	return
}
func (i *objectIndex) latest(id []byte, k objectBinding) (revision uint64, a collectionAddress, ok bool, err error) {
	v := i.pin()
	defer releaseCollection(v)
	err = i.walkObject(v, id, k, func(p *collectionPage, n int) bool {
		// Follow links first; only the terminal page can contain the latest revision.
		if p.linked(n) {
			return true
		}
		cap, base := p.capacity(n), p.base(n)
		for j := cap - 1; j >= 0; j-- {
			if base+uint64(j) > collectionMaxRevision {
				continue
			}
			if ref := p.ref(n, (p.head(n)+j)%cap); ref != 0 {
				revision = base + uint64(j)
				a = unpackCollection(ref)
				ok = true
				break
			}
		}
		return false
	})
	return
}
func (i *objectIndex) each(k objectBinding, fn func([16]byte, uint64, collectionAddress) error) error {
	v := i.pin()
	defer releaseCollection(v)
	s := v.Sources[k]
	if s == nil {
		return nil
	}
	for _, f := range s.Fences {
		p, e := i.readPage(f.Page, k)
		if e != nil {
			return e
		}
		for n := 0; n < p.used(); n = p.end(n) {
			var id [16]byte
			copy(id[:], p.key(n))
			var failure error
			e = i.walkObject(v, id[:], k, func(q *collectionPage, at int) bool {
				q.visit(at, func(r, a uint64) {
					if failure == nil {
						failure = fn(id, r, unpackCollection(a))
					}
				})
				return failure == nil
			})
			if e != nil {
				return e
			}
			if failure != nil {
				return failure
			}
		}
	}
	return nil
}
func collectionKeys(s map[objectBinding]*collectionSource) []objectBinding {
	keys := make([]objectBinding, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].Minor != keys[b].Minor {
			return keys[a].Minor < keys[b].Minor
		}
		return bytes.Compare(keys[a].Generation[:], keys[b].Generation[:]) < 0
	})
	return keys
}
func encodeCollectionDirectory(sources map[objectBinding]*collectionSource) []byte {
	size := 4
	for _, s := range sources {
		size += 40 + len(s.Fences)*20
	}
	out := make([]byte, size)
	order.PutUint32(out, uint32(len(sources)))
	at := 4
	for _, k := range collectionKeys(sources) {
		s := sources[k]
		copy(out[at:at+16], k.Generation[:])
		order.PutUint32(out[at+16:], k.Minor)
		out[at+20] = s.Width
		order.PutUint64(out[at+24:], s.Through)
		order.PutUint32(out[at+32:], uint32(len(s.Fences)))
		at += 40
		for _, f := range s.Fences {
			copy(out[at:at+16], f.First[:])
			order.PutUint32(out[at+16:], f.Page)
			at += 20
		}
	}
	return out
}
func decodeCollectionDirectory(raw []byte, limit uint32) (map[objectBinding]*collectionSource, error) {
	bad := fmt.Errorf("invalid collection directory")
	if len(raw) < 4 {
		return nil, bad
	}
	count := int(order.Uint32(raw))
	at := 4
	sources := map[objectBinding]*collectionSource{}
	if count > (len(raw)-4)/40 {
		return nil, bad
	}
	for n := 0; n < count; n++ {
		if len(raw)-at < 40 {
			return nil, bad
		}
		var k objectBinding
		copy(k.Generation[:], raw[at:at+16])
		k.Minor = order.Uint32(raw[at+16:])
		s := &collectionSource{Width: raw[at+20], Through: order.Uint64(raw[at+24:])}
		nf := int(order.Uint32(raw[at+32:]))
		if sources[k] != nil || s.Through > IndexCapacity || (s.Width != 1 && s.Width != 2 && s.Width != 4 && s.Width != 8) || !bytes.Equal(raw[at+21:at+24], []byte{0, 0, 0}) || order.Uint32(raw[at+36:]) != 0 {
			return nil, bad
		}
		at += 40
		if nf > (len(raw)-at)/20 {
			return nil, bad
		}
		s.Fences = make([]collectionFence, nf)
		for j := range s.Fences {
			f := &s.Fences[j]
			copy(f.First[:], raw[at:at+16])
			f.Page = order.Uint32(raw[at+16:])
			at += 20
			if f.Page == 0 || f.Page >= limit || j > 0 && bytes.Compare(s.Fences[j-1].First[:], f.First[:]) >= 0 {
				return nil, bad
			}
		}
		sources[k] = s
	}
	if at != len(raw) {
		return nil, bad
	}
	return sources, nil
}
func collectionRoot(v *collectionView, rawLen int) [64]byte {
	var b [64]byte
	copy(b[:8], collectionMagic)
	order.PutUint64(b[8:], v.Sequence)
	order.PutUint32(b[16:], v.Metadata[0])
	order.PutUint32(b[20:], uint32(len(v.Metadata)))
	order.PutUint64(b[24:], uint64(rawLen))
	order.PutUint32(b[32:], crc32.Checksum(b[:], checksum))
	return b
}
func readCollectionView(f *os.File, root []byte, limit uint32) (*collectionView, error) {
	bad := fmt.Errorf("invalid collection root")
	var b [64]byte
	copy(b[:], root)
	want := order.Uint32(b[32:])
	clear(b[32:36])
	if string(b[:8]) != collectionMagic || want != crc32.Checksum(b[:], checksum) {
		return nil, bad
	}
	count, length := uint64(order.Uint32(b[20:])), order.Uint64(b[24:])
	if count == 0 || count >= uint64(limit) || length < 4 || length > count*4080 || count != (length+4079)/4080 {
		return nil, bad
	}
	v := &collectionView{Sequence: order.Uint64(b[8:])}
	raw := make([]byte, 0, int(length))
	seen := map[uint32]bool{}
	next := order.Uint32(b[16:])
	for n := uint64(0); n < count; n++ {
		if next == 0 || next >= limit || seen[next] {
			return nil, bad
		}
		seen[next] = true
		v.Metadata = append(v.Metadata, next)
		var p [4096]byte
		if _, e := f.ReadAt(p[:], int64(next)*4096); e != nil {
			return nil, e
		}
		want := order.Uint32(p[8:])
		clear(p[8:12])
		if string(p[:4]) != "CDIR" || want != crc32.Checksum(p[:], checksum) {
			return nil, bad
		}
		next = order.Uint32(p[4:])
		size := min(4080, int(length)-len(raw))
		raw = append(raw, p[16:16+size]...)
	}
	if next != 0 {
		return nil, bad
	}
	var e error
	v.Sources, e = decodeCollectionDirectory(raw, limit)
	if e != nil {
		return nil, e
	}
	for k, s := range v.Sources {
		s.Overflow = map[uint32]bool{}
		for n, fence := range s.Fences {
			if seen[fence.Page] {
				return nil, bad
			}
			seen[fence.Page] = true
			var p collectionPage
			if _, e = f.ReadAt(p[:], int64(fence.Page)*4096); e != nil {
				return nil, e
			}
			if e = p.validate(k); e != nil {
				return nil, e
			}
			if p[29] != 1 || p.used() == 0 || !bytes.Equal(p.key(0), fence.First[:]) {
				return nil, bad
			}
			for j := 0; j < p.used(); j = p.end(j) {
				if n+1 < len(s.Fences) && bytes.Compare(p.key(j), s.Fences[n+1].First[:]) >= 0 {
					return nil, bad
				}
				q, at := p, j
				var owner [16]byte
				copy(owner[:], p.key(j))
				for q.linked(at) {
					link := q.link(at)
					if link.Page == 0 || link.Page >= limit || seen[link.Page] {
						return nil, bad
					}
					seen[link.Page] = true
					s.Overflow[link.Page] = true
					high := collectionLastRevision(&q, at)
					if _, e = f.ReadAt(q[:], int64(link.Page)*4096); e != nil {
						return nil, e
					}
					if e = q.validate(k); e != nil {
						return nil, e
					}
					at, e = continuationSlot(&q, link, owner[:])
					if e != nil {
						return nil, e
					}
					if q.base(at) <= high {
						return nil, bad
					}
				}
			}
		}
	}
	return v, nil
}
func loadObjectIndex(f *os.File) (*objectIndex, error) {
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	i := newObjectIndex(f)
	if st.Size() == 0 {
		return i, nil
	}
	if st.Size() < 4096 || uint64(st.Size()+4095)/4096 > uint64(^uint32(0)) {
		return nil, fmt.Errorf("invalid collection file size")
	}
	i.next = uint32((st.Size() + 4095) / 4096)
	var roots [128]byte
	if _, e = f.ReadAt(roots[:], 0); e != nil {
		return nil, e
	}
	for n := 0; n < 2; n++ {
		v, err := readCollectionView(f, roots[n*64:(n+1)*64], i.next)
		var ioError *os.PathError
		if errors.As(err, &ioError) {
			return nil, err
		}
		if err == nil {
			i.roots[n] = v
			if v.Sequence > i.view.Sequence {
				i.view = v
			}
		}
	}
	if i.view.Sequence == 0 {
		return nil, fmt.Errorf("no valid collection root")
	}
	i.sources = i.view.Sources
	for k, s := range i.sources {
		i.through[k] = s.Through
	}
	return i, nil
}
func (i *objectIndex) freePages() []uint32 {
	protected := map[uint32]bool{0: true}
	mark := func(v *collectionView) {
		if v == nil {
			return
		}
		for _, p := range v.Metadata {
			protected[p] = true
		}
		for _, s := range v.Sources {
			for id := range s.Overflow {
				protected[id] = true
			}
			for _, f := range s.Fences {
				protected[f.Page] = true
			}
		}
	}
	for _, v := range i.roots {
		mark(v)
	}
	retired := i.retired[:0]
	for _, v := range i.retired {
		if v.Readers.Load() > 0 {
			mark(v)
			retired = append(retired, v)
		}
	}
	i.retired = retired
	var free []uint32
	for p := uint32(1); p < i.next; p++ {
		if !protected[p] {
			free = append(free, p)
		}
	}
	return free
}
func (i *objectIndex) append(k objectBinding, width byte, from, through uint64, entries []collectionPosting) error {
	return i.appendBatch(k, width, from, through, entries, false)
}
func (i *objectIndex) mergeRetained(k objectBinding, width byte, through uint64, entries []collectionPosting) error {
	return i.appendBatch(k, width, 0, through, entries, true)
}
func (i *objectIndex) replaceWith(next *objectIndex) {
	i.writer.Lock()
	defer i.writer.Unlock()
	i.publication.Lock()
	defer i.publication.Unlock()
	i.file = next.file
	i.view = next.view
	i.roots = next.roots
	i.retired = nil
	i.through = next.through
	i.sources = next.sources
	i.next = next.next
	i.failed = nil
}
func (i *objectIndex) appendBatch(k objectBinding, width byte, from, through uint64, entries []collectionPosting, privateMerge bool) error {
	i.writer.Lock()
	defer i.writer.Unlock()
	if i.failed != nil {
		return i.failed
	}
	if (width != 1 && width != 2 && width != 4 && width != 8) || !privateMerge && i.through[k] != from || through <= from || through > IndexCapacity {
		return fmt.Errorf("invalid object watermark")
	}
	old := i.view
	if old.Sequence == ^uint64(0) {
		return fmt.Errorf("collection publication sequence exhausted")
	}
	source := old.Sources[k]
	if source != nil && source.Width != width {
		return fmt.Errorf("collection revision width changed within source")
	}
	s := &collectionSource{Width: width, Through: through, Overflow: map[uint32]bool{}}
	if source != nil {
		s.Fences = append([]collectionFence(nil), source.Fences...)
		for id := range source.Overflow {
			s.Overflow[id] = true
		}
	}
	free := i.freePages()
	next := i.next
	alloc := func() (uint32, error) {
		if len(free) > 0 {
			v := free[len(free)-1]
			free = free[:len(free)-1]
			return v, nil
		}
		if next == ^uint32(0) {
			return 0, fmt.Errorf("collection page address exhausted")
		}
		v := next
		next++
		return v, nil
	}
	dirty := map[uint32]*collectionPage{}
	planner := collectionSplitPlanner{i, s, k, dirty, alloc}
	posts := append([]collectionPosting(nil), entries...)
	sort.Slice(posts, func(a, b int) bool {
		if c := bytes.Compare(posts[a].Object[:], posts[b].Object[:]); c != 0 {
			return c < 0
		}
		return posts[a].Revision < posts[b].Revision
	})
	for _, post := range posts {
		if post.Revision > collectionMaxRevision || width == 1 && post.Revision > 255 || width == 2 && post.Revision > 65535 || post.Address.Record == 0 {
			return fmt.Errorf("invalid collection posting")
		}
		if err := planner.put(post); err != nil {
			return err
		}
	}

	v := &collectionView{Sequence: old.Sequence + 1, Sources: make(map[objectBinding]*collectionSource, len(old.Sources)+1)}
	for key, src := range old.Sources {
		v.Sources[key] = src
	}
	v.Sources[k] = s
	raw := encodeCollectionDirectory(v.Sources)
	for n := 0; n < (len(raw)+4079)/4080; n++ {
		id, e := alloc()
		if e != nil {
			return e
		}
		v.Metadata = append(v.Metadata, id)
	}
	fail := func(e error) error { i.failed = e; return e }
	write := func(raw []byte, at int64) error {
		n, e := i.file.WriteAt(raw, at)
		if e == nil && n != len(raw) {
			e = io.ErrShortWrite
		}
		return e
	}
	if old.Sequence == 0 {
		if e := i.file.Truncate(4096); e != nil {
			return fail(e)
		}
	}
	for id, p := range dirty {
		p.seal()
		if e := write(p[:], int64(id)*4096); e != nil {
			return fail(e)
		}
	}
	for n, id := range v.Metadata {
		var p [4096]byte
		copy(p[:4], "CDIR")
		if n+1 < len(v.Metadata) {
			order.PutUint32(p[4:], v.Metadata[n+1])
		}
		copy(p[16:], raw[n*4080:min(len(raw), (n+1)*4080)])
		order.PutUint32(p[8:], crc32.Checksum(p[:], checksum))
		if e := write(p[:], int64(id)*4096); e != nil {
			return fail(e)
		}
	}
	if e := i.file.Sync(); e != nil {
		return fail(e)
	}
	if i.fault != nil {
		if e := i.fault("before-root"); e != nil {
			return fail(e)
		}
	}
	root := collectionRoot(v, len(raw))
	slot := v.Sequence % 2
	if e := write(root[:], int64(slot)*64); e != nil {
		return fail(e)
	}
	if e := i.file.Sync(); e != nil {
		return fail(e)
	}
	if i.fault != nil {
		if e := i.fault("after-root"); e != nil {
			return fail(e)
		}
	}
	i.publication.Lock()
	if replaced := i.roots[slot]; replaced != nil {
		i.retired = append(i.retired, replaced)
	}
	i.roots[slot] = v
	i.view = v
	i.sources = v.Sources
	i.through[k] = through
	i.next = next
	i.publication.Unlock()
	return nil
}
