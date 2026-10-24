package cache

import "time"

const HotWindow = 24 * time.Hour

// ProvisionalRow marks a live UUID without a permanent disk reference.
const ProvisionalRow = ^uint64(0)

// Rebuild the resident window from durable files on restart. Ordinary time
// scans still use the sequential reader outside this window.
func (s *Store) warmHot(b *branchStore) error {
	now := s.config.Now()
	var reader populationReader
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
			full, e := a.assemble(idx, raw, nil)
			if e != nil {
				return e
			}
			var id [16]byte
			copy(id[:], idx[:16])
			full, e = b.own(s, id, full, nil)
			if e != nil {
				return e
			}
			if e = s.rememberSealed(b, id, full, row); e != nil {
				return e
			}
		}
	}
	return nil
}
