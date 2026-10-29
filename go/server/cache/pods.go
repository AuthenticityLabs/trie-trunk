package cache

import "fmt"

// A query owns one bounded decoded PAC. Native replication bypasses this reader.
type podsReader struct {
	context    recordContext
	branch     *branchStore
	pac        *PAC
	block      uint32
	start, end uint64
	data       []byte
	reads      uint64
	point      bool // Object lookups read exact extents; sequential scans keep read-ahead.
}

func (r *podsReader) read(b *branchStore, number uint64, row RowLayout) ([]byte, error) {
	if number < b.baseRows || number >= b.tail() {
		return nil, fmt.Errorf("payload row outside index")
	}
	run := r.context.segment(b, number)
	if number < run.Start {
		return nil, fmt.Errorf("reference points to segment header")
	}
	block := run.PAC.Placement.Block
	if r.branch != b || r.pac == nil || r.block != block {
		start, end, err := b.podsRange(number, row)
		if err != nil {
			return nil, err
		}
		raw, err := readBytes(b.pods, start-b.podsRetired, end-b.podsRetired)
		if err != nil {
			return nil, err
		}
		p, err := decodeStoredPAC(b.layout, b.runs[block-b.blockBase], raw)
		if err != nil {
			return nil, err
		}
		r.branch, r.block, r.pac = b, block, p
		r.reads++
	}
	member := r.pac.Members[number-b.runs[block-b.blockBase].Start]
	if len(r.pac.Common) == 0 {
		return member, nil
	}
	return ResolvePAC(b.layout, r.pac.Common, member)
}
