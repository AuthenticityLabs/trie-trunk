package cache

import (
	"bytes"
	"fmt"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
)

type frameAssembler struct {
	layout   *Layout
	trie     *sharedTrie
	identity Frame
}

// Assemble copies encoded primitive spans and the entire existing offset/value
// section unchanged. Only the mask, indexed UUID and referenced direct fields
// are added. No per-event Value array, scalar decoding or Encode call occurs.
// Input is an ingest-validated population protected by committed CRCs. This is
// standalone output; native Collect sends index/population bytes unchanged.
func (a *frameAssembler) assemble(row, pop, dst []byte) ([]byte, error) {
	if len(row) != 16 {
		return nil, fmt.Errorf("short row")
	}
	l := a.layout
	if l.IsCompressed(pop) {
		f := &a.identity
		// Committed bytes were validated at ingress. Check the clear identity only.
		head, n, _, stored, err := l.CompressionLengths(pop)
		if err != nil {
			return nil, err
		}
		if len(pop) != head+n+stored || len(row) != 16 {
			return nil, fmt.Errorf("invalid compressed population")
		}
		if err = l.ScanPlain(pop[head:head+n], f); err != nil {
			return nil, err
		}
		if !bytes.Equal(f.Values[l.Event].Bytes, row[:16]) {
			return nil, fmt.Errorf("compressed identity differs from index")
		}
		return append(dst[:0], pop...), nil
	}
	if l.Shared >= 0 {
		f := &a.identity
		if err := l.Scan(pop, f); err != nil {
			return nil, err
		}
		if err := expandShared(l, a.trie, f.Values); err != nil {
			return nil, err
		}
		if f.Values[l.Event].State != Absent {
			return nil, fmt.Errorf("population shadows event_id")
		}
		f.Values[l.Event] = Value{Direct, row[:16]}
		return l.Encode(dst, f.Values)
	}
	if len(row) != 16 {
		return nil, fmt.Errorf("short row")
	}
	return l.RestoreDirect(dst, pop, []pil.DirectField{{Index: l.Event, Bytes: row[:16]}})
}

func (s *Store) assemble(row, pop, dst []byte) ([]byte, error) {
	a := frameAssembler{layout: s.layout, trie: &s.trie}
	return a.assemble(row, pop, dst)
}
