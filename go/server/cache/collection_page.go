package cache

// Fixed 32-byte units use a sorted virtual pointer array. Source binding and
// independent reference widths live in the page header.
import (
	"bytes"
	"fmt"
	"hash/crc32"
)

const collectionPageBytes = 4096
const collectionPageHeader = 37
const collectionUnits = 123
const collectionMaxRevision = 1<<24 - 1
const collectionLinked = 1
const collectionOverflowPage = 2
const collectionEmpty = 128

type collectionPage [collectionPageBytes]byte

func newCollectionPage(k objectBinding) collectionPage {
	var p collectionPage
	copy(p[:4], "OBJ0")
	copy(p[4:20], k.Generation[:])
	order.PutUint32(p[20:24], k.Minor)
	p[29] = 1
	p[30], p[31] = 2, 3
	for n := 37; n < 160; n++ {
		p[n] = collectionEmpty
	}
	return p
}
func (p *collectionPage) used() int        { return int(p[28]) }
func (p *collectionPage) start(n int) bool { return p[37+n]&128 == 0 }
func (p *collectionPage) at(n int) int     { return int(p[37+n]&127) * 32 }
func (p *collectionPage) key(n int) []byte { a := p.at(n); return p[a : a+16] }
func (p *collectionPage) end(n int) int {
	n++
	for n < p.used() && !p.start(n) {
		n++
	}
	return n
}
func (p *collectionPage) parentReferences() int { return 10 / p.referenceWidth() }
func (p *collectionPage) rawCapacity(n int) int {
	return p.parentReferences() + (p.end(n)-n-1)*p.perExtension()
}
func (p *collectionPage) linked(n int) bool { return p[p.at(n)+21]&collectionLinked != 0 }
func (p *collectionPage) capacity(n int) int {
	v := p.rawCapacity(n)
	if p.linked(n) {
		v--
	}
	return v
}
func (p *collectionPage) base(n int) uint64   { a := p.at(n); return getUintWidth(p[a+16 : a+19]) }
func (p *collectionPage) head(n int) int      { return int(order.Uint16(p[p.at(n)+19:])) }
func (p *collectionPage) referenceWidth() int { return int(p[30]) + int(p[31]) }
func (p *collectionPage) perExtension() int   { return 32 / p.referenceWidth() }
func (p *collectionPage) maxCapacity() int {
	return p.parentReferences() + (collectionUnits-1)*p.perExtension()
}
func (p *collectionPage) unitsFor(span int) int {
	return 1 + (max(0, span-p.parentReferences())+p.perExtension()-1)/p.perExtension()
}
func (p *collectionPage) refAt(n, pos int) int {
	parents := p.parentReferences()
	if pos < parents {
		return p.at(n) + 22 + pos*p.referenceWidth()
	}
	per := p.perExtension()
	return p.at(n+1+(pos-parents)/per) + (pos-parents)%per*p.referenceWidth()
}

type collectionLink struct {
	Page uint32
	Slot byte
}

func (p *collectionPage) link(n int) collectionLink {
	if !p.linked(n) {
		return collectionLink{}
	}
	a := p.refAt(n, p.rawCapacity(n)-1)
	return collectionLink{order.Uint32(p[a:]), p[a+4]}
}
func (p *collectionPage) setLink(n int, link collectionLink) {
	a := p.refAt(n, p.rawCapacity(n)-1)
	clear(p[a : a+p.referenceWidth()])
	order.PutUint32(p[a:], link.Page)
	p[a+4] = link.Slot
	p[p.at(n)+21] |= collectionLinked
}
func (p *collectionPage) ref(n, pos int) uint64 {
	a := p.refAt(n, pos)
	h := int(p[30])
	r := int(p[31])
	return getUintWidth(p[a:a+h])<<32 | getUintWidth(p[a+h:a+h+r])
}
func (p *collectionPage) putRef(n, pos int, v uint64) {
	a := p.refAt(n, pos)
	h := int(p[30])
	r := int(p[31])
	putUintWidth(p[a:a+h], v>>32)
	putUintWidth(p[a+h:a+h+r], uint64(uint32(v)))
}
func (p *collectionPage) widen(h, r byte) error {
	h = max(h, p[30])
	r = max(r, p[31])
	if h == p[30] && r == p[31] {
		return nil
	}
	var k objectBinding
	copy(k.Generation[:], p[4:20])
	k.Minor = order.Uint32(p[20:24])
	next := newCollectionPage(k)
	next[30], next[31], next[29] = h, r, p[29]
	for n := 0; n < p.used(); n = p.end(n) {
		if err := p.copyGroup(n, &next); err != nil {
			return err
		}
	}
	*p = next
	return nil
}
func (p *collectionPage) find(key []byte) int {
	lo, hi, right := 0, p.used(), true
	for lo < hi {
		mid := lo + (hi-lo)/2
		found := -1
		if right {
			for n := mid; n < hi; n++ {
				if p.start(n) {
					found = n
					break
				}
			}
			if found < 0 {
				for n := mid - 1; n >= lo; n-- {
					if p.start(n) {
						found = n
						break
					}
				}
			}
		} else {
			for n := mid; n >= lo; n-- {
				if p.start(n) {
					found = n
					break
				}
			}
			if found < 0 {
				for n := mid + 1; n < hi; n++ {
					if p.start(n) {
						found = n
						break
					}
				}
			}
		}
		if found < 0 {
			return -1
		}
		c := bytes.Compare(p.key(found), key)
		if c == 0 {
			return found
		}
		if c < 0 {
			lo = found + 1
		} else {
			hi = found
		}
		right = !right
	}
	return -1
}
func (p *collectionPage) reference(n int, revision uint64) uint64 {
	base, cap := p.base(n), p.capacity(n)
	if revision < base || revision-base >= uint64(cap) {
		return 0
	}
	return p.ref(n, (p.head(n)+int(revision-base))%cap)
}
func (p *collectionPage) visit(n int, fn func(uint64, uint64)) {
	base, cap := p.base(n), p.capacity(n)
	for j := 0; j < cap && base+uint64(j) <= collectionMaxRevision; j++ {
		if v := p.ref(n, (p.head(n)+j)%cap); v != 0 {
			fn(base+uint64(j), v)
		}
	}
}
func (p *collectionPage) add(pos int, ext bool) {
	var occupied [128]bool
	for n := 0; n < p.used(); n++ {
		occupied[p[37+n]&127] = true
	}
	unit := 5
	for occupied[unit] {
		unit++
	}
	used := p.used()
	copy(p[38+pos:38+used], p[37+pos:37+used])
	p[37+pos] = byte(unit)
	if ext {
		p[37+pos] |= 128
	}
	clear(p[unit*32 : (unit+1)*32])
	p[28]++
}

var errCollectionPageFull = fmt.Errorf("collection object page full")

func (p *collectionPage) put(id [16]byte, rev, ref uint64) error {
	if rev > collectionMaxRevision || uint32(ref) == 0 {
		return fmt.Errorf("invalid collection revision/reference")
	}
	if e := p.widen(byte(uintWidth(ref>>32, 2)), byte(uintWidth(uint64(uint32(ref)), 3))); e != nil {
		return e
	}
	n := p.find(id[:])
	if n < 0 {
		if p.used() == collectionUnits {
			return errCollectionPageFull
		}
		n = 0
		for n < p.used() && bytes.Compare(p.key(n), id[:]) < 0 {
			n = p.end(n)
		}
		p.add(n, false)
		a := p.at(n)
		copy(p[a:a+16], id[:])
		putUintWidth(p[a+16:a+19], rev)
		p.putRef(n, 0, ref)
		return nil
	}
	if old := p.reference(n, rev); old != 0 {
		if old == ref {
			return nil
		}
		return fmt.Errorf("object revision already references a different event")
	}
	base, cap := p.base(n), p.capacity(n)
	if rev >= base && rev-base < uint64(cap) {
		p.putRef(n, (p.head(n)+int(rev-base))%cap, ref)
		return nil
	}
	// Linked groups cannot grow over their link; the split planner rebuilds them.
	if p.linked(n) {
		return errCollectionPageFull
	}
	low, high := min(base, rev), rev
	p.visit(n, func(r, v uint64) { high = max(high, r) })
	if high-low >= uint64(p.maxCapacity()) {
		return errCollectionPageFull
	}
	wanted := p.unitsFor(int(high-low) + 1)
	if p.used()+max(0, wanted-(p.end(n)-n)) > collectionUnits {
		return errCollectionPageFull
	}
	// Appending an unrotated group does not copy its existing references.
	if low == base && p.head(n) == 0 {
		for p.end(n)-n < wanted {
			p.add(p.end(n), true)
		}
		p.putRef(n, int(rev-base), ref)
		return nil
	}
	var values [1024]uint64
	p.visit(n, func(r, v uint64) { values[r-low] = v })
	values[rev-low] = ref
	for p.end(n)-n < wanted {
		p.add(p.end(n), true)
	}
	a := p.at(n)
	putUintWidth(p[a+16:a+19], low)
	order.PutUint16(p[a+19:], 0)
	for j := 0; j < p.capacity(n); j++ {
		p.putRef(n, j, values[j])
	}
	return nil
}
func (p *collectionPage) copyGroup(n int, to *collectionPage) error {
	var id [16]byte
	copy(id[:], p.key(n))
	var err error
	p.visit(n, func(rev, ref uint64) {
		if err == nil {
			err = to.put(id, rev, ref)
		}
	})
	if err != nil {
		return err
	}
	if p.linked(n) {
		at := to.find(id[:])
		// Reserve an additional position for the link, never overwrite a revision.
		var high uint64
		to.visit(at, func(r, v uint64) { high = r })
		needed := to.unitsFor(int(high-to.base(at)) + 2)
		if to.used()+max(0, needed-(to.end(at)-at)) > collectionUnits {
			return errCollectionPageFull
		}
		for to.end(at)-at < needed {
			to.add(to.end(at), true)
		}
		to.setLink(at, p.link(n))
	}
	return nil
}
func (p *collectionPage) seal() {
	clear(p[24:28])
	order.PutUint32(p[24:28], crc32.Checksum(p[:], checksum))
}
func (p *collectionPage) validate(k objectBinding) error {
	want := order.Uint32(p[24:28])
	clear(p[24:28])
	got := crc32.Checksum(p[:], checksum)
	order.PutUint32(p[24:28], want)
	if string(p[:4]) != "OBJ0" || (p[29] != 1 && p[29] != collectionOverflowPage) || p[30] < 2 || p[30] > 4 || p[31] < 3 || p[31] > 4 || p.used() > collectionUnits || want != got || !bytes.Equal(p[4:20], k.Generation[:]) || order.Uint32(p[20:24]) != k.Minor {
		return fmt.Errorf("invalid collection page header/checksum")
	}
	for _, v := range p[32:37] {
		if v != 0 {
			return fmt.Errorf("collection reserved byte")
		}
	}
	var occupied [128]bool
	var last []byte
	for n := 0; n < p.used(); n++ {
		u := p[37+n] & 127
		if u < 5 || occupied[u] || n == 0 && !p.start(n) {
			return fmt.Errorf("invalid collection slot")
		}
		occupied[u] = true
		if p.start(n) {
			if last != nil && bytes.Compare(last, p.key(n)) >= 0 {
				return fmt.Errorf("unordered collection UUID")
			}
			last = p.key(n)
		}
	}
	for n := p.used(); n < 123; n++ {
		if p[37+n] != collectionEmpty {
			return fmt.Errorf("invalid unused collection pointer")
		}
	}
	for n := 0; n < p.used(); n = p.end(n) {
		a := p.at(n)
		cap, base, head := p.capacity(n), p.base(n), p.head(n)
		if head >= cap || p[a+21]&^byte(collectionLinked) != 0 {
			return fmt.Errorf("invalid collection parent")
		}
		if p.linked(n) {
			link := p.link(n)
			if link.Page == 0 || link.Slot < 5 || link.Slot > 127 {
				return fmt.Errorf("invalid collection continuation")
			}
		}
		for j := max(0, int(collectionMaxRevision-base)+1); j < cap; j++ {
			if p.ref(n, (head+j)%cap) != 0 {
				return fmt.Errorf("collection revision overflow")
			}
		}
	}
	return nil
}
