package cache

import (
	"fmt"
)

type frameAssembler struct {
	layout   *Layout
	trie     *sharedTrie
	identity Frame
}

// Reconstruct standalone event identity from the indexed timestamp and stored
// record number, then resolve index-bound shared fields. Native replication transfers the sealed
// index and PAC unchanged and does not invoke this materialization path.
func (a *frameAssembler) assemble(row, pods, dst []byte, leaf uint16) ([]byte, error) {
	if len(row) != 16 {
		return nil, fmt.Errorf("short row")
	}
	l := a.layout
	f := &a.identity
	if err := l.Scan(pods, f); err != nil {
		return nil, err
	}
	if f.Values[l.Event].State != Absent || f.Values[l.Time].State != Absent {
		return nil, fmt.Errorf("payload shadows indexed identity")
	}
	if f.Values[l.Record].State != Direct || len(f.Values[l.Record].Bytes) != 3 {
		return nil, fmt.Errorf("missing fixed U24 event record")
	}
	id, err := seedKey(eventTime(row), uint32(unsigned(f.Values[l.Record].Bytes)))
	if err != nil {
		return nil, err
	}
	if err = a.trie.expand(leaf, f.Values); err != nil {
		return nil, err
	}
	f.Values[l.Leaf] = Uint(uint64(leaf), 2)
	f.Values[l.Event] = Value{Direct, id[:]}
	return l.Encode(dst, f.Values)
}

func (s *Store) assemble(row, pods, dst []byte, leaf uint16) ([]byte, error) {
	a := frameAssembler{layout: s.layout, trie: &s.trie}
	return a.assemble(row, pods, dst, leaf)
}
