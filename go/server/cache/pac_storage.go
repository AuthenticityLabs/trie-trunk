package cache

import (
	"crypto/sha256"
	"fmt"
	"math"
	"time"
)

// StoredPAC is the block directory entry in the companion commit journal. MemberRanges
// addresses are encoded extents; placement addresses count decoded members only.
// PAC directories keep decoded member starts; the segment directory owns compressed extents.
type StoredPAC struct {
	Placement PACPlacement `pil:"0"`
	Decoded   uint64       `pil:"1"`
	Gzip      bool         `pil:"2"`
}

const NativePageSize = 1 << 20

func (s *Store) packBlock(b *branchStore, first uint64, batch []pending, frames [][]byte, rows []byte) ([]byte, *StoredPAC, error) {
	if uint64(b.blockBase)+uint64(len(b.runs)) > math.MaxUint32 {
		return nil, nil, fmt.Errorf("payload block number exhausted")
	}
	p, err := factorPAC(s.layout, frames, nil, true)
	if err != nil {
		return nil, nil, err
	}
	start := b.memberEnd
	if len(b.runs) > 0 {
		last := b.runs[len(b.runs)-1].PAC.Placement
		start = last.MemberStart + last.MemberBytes
	}
	from, to := semanticBounds(time.UnixMilli(eventTime(batch[0].id[:])), 1, s.location)
	// The shared trie is append-only. Bind the exact prefix needed by these rows.
	trie, err := encoder.Marshal(s.trie.nodes)
	if err != nil {
		return nil, nil, err
	}
	place, err := PlacePAC(p, PACPlacement{
		Source: b.generation, Trie: sha256.Sum256(trie), Block: b.blockBase + uint32(len(b.runs)),
		PageSize: NativePageSize, From: from.UnixMilli(), To: to.UnixMilli(),
		FirstRow: first, MemberStart: start + 4 + uint64(len(p.Members))*4,
	})
	if err != nil {
		return nil, nil, err
	}
	if eventTime(batch[len(batch)-1].id[:]) >= place.To {
		return nil, nil, fmt.Errorf("PAC crosses an hour boundary")
	}
	e, err := place.Extension()
	if err != nil {
		return nil, nil, err
	}
	p.Extensions = []PACExtension{{Type: PACSource, Data: []byte((&HTTP{Store: s}).tap(b.branch))}, e}
	p.AddressBytes = 4
	p.Bytes, err = p.Marshal()
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(p.Directory))+place.MemberBytes+4 > math.MaxUint32 {
		return nil, nil, fmt.Errorf("decoded segment exceeds U32")
	}

	// Write packs remain directly addressable for object history. Replication
	// still transfers whole packs, exactly as it does for the other branches.
	block := &StoredPAC{Placement: place, Decoded: uint64(len(p.Bytes)), Gzip: b.branch != Write}
	if block.Gzip {
		sealed, err := compressPAC(p.Bytes, s.policyFor(b.branch).PAC.level())
		if err == nil {
			block.Gzip = order.Uint32(sealed[4:8])&2 != 0
		}
		return sealed, block, err
	}
	return p.Bytes, block, nil
}

func validateBlock(run Run, rows []byte, layout RowLayout, source [16]byte) error {
	if run.PAC == nil {
		return fmt.Errorf("missing PAC directory")
	}
	p := run.PAC.Placement
	if err := p.validate(); err != nil {
		return err
	}
	if p.Source != source || p.FirstRow != run.Start || p.Rows != run.End-run.Start ||
		len(rows) != int(p.Rows)*layout.Width || run.PAC.Decoded > PACMaximum ||
		run.PAC.Decoded < PACHeaderSize || run.MinMS < p.From || run.MaxMS >= p.To {
		return fmt.Errorf("PAC directory/commit mismatch")
	}
	var previous int64 = -1
	for i := 0; i < len(rows); i += layout.Width {
		ms := indexTime(rows[i:i+layout.Width], p.From)
		if ms < run.MinMS || ms > run.MaxMS || ms < previous {
			return fmt.Errorf("invalid PAC row time")
		}
		previous = ms
	}

	return nil
}

// Only payload inspection uses this decoder. Replication reads the encoded
// extent directly and never calls this function. Keep one decoded block per
// query, rather than decompressing separately for each matching event.
func decodeStoredPAC(l *Layout, run Run, raw []byte) (*PAC, error) {
	if run.PAC == nil {
		return nil, fmt.Errorf("missing PAC directory")
	}
	var err error
	raw, err = expandPAC(raw)
	if err != nil {
		return nil, err
	}

	if uint64(len(raw)) != run.PAC.Decoded {
		return nil, fmt.Errorf("decoded PAC length mismatch")
	}
	p, err := ParsePAC(l, raw, map[uint32]bool{PACNative: true})
	if err != nil {
		return nil, err
	}
	if uint64(len(p.Members)) != run.End-run.Start {
		return nil, fmt.Errorf("PAC member count mismatch")
	}
	return p, nil
}
