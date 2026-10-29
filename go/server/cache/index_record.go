package cache

import (
	"fmt"
)

// IndexRowSize is identical on disk, on the wire and in the browser.
// Low epoch-millisecond word U32 LE | state leaf U16 LE. Record ordinal is implicit in the index position.
const IndexRowSize = 6
const IndexCapacity uint64 = 1 << 48

func indexTime(row []byte, base int64) int64 {
	ms := (base & ^int64(0xffffffff)) | int64(order.Uint32(row[:4]))
	if ms < base {
		ms += 1 << 32
	}
	return ms
}
func packIndexID(row []byte, id [16]byte, base int64) error {
	ms := eventTime(id[:])
	if ms < base || ms >= base+TimeHourMS || id[6]>>4 != 7 || id[8]>>6 != 2 {
		return fmt.Errorf("UUID outside hourly segment")
	}
	order.PutUint32(row[:4], uint32(ms))
	return nil
}

// Time-only ordering keys are not event identities. Source and row break ties.
func timeKey(ms int64) []byte {
	raw := make([]byte, 16)
	for i := 5; i >= 0; i-- {
		raw[i] = byte(ms)
		ms >>= 8
	}
	return raw
}

// Sealed references are source-bound logical record numbers, preserved by retention.
func (b *branchStore) searchRef(row uint64) uint64 { return b.eventReference(row).packed() }
func (b *branchStore) searchRow(ref uint64) (uint64, error) {
	return b.time.resolveReference(unpackCollection(ref))
}

// Each segment starts with one index-width slot. Event references count this slot,
// but never point to it. The file header is outside the logical slot address space.
const IndexHeaderSlots = 1

func indexHeader(base int64, counts ...uint64) []byte {
	raw := make([]byte, IndexRowSize)
	order.PutUint32(raw, uint32(uint64(base)>>32))
	return raw
}
func validateIndexHeader(raw []byte, run Run) error {
	if len(raw) != IndexRowSize || order.Uint32(raw) != uint32(uint64(run.PAC.Placement.From)>>32) || raw[4] != 0 || raw[5] != 0 {
		return fmt.Errorf("invalid index hour prefix")
	}
	return nil
}

func (b *branchStore) nextRecord(row uint64) uint64 {
	i := b.runIndex(row)
	if i == len(b.runs) {
		return b.tail()
	}
	return max(row, b.runs[i].Start)
}

// A query-local cursor keeps the interpretation context while visiting records
// from one segment. It never mutates the shared branch snapshot.
type recordContext struct {
	branch *branchStore
	run    *Run
	reader *podsReader
}

func (c *recordContext) segment(b *branchStore, row uint64) *Run {
	if c.branch != b || c.run == nil || row < c.run.Start || row >= c.run.End {
		c.branch = b
		c.run = &b.runs[b.runIndex(row)]
	}
	return c.run
}
func (c *recordContext) identity(b *branchStore, row uint64) ([]byte, error) {
	run := c.segment(b, row)
	if row < run.Start || row-run.Start >= maxHourRecords {
		return nil, fmt.Errorf("record outside hour")
	}
	id, err := seedKey(b.rowTime(row), uint32(row-run.Start))
	return id[:], err
}
func (l *Layout) seedIdentity(raw []byte, ms int64) ([]byte, error) {
	var f Frame
	if err := l.Scan(raw, &f); err != nil {
		return nil, err
	}
	if f.Values[l.Record].State != Direct || len(f.Values[l.Record].Bytes) != 3 {
		return nil, fmt.Errorf("missing fixed U24 event record")
	}
	id, err := seedKey(ms, uint32(unsigned(f.Values[l.Record].Bytes)))
	if err != nil {
		return nil, err
	}
	return id[:], nil
}

func (c *recordContext) resolve(b *branchStore, ref uint64) (uint64, error) { return b.searchRow(ref) }
