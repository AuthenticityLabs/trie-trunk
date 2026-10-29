package cache

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sequence orders physical replacements only. It is independent of definition
// minors, event revisions, and invalidation tokens. Initial sources begin at 1.
func (s *Store) generationSequence(branch Branch) uint64 {
	if sequence := s.catalog.GenerationSequences[s.binding][branch]; sequence > 0 {
		return sequence
	}
	return 1
}

func (s *Store) prefix(branch Branch) string {
	if n := s.catalog.RetiredRows[s.binding][branch]; n > 0 {
		return fmt.Sprintf("%s.r%d", branch.Prefix(), n)
	}
	if generation, ok := s.catalog.Generations[s.binding][branch]; ok {
		return branch.Prefix() + ".g" + hex.EncodeToString(generation[:])
	}
	return branch.Prefix()
}

// ApplyRetention copies retained encoded extents without changing references. No file is deleted
// until the catalog durably selects the replacement. Invalidation state and
// definition/shared-reference identities are not rolled off with event payloads.
func (s *Store) ApplyRetention() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	for branch := Read; branch <= Trace; branch++ {
		durations, e := s.policyFor(branch).Retention.Durations()
		if e != nil {
			return e
		}
		if duration := durations[branch]; duration > 0 {
			if e = s.prune(branch, s.config.Now().Add(-duration)); e != nil {
				return s.fail(e)
			}
		}
	}
	return nil
}

// Retention removes whole sealed-hour prefixes. Source identities, row numbers,
// block numbers and logical member coordinates remain unchanged.
func (s *Store) prune(branch Branch, cutoff time.Time) (err error) {
	s.waitSealing()
	if err = s.check(); err != nil {
		return err
	}
	old := s.branches[branch]
	if old == nil {
		return nil
	}
	if err = s.flush(old); err != nil {
		return err
	}
	count := 0
	for count < len(old.runs) && old.runs[count].PAC.Placement.To <= cutoff.UnixMilli() {
		count++
	}
	if count == 0 {
		return nil
	}
	loadedObjects := false
	if branch == Write && s.objects == nil {
		if _, e := os.Stat(filepath.Join(s.collection, "collection.idx")); e == nil {
			enabled := s.config.ObjectIndex
			s.config.ObjectIndex = true
			err = s.openObjects()
			s.config.ObjectIndex = enabled
			if err != nil {
				return err
			}
			loadedObjects = true
		}
	}
	defer func() {
		if loadedObjects && s.config.group == nil && s.objects != nil {
			s.objects.file.Close()
			s.objects = nil
		}
	}()
	last := old.runs[count-1]
	baseRows := last.End
	podsRetired := last.PodsEnd - headerSize
	memberEnd := last.PAC.Placement.MemberStart + last.PAC.Placement.MemberBytes
	blockBase := last.PAC.Placement.Block + 1
	base := filepath.Join(s.dir, fmt.Sprintf("%s.r%d", branch.Prefix(), baseRows))
	next := &branchStore{branch: branch, layout: s.layout, generation: old.generation, baseRows: baseRows, podsRetired: podsRetired, memberEnd: memberEnd, blockBase: blockBase, podsEnd: headerSize + podsRetired, sumEnd: headerSize, time: timeDirectory{}}
	var created []string
	published := false
	defer func() {
		if !published {
			next.close()
			for _, p := range created {
				os.Remove(p)
			}
		}
	}()
	create := func(ext, magic string, width int) (*os.File, error) {
		p := base + ext
		created = append(created, p)
		if e := os.Remove(p); e != nil && !os.IsNotExist(e) {
			return nil, e
		}
		return openFile(p, magic, s.layout.Digest, old.generation, width)
	}
	if next.idx, err = create(".idx", "NUTSIDX6", IndexRowSize); err != nil {
		return err
	}
	if next.pods, err = create(".pods", "PILPOD00", 0); err != nil {
		return err
	}
	if next.journal, err = create(".journal", "TTCOMM00", IndexRowSize); err != nil {
		return err
	}
	if old.sum != nil {
		if next.sum, err = create(".sum", "NUTSSUM6", 0); err != nil {
			return err
		}
	}
	if err = setRetiredBytes(next.idx, baseRows*IndexRowSize); err != nil {
		return err
	}
	if err = setRetiredBytes(next.pods, podsRetired); err != nil {
		return err
	}
	first := min(utcHour(cutoff.UnixMilli()), utcHour(old.finalizedBefore))
	if count < len(old.runs) {
		first = min(first, old.runs[count].PAC.Placement.From)
	}
	anchor := &TimeAnchor{First: first, Origin: old.time.origin()}
	next.timeAnchor = anchor
	retirement := Retirement{Time: anchor, Rows: baseRows, Members: memberEnd, Block: blockBase, Cutoff: old.finalizedBefore}
	raw, e := encoder.Marshal(Commit{Retired: &retirement})
	if e != nil {
		return e
	}
	if err = appendRecord(next.journal, raw); err != nil {
		return err
	}
	for i, run := range old.runs[count:] {
		idx := old.rows[(run.Start-old.baseRows)*IndexRowSize : (run.End-old.baseRows)*IndexRowSize]
		pods, e := readBytes(old.pods, run.PodsStart-old.podsRetired, run.PodsEnd-old.podsRetired)
		if e != nil {
			return e
		}
		if _, err = next.idx.WriteAt(indexHeader(run.PAC.Placement.From, run.End-run.Start), headerSize+int64((run.Start-IndexHeaderSlots-baseRows)*IndexRowSize)); err != nil {
			return err
		}
		if _, err = next.idx.WriteAt(idx, headerSize+int64((run.Start-baseRows)*IndexRowSize)); err != nil {
			return err
		}
		if _, err = next.pods.WriteAt(pods, int64(run.PodsStart-podsRetired)); err != nil {
			return err
		}
		if next.sum != nil {
			summary := old.summaries[count+i]
			raw, e := encodeSummary(summary)
			if e != nil {
				return e
			}
			if err = appendRecord(next.sum, raw); err != nil {
				return err
			}
			stat, e := next.sum.Stat()
			if e != nil {
				return e
			}
			next.sumEnd = uint64(stat.Size())
			next.summaries = append(next.summaries, summary)
		}
		raw, e := encoder.Marshal(Commit{Run: run})
		if e != nil {
			return e
		}
		if err = appendRecord(next.journal, raw); err != nil {
			return err
		}
		next.runs = append(next.runs, run)
		next.podsEnd = run.PodsEnd
	}
	for _, f := range []*os.File{next.idx, next.pods, next.journal, next.sum} {
		if f != nil {
			if err = f.Sync(); err != nil {
				return err
			}
		}
	}
	if err = syncDir(s.dir); err != nil {
		return err
	}
	if err = next.mapIndex(); err != nil {
		return err
	}
	next.finalizedBefore = old.finalizedBefore
	if err = s.packSummaryDays(next); err != nil {
		return err
	}
	var widths [6]byte
	copy(widths[:], old.time.raw[40:46])
	next.time, err = buildHourDirectory(next.runs, first, next.finalizedBefore, next.tail(), next.generation, old.hourOrigin(), widths)
	if err != nil {
		return err
	}
	created = append(created, base+".tri")
	if err = replace(base+".tri", next.time.raw); err != nil {
		return err
	}
	// Publish physical filenames atomically; the source identity does not change.
	catalog := s.catalog
	catalog.RetiredRows = map[string]map[Branch]uint64{}
	for binding, branches := range s.catalog.RetiredRows {
		m := map[Branch]uint64{}
		for k, v := range branches {
			m[k] = v
		}
		catalog.RetiredRows[binding] = m
	}
	if catalog.RetiredRows[s.binding] == nil {
		catalog.RetiredRows[s.binding] = map[Branch]uint64{}
	}
	catalog.RetiredRows[s.binding][branch] = baseRows
	raw, e = encoder.Marshal(catalog)
	if e != nil {
		return e
	}
	if branch == Write {
		if err = s.invalidate(nil, true); err != nil {
			return err
		}
	}
	if err = replace(filepath.Join(s.root, "registry.map"), raw); err != nil {
		return err
	}
	published = true
	s.catalog = catalog
	if g := s.config.group; g != nil {
		g.catalog(catalog)
	}
	next.swap, old.swap = old.swap, nil
	next.live, old.live = old.live, nil
	next.accumulators, old.accumulators = old.accumulators, nil
	next.pendingBytes, next.pendingVersion = old.pendingBytes, old.pendingVersion
	next.finalizedBefore = old.finalizedBefore
	next.memory, old.memory = old.memory, nil
	next.live.prune(s.config.Now())
	s.branches[branch] = next
	s.queueSearch(branch)
	defer old.close() // also release old mappings if derived-index maintenance fails
	if branch == Write && s.objects != nil {
		if err = s.compactObjects(); err != nil {
			return err
		}
	}
	paths := []string{old.idx.Name(), old.pods.Name(), old.journal.Name(), strings.TrimSuffix(old.journal.Name(), ".journal") + ".tri"}
	if old.sum != nil {
		paths = append(paths, old.sum.Name())
	}
	if err = old.close(); err != nil {
		return err
	}
	for _, p := range paths {
		if err = os.Remove(p); err != nil {
			return err
		}
	}
	return syncDir(s.dir)
}

func (s *Store) compactObjects() error {
	path := filepath.Join(s.collection, "collection.idx")
	f, e := os.CreateTemp(s.collection, ".collection-index-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	next := newObjectIndex(f)
	for key, source := range s.objects.sources {
		if key.Minor == s.layout.Definition.Resource.Minor && key.Generation != s.branches[Write].generation {
			continue
		}
		n := s.layout.Definition.Resource
		binding := fmt.Sprintf("%s/%s.v%d/%d", n.Service, n.Resource, n.Major, key.Minor)
		_ = binding
		directory, e := s.collectionDirectory(key)
		if e != nil {
			return e
		}
		// Stream bounded batches; retention never collects all references in RAM.
		var entries []collectionPosting
		flush := func() error {
			if len(entries) == 0 {
				return nil
			}
			// Distinct objects may have interleaved row numbers. The replacement is
			// built privately with the full source bound, then published once.
			e := next.mergeRetained(key, source.Width, source.Through, entries)
			entries = nil
			return e
		}
		e = s.objects.each(key, func(id [16]byte, rev uint64, a collectionAddress) error {
			if _, err := directory.resolveReference(a); err != nil {
				return nil
			}
			if len(entries) == 1024 {
				if e := flush(); e != nil {
					return e
				}
			}
			entries = append(entries, collectionPosting{id, rev, a})
			return nil
		})
		if e != nil {
			return e
		}
		if e = flush(); e != nil {
			return e
		}
		if next.through[key] == 0 && source.Through > 0 {
			if e = next.append(key, source.Width, 0, source.Through, nil); e != nil {
				return e
			}
		}
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	if e = syncDir(s.collection); e != nil {
		return e
	}
	// All endpoint operations share the group lock. Existing public history
	// snapshots hold event files, not the collection index descriptor.
	if e = s.objects.file.Close(); e != nil {
		return e
	}
	published, e := os.OpenFile(path, os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	next.file = published
	s.objects.replaceWith(next)
	return nil
}
