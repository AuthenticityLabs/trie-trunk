package cache

import (
	"fmt"
	"time"
)

const HotWindow = 24 * time.Hour

// ProvisionalRow marks a live UUID without a permanent disk reference.
const ProvisionalRow = ^uint64(0)

// Rebuild the resident window from durable files on restart. Ordinary time
// scans still use the sequential reader outside this window.
func (s *Store) warmHot(b *branchStore) error {
	now := s.config.Now()
	var reader podsReader
	a := frameAssembler{layout: s.layout, trie: &s.trie}
	for _, run := range b.runs {
		if run.MaxMS < now.Add(-HotWindow).UnixMilli() {
			continue
		}
		span := cursor{b.lower(run.Start, run.End, now.Add(-HotWindow).UnixMilli()), run.End}
		for row := span.at; row < span.end; row++ {
			idx := b.assemblyRow(row)
			raw, e := reader.read(b, row, s.row)
			if e != nil {
				return e
			}
			full, e := a.assemble(idx, raw, nil, b.rowLeaf(row))
			if e != nil {
				return e
			}
			var id [16]byte
			identity, err := s.layout.seedIdentity(raw, b.rowTime(row))
			if err != nil {
				return err
			}
			copy(id[:], identity)
			size := len(full)
			full, e = b.own(s, id, full, nil)
			if e != nil {
				return fmt.Errorf("restore resident row %d, %d bytes, memory %+v: %w", row, size, s.memory.stats(), e)
			}
			if e = s.rememberSealed(b, id, full, row); e != nil {
				return e
			}
		}
	}
	return nil
}
