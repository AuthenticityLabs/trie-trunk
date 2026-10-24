package cache

import (
	"path/filepath"
	"time"
)

type TrieStatistics struct {
	From, To        time.Time
	Nodes, Capacity int
	Branches        map[string]BranchStatistics
}
type BranchStatistics struct {
	Events, Unshared uint64
	References       map[uint16]uint64
	Prefixes         map[uint16]uint64
	Cardinality      map[string]uint64
}

// These are trie-organization diagnostics, not event/business aggregations.
// Branches stay distinct, so a trace burst cannot silently set object policy.
func (s *Store) statistics() (TrieStatistics, error) {
	end := s.config.Now()
	from := end.Add(-30 * 24 * time.Hour)
	out := TrieStatistics{From: from, To: end, Nodes: len(s.trie.nodes), Capacity: 65536, Branches: map[string]BranchStatistics{}}
	for branch, b := range s.branches {
		v := BranchStatistics{References: map[uint16]uint64{}, Prefixes: map[uint16]uint64{}, Cardinality: map[string]uint64{}}
		fields := map[uint16]map[string]bool{}
		reader := s.assembledReader()
		for _, run := range b.runs {
			if run.MaxMS < from.UnixMilli() || run.MinMS >= end.UnixMilli() {
				continue
			}
			counts := run.References
			if run.MinMS < from.UnixMilli() || run.MaxMS >= end.UnixMilli() {
				counts = map[uint16]uint64{}
				lo, hi := b.lower(run.Start, run.End, from.UnixMilli()), b.lower(run.Start, run.End, end.UnixMilli())
				var f Frame
				for row := lo; row < hi; row++ {
					raw, err := reader(b, row, nil)
					if err != nil {
						return TrieStatistics{}, err
					}
					if err = s.layout.Scan(raw, &f); err != nil {
						return TrieStatistics{}, err
					}
					counts[s.trie.lookup(s.layout, f.Values)]++
				}
			}
			for ref, count := range counts {
				v.Events += count
				if ref == 0 {
					v.Unshared += count
				} else {
					v.References[ref] += count
				}
			}
		}
		for ref, count := range v.References {
			for node := ref; node != 0; node = s.trie.nodes[node].Parent {
				n := s.trie.nodes[node]
				v.Prefixes[node] += count
				if fields[n.Field] == nil {
					fields[n.Field] = map[string]bool{}
				}
				fields[n.Field][string(n.Bytes)] = true
			}
		}
		for field, values := range fields {
			v.Cardinality[s.layout.Definition.Fields[field].Name] = uint64(len(values))
		}
		out.Branches[branch.Name()] = v
	}
	return out, nil
}
func (s *Store) publishStatistics() error {
	stats, e := s.statistics()
	if e != nil {
		return e
	}
	raw, e := encoder.Marshal(stats)
	if e != nil {
		return e
	}
	return replace(filepath.Join(s.dir, "resource.stats"), raw)
}
func (s *Store) Statistics() (TrieStatistics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return TrieStatistics{}, e
	}
	return s.statistics()
}
