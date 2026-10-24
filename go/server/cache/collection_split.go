package cache

import (
	"bytes"
	"fmt"
	"sort"
)

// Continuations are private to one object. Only the first page participates in
// UUID routing. A split planner follows the active object's chain, not all pages
// in the file; the retained path supplies every incoming link that must change.
type collectionSplitPlanner struct {
	index   *objectIndex
	source  *collectionSource
	binding objectBinding
	dirty   map[uint32]*collectionPage
	alloc   func() (uint32, error)
}

func (t *collectionSplitPlanner) read(id uint32) (collectionPage, error) {
	if p := t.dirty[id]; p != nil {
		return *p, nil
	}
	return t.index.readPage(id, t.binding)
}
func (t *collectionSplitPlanner) save(old uint32, p collectionPage) (uint32, error) {
	id := old
	if id == 0 || t.dirty[id] == nil {
		var e error
		id, e = t.alloc()
		if e != nil {
			return 0, e
		}
	}
	if old != 0 {
		delete(t.source.Overflow, old)
	}
	t.dirty[id] = &p
	if p[29] == collectionOverflowPage {
		t.source.Overflow[id] = true
	}
	return id, nil
}
func continuationSlot(p *collectionPage, link collectionLink, id []byte) (int, error) {
	if p[29] != collectionOverflowPage || p.used() == 0 || p.end(0) != p.used() ||
		p.at(0)/32 != int(link.Slot) || !bytes.Equal(p.key(0), id) {
		return 0, fmt.Errorf("invalid collection continuation owner/slot")
	}
	return 0, nil
}
func collectionLastRevision(p *collectionPage, n int) uint64 {
	cap, base := p.capacity(n), p.base(n)
	for j := cap - 1; j >= 0; j-- {
		if base+uint64(j) <= collectionMaxRevision && p.ref(n, (p.head(n)+j)%cap) != 0 {
			return base + uint64(j)
		}
	}
	return base
}

type collectionRevisionRef struct{ revision, reference uint64 }
type collectionGroup struct {
	id      [16]byte
	entries []collectionRevisionRef
	link    collectionLink
}

func (t *collectionSplitPlanner) rebuild(p collectionPage, post collectionPosting) ([]collectionPage, error) {
	groups := make([]collectionGroup, 0)
	found := false
	for n := 0; n < p.used(); n = p.end(n) {
		g := collectionGroup{link: p.link(n)}
		copy(g.id[:], p.key(n))
		p.visit(n, func(r, v uint64) { g.entries = append(g.entries, collectionRevisionRef{r, v}) })
		if g.id == post.Object {
			found = true
			if e := g.insert(post); e != nil {
				return nil, e
			}
		}
		groups = append(groups, g)
	}
	if !found {
		g := collectionGroup{id: post.Object}
		_ = g.insert(post)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(a, b int) bool { return bytes.Compare(groups[a].id[:], groups[b].id[:]) < 0 })
	template := newCollectionPage(t.binding)
	template[30] = max(p[30], byte(uintWidth(uint64(post.Address.Hour), 2)))
	template[31] = max(p[31], byte(uintWidth(uint64(post.Address.Record), 3)))

	// Balance a UUID page split at object boundaries instead of leaving a
	// nearly full left page that splits again on the next insertion.
	units := 0
	for _, g := range groups {
		limit := template.maxCapacity()
		link := g.link.Page != 0 || g.entries[len(g.entries)-1].revision-g.entries[0].revision >= uint64(limit)
		if link {
			limit--
		}
		end := sort.Search(len(g.entries), func(n int) bool { return g.entries[n].revision-g.entries[0].revision >= uint64(limit) })
		span := int(g.entries[end-1].revision-g.entries[0].revision) + 1
		if link {
			span++
		}
		units += template.unitsFor(span)
	}
	pages := (units + collectionUnits - 1) / collectionUnits
	target := (units + pages - 1) / pages
	var out []collectionPage
	current := template
	current[29] = p[29]
	for _, g := range groups {
		head, e := t.packGroup(template, g)
		if e != nil {
			return nil, e
		}

		if current.used() > 0 && current.used()+head.used() > target {
			out = append(out, current)
			current = template
			current[29] = p[29]
		}
		candidate := current
		e = head.copyGroup(0, &candidate)
		if e == errCollectionPageFull {
			if current.used() == 0 {
				return nil, e
			}
			out = append(out, current)
			current = template
			current[29] = p[29]
			if e = head.copyGroup(0, &current); e != nil {
				return nil, e
			}
		} else if e != nil {
			return nil, e
		} else {
			current = candidate
		}
	}
	if current.used() > 0 {
		out = append(out, current)
	}
	return out, nil
}
func (g *collectionGroup) insert(post collectionPosting) error {
	at := sort.Search(len(g.entries), func(n int) bool { return g.entries[n].revision >= post.Revision })
	if at < len(g.entries) && g.entries[at].revision == post.Revision {
		if g.entries[at].reference != post.Address.packed() {
			return fmt.Errorf("object revision already references a different event")
		}
		return nil
	}
	g.entries = append(g.entries, collectionRevisionRef{})
	copy(g.entries[at+1:], g.entries[at:])
	g.entries[at] = collectionRevisionRef{post.Revision, post.Address.packed()}
	return nil
}

// At most one existing page's entries are decoded for a split. Large gaps start
// a new continuation base rather than reserving millions of empty positions.
func (t *collectionSplitPlanner) packGroup(template collectionPage, g collectionGroup) (collectionPage, error) {
	var cuts []int
	for start := 0; start < len(g.entries); {
		limit := template.maxCapacity()
		if g.link.Page != 0 || g.entries[len(g.entries)-1].revision-g.entries[start].revision >= uint64(limit) {
			limit--
		}
		end := start + 1
		for end < len(g.entries) && g.entries[end].revision-g.entries[start].revision < uint64(limit) {
			end++
		}
		cuts = append(cuts, start)
		start = end
	}
	next := g.link
	for part := len(cuts) - 1; part >= 0; part-- {
		start, end := cuts[part], len(g.entries)
		if part+1 < len(cuts) {
			end = cuts[part+1]
		}
		q := template
		for _, v := range g.entries[start:end] {
			if e := q.put(g.id, v.revision, v.reference); e != nil {
				return q, e
			}
		}
		if next.Page != 0 {
			high := g.entries[end-1].revision
			needed := q.unitsFor(int(high-q.base(0)) + 2)
			for q.used() < needed {
				q.add(q.used(), true)
			}
			q.setLink(0, next)
		}
		if part == 0 {
			return q, nil
		}
		q[29] = collectionOverflowPage
		id, e := t.save(0, q)
		if e != nil {
			return q, e
		}
		next = collectionLink{id, byte(q.at(0) / 32)}
	}
	return collectionPage{}, fmt.Errorf("empty collection group")
}
func (t *collectionSplitPlanner) put(post collectionPosting) error {
	if len(t.source.Fences) == 0 {
		p := newCollectionPage(t.binding)
		if e := p.put(post.Object, post.Revision, post.Address.packed()); e != nil {
			return e
		}
		id, e := t.save(0, p)
		if e != nil {
			return e
		}
		t.source.Fences = append(t.source.Fences, collectionFence{post.Object, id})
		return nil
	}
	fence := max(0, collectionRoute(t.source.Fences, post.Object[:]))
	pageID := t.source.Fences[fence].Page
	type predecessor struct {
		page    uint32
		pointer int
	}
	var path []predecessor
	p, e := t.read(pageID)
	if e != nil {
		return e
	}
	for {
		n := p.find(post.Object[:])
		if n < 0 || !p.linked(n) {
			break
		}
		link := p.link(n)
		child, err := t.read(link.Page)
		if err != nil {
			return err
		}
		if _, err = continuationSlot(&child, link, post.Object[:]); err != nil {
			return err
		}
		if child.base(0) <= collectionLastRevision(&p, n) {
			return fmt.Errorf("non-increasing collection chain")
		}
		if post.Revision < child.base(0) {
			break
		}
		path = append(path, predecessor{pageID, n})
		pageID, p = link.Page, child
	}
	// The common path writes straight into a private page, without decoding its
	// other objects. Only full pages or width expansion need the split planner.
	e = p.put(post.Object, post.Revision, post.Address.packed())
	var replacements []collectionPage
	if e == nil {
		replacements = []collectionPage{p}
	} else if e == errCollectionPageFull {
		replacements, e = t.rebuild(p, post)
		if e != nil {
			return e
		}
	} else {
		return e
	}
	var fences []collectionFence
	var first uint32
	for n, q := range replacements {
		prior := uint32(0)
		if n == 0 {
			prior = pageID
		}
		id, err := t.save(prior, q)
		if err != nil {
			return err
		}
		if n == 0 {
			first = id
		}
		var key [16]byte
		copy(key[:], q.key(0))
		fences = append(fences, collectionFence{key, id})
	}
	if len(path) > 0 && len(replacements) != 1 {
		return fmt.Errorf("continuation split created UUID fences")
	}
	// Repair only the known incoming path. Published pages are never patched.
	for n := len(path) - 1; n >= 0; n-- {
		prev, err := t.read(path[n].page)
		if err != nil {
			return err
		}
		child := t.dirty[first]
		prev.setLink(path[n].pointer, collectionLink{first, byte(child.at(0) / 32)})
		first, err = t.save(path[n].page, prev)
		if err != nil {
			return err
		}
	}
	if len(path) > 0 {
		t.source.Fences[fence].Page = first
		return nil
	}
	t.source.Fences = append(t.source.Fences[:fence], append(fences, t.source.Fences[fence+1:]...)...)
	return nil
}
