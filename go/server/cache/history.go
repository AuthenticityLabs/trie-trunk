package cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"time"
)

// EventReference names a committed canonical row. It carries its immutable map
// and physical generation; retention cannot silently rebind an old reference.
type EventReference struct {
	Definition [32]byte
	Generation [16]byte
	Branch     Branch
	Row        uint64
}

func (p *Endpoint) Resolve(ref EventReference) ([]byte, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("endpoint closed")
	}
	api := p.apis[fmt.Sprintf("%x", ref.Definition)]
	p.mu.Unlock()
	if api == nil {
		return nil, fmt.Errorf("unknown source definition")
	}
	s := api.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	b := s.branches[ref.Branch]
	if b == nil || b.generation != ref.Generation {
		return nil, fmt.Errorf("stale or unknown source generation")
	}
	if ref.Row < b.baseRows || ref.Row >= b.tail() {
		return nil, fmt.Errorf("row outside committed source")
	}
	return s.frame(b, ref.Row, nil)
}

// A history snapshot pins its index mappings and population descriptors. Its
// immutable trie prefix survives concurrent append, retention, and endpoint close.
// Object payloads are not copied into an intermediate event collection.
type historySource struct {
	layout        *Layout
	row           RowLayout
	branch        branchStore
	trie          sharedTrie
	release       func()
	memoryRelease func()
	output        *nativeBuffer
	reader        populationReader
	assembler     frameAssembler
}
type historyLocation struct {
	source  *historySource
	row     uint64
	binding string
	id      [16]byte
	raw     []byte // immutable resident or provisional full PIL frame
}
type historySnapshot struct {
	sources   []*historySource
	locations []historyLocation
	token     StateToken
}

func (h *historySnapshot) close() {
	for _, s := range h.sources {
		s.output.close()
		if s.memoryRelease != nil {
			s.memoryRelease()
			s.memoryRelease = nil
		}
		if s.branch.pop != nil {
			s.branch.pop.Close()
		}
		if s.release != nil {
			s.release()
		}
	}
}
func (h *historySnapshot) visit(ctx context.Context, visit func(EventReference, []byte) error) error {
	return h.visitFrames(ctx, visit, false)
}

// HTTP consumers must neither mutate nor retain these snapshot-pinned bytes.
func (h *historySnapshot) visitBorrowed(ctx context.Context, visit func(EventReference, []byte) error) error {
	return h.visitFrames(ctx, visit, true)
}
func (h *historySnapshot) visitFrames(ctx context.Context, visit func(EventReference, []byte) error, borrowed bool) error {
	var output []byte
	for _, v := range h.locations {
		if err := ctx.Err(); err != nil {
			return err
		}
		s := v.source
		var err error
		if v.raw != nil {
			if borrowed {
				if err = visit(EventReference{s.layout.Digest, s.branch.generation, Write, v.row}, v.raw); err != nil {
					return err
				}
				continue
			}
			buffer, e := s.output.resize(len(v.raw))
			if e != nil {
				return e
			}
			copy(buffer, v.raw)
			if err = visit(EventReference{s.layout.Digest, s.branch.generation, Write, v.row}, buffer); err != nil {
				return err
			}
			continue
		} else {
			raw, e := s.reader.read(&s.branch, v.row, s.row)
			if e != nil {
				return e
			}
			row := s.branch.assemblyRow(v.row)
			output, err = s.assembler.assemble(row, raw, output)
			if err != nil {
				return err
			}
		}
		if err = visit(EventReference{s.layout.Digest, s.branch.generation, Write, v.row}, output); err != nil {
			return err
		}
	}
	return nil
}

// History follows optional collection.idx postings across all immutable minors in
// event order, including acknowledged pending writes. Revisions are opaque.
// Provisional callbacks use ProvisionalRow, never a durable physical address.
// Callback bytes are borrowed until the next callback. No writer lock is held
// during payload reads or callbacks, and no object-ID scan fallback is possible.
func (p *Endpoint) History(ctx context.Context, identity string, major uint32, id Value, from, to time.Time, visit func(EventReference, []byte) error) error {
	h, err := p.snapshotHistory(identity, major, id, from, to)
	if err != nil {
		return err
	}
	defer h.close()
	return h.visit(ctx, visit)
}
func (p *Endpoint) snapshotHistory(identity string, major uint32, id Value, from, to time.Time) (snapshot *historySnapshot, err error) {
	if !from.Before(to) {
		return nil, fmt.Errorf("invalid history time window")
	}
	p.mu.Lock()
	if p.closed || p.group == nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("cache unavailable")
	}
	sources := p.sources()
	g := p.group
	g.mu.Lock()
	p.mu.Unlock()
	defer g.mu.Unlock()
	h := &historySnapshot{}
	defer func() {
		if err != nil {
			h.close()
		}
	}()
	found := false
	for _, api := range sources {
		s := api.Store
		if s.layout.Definition.Identity != identity || s.layout.Definition.Resource.Major != major {
			continue
		}
		found = true
		if err = s.check(); err != nil {
			return nil, err
		}
		if !s.config.ObjectIndex || s.objects == nil {
			return nil, ErrObjectIndexDisabled
		}
		object := s.layout.IdentityValue(s.layout.Object, id)
		if e := s.validateObject(object); e != nil {
			return nil, e
		}
		h.token = s.token(object)
		b := s.branches[Write]
		if b == nil {
			continue
		}
		// Sealing belongs to maintenance/ingress. Replay reads merge durable
		// pending data and committed data without initiating disk writes.
		source := &historySource{layout: s.layout, row: s.row, branch: branchStore{baseRows: b.baseRows, popRetired: b.popRetired, blockBase: b.blockBase, layout: s.layout, runs: append([]Run(nil), b.runs...), generation: b.generation, popEnd: b.popEnd, time: b.time, timeAnchor: b.timeAnchor}, trie: sharedTrie{nodes: s.trie.nodes}, reader: populationReader{point: true}}
		source.memoryRelease = b.pinMemory()
		source.output = s.memory.buffer()
		source.assembler = frameAssembler{layout: source.layout, trie: &source.trie}
		h.sources = append(h.sources, source)
		if b.residentHistory(object.Bytes, from, to, s.config.Now(), source, s.binding, h) {
			continue
		}
		b.live.prune(s.config.Now())
		selected, e := s.objectRows(b, object, from, to)
		if e != nil {
			return nil, e
		}
		pending := b.pendingWindow(object.Bytes, from, to)
		if len(selected)+len(pending) == 0 {
			continue
		}
		for _, row := range selected {
			var event [16]byte
			copy(event[:], b.rowID(row))
			raw := b.live.get(event[:], false, s.config.Now())
			if raw == nil && source.branch.pop == nil {
				source.branch.rows, source.release, e = b.snapshotIndex()
				if e != nil {
					return nil, e
				}
				source.branch.pop, e = os.Open(b.pop.Name())
				if e != nil {
					return nil, e
				}
			}
			h.locations = append(h.locations, historyLocation{source: source, row: row, binding: s.binding, id: event, raw: raw})
		}
		for _, p := range pending {
			h.locations = append(h.locations, historyLocation{source: source, row: ProvisionalRow, binding: s.binding, id: p.id, raw: p.frame})
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown collection")
	}
	sort.Slice(h.locations, func(i, j int) bool {
		if c := bytes.Compare(h.locations[i].id[:], h.locations[j].id[:]); c != 0 {
			return c < 0
		}
		return h.locations[i].binding < h.locations[j].binding
	})
	return h, nil
}
