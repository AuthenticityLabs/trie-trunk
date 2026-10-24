package cache

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// The in-memory tuple uses independent U32 components. Serialized collection
// pages choose their own fixed widths, initially U16/U16, without changing values.
// Record positions include the header slot (0); event records start at 1.
func (b *branchStore) hourOrigin() int64 {
	if b.timeAnchor != nil {
		return b.timeAnchor.Origin
	}
	if len(b.runs) > 0 {
		origin := b.runs[0].PAC.Placement.From
		if len(b.time.raw) >= TimeTrieHeader {
			origin = min(origin, b.time.origin())
		}
		return origin
	}
	if len(b.time.raw) >= TimeTrieHeader {
		return b.time.origin()
	}
	return 0
}
func (b *branchStore) eventReference(row uint64) collectionAddress {
	run := b.runs[b.runIndex(row)]
	return collectionAddress{uint32((run.PAC.Placement.From - b.hourOrigin()) / TimeHourMS), uint32(row - run.Start + 1)}
}
func (t *timeDirectory) resolveReference(a collectionAddress) (uint64, error) {
	if len(t.raw) < TimeTrieHeader || a.Record == 0 {
		return 0, fmt.Errorf("invalid hourly event reference")
	}
	ms := t.origin() + int64(a.Hour)*TimeHourMS
	l, ok := t.leaf(ms)
	if !ok || l.Segments != 1 || uint64(a.Record) >= l.End-l.Start {
		return 0, fmt.Errorf("retired or unknown hourly event reference")
	}
	return l.Start + uint64(a.Record), nil
}
func (s *Store) collectionDirectory(k objectBinding) (timeDirectory, error) {
	if k.Minor == s.layout.Definition.Resource.Minor {
		if b := s.branches[Write]; b != nil && b.generation == k.Generation {
			return b.time, nil
		}
	}
	n := s.layout.Definition.Resource
	binding := fmt.Sprintf("%s/%s.v%d/%d", n.Service, n.Resource, n.Major, k.Minor)
	prefix := Write.Prefix()
	if retired := s.catalog.RetiredRows[binding][Write]; retired > 0 {
		prefix = fmt.Sprintf("%s.r%d", prefix, retired)
	} else if gen, ok := s.catalog.Generations[binding][Write]; ok {
		prefix += ".g" + hex.EncodeToString(gen[:])
	}
	raw, err := os.ReadFile(filepath.Join(s.root, binding, prefix+".tri"))
	if err != nil {
		return timeDirectory{}, err
	}
	return parseTimeDirectory(raw, k.Generation)
}
