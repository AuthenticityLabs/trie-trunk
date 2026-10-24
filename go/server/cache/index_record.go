package cache

import (
	"encoding/binary"
	"fmt"
)

// IndexRowSize is identical on disk, on the wire and in the browser.
// 96-bit hour-relative UUIDv7 | decoded member start U32 LE.
const IndexRowSize = 16

// Directory slot columns widen as needed. Keep byte coordinates exact in browser Numbers.
const IndexCapacity uint64 = 1 << 48

func indexDelta(row []byte) int64 {
	return int64(uint32(row[0])<<14 | uint32(row[1])<<6 | uint32(row[2])>>2)
}
func packIndexID(row []byte, id [16]byte, base int64) error {
	delta := eventTime(id[:]) - base
	if delta < 0 || delta >= 1<<22 || id[6]>>4 != 7 || id[8]>>6 != 2 {
		return fmt.Errorf("UUID outside hourly segment")
	}
	// 22 timestamp bits, 12 rand_a bits, 62 rand_b bits. Network bit order
	// preserves UUID ordering within the segment without discarding random bits.
	hi := uint64(delta)<<42 | uint64(id[6]&15)<<38 | uint64(id[7])<<30 |
		uint64(id[8]&63)<<24 | uint64(id[9])<<16 | uint64(id[10])<<8 | uint64(id[11])
	binary.BigEndian.PutUint64(row[:8], hi)
	copy(row[8:12], id[12:16])
	return nil
}
func unpackIndexID(row []byte, base int64) [16]byte {
	var id [16]byte
	ms := uint64(base + indexDelta(row))
	for i := 5; i >= 0; i-- {
		id[i] = byte(ms)
		ms >>= 8
	}
	hi := binary.BigEndian.Uint64(row[:8])
	id[6] = 0x70 | byte(hi>>38)&15
	id[7] = byte(hi >> 30)
	id[8] = 0x80 | byte(hi>>24)&63
	id[9] = byte(hi >> 16)
	id[10] = byte(hi >> 8)
	id[11] = byte(hi)
	copy(id[12:], row[8:12])
	return id
}

// Sealed references are source-bound logical record numbers, preserved by retention.
func (b *branchStore) searchRef(row uint64) uint64 { return b.eventReference(row).packed() }
func (b *branchStore) searchRow(ref uint64) (uint64, error) {
	return b.time.resolveReference(unpackCollection(ref))
}

// Each segment starts with one 16-byte slot. Event references count this slot,
// but never point to it. The file header is outside the logical slot address space.
const IndexHeaderSlots = 1

func indexHeader(base int64, counts ...uint64) []byte {
	raw := make([]byte, IndexRowSize)
	count := uint64(0)
	if len(counts) > 0 {
		count = counts[0]
	}
	w := uintWidth(count, 2)
	putUintWidth(raw[:w], count)
	raw[12] = byte(w)
	order.PutUint64(raw[4:12], uint64(base))
	// A literal count is widened independently of event coordinates.
	return raw
}
func validateIndexHeader(raw []byte, run Run) error {
	if len(raw) != IndexRowSize || raw[12] < 2 || raw[12] > 4 || getUintWidth(raw[:raw[12]]) != run.End-run.Start || int64(order.Uint64(raw[4:12])) != run.PAC.Placement.From {
		return fmt.Errorf("invalid index segment header")
	}
	for _, v := range raw[13:] {
		if v != 0 {
			return fmt.Errorf("nonzero index header reserve")
		}
	}
	for _, v := range raw[raw[12]:4] {
		if v != 0 {
			return fmt.Errorf("nonzero count padding")
		}
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
}

func (c *recordContext) segment(b *branchStore, row uint64) *Run {
	if c.branch != b || c.run == nil || row < c.run.Start || row >= c.run.End {
		c.branch = b
		c.run = &b.runs[b.runIndex(row)]
	}
	return c.run
}
func (c *recordContext) identity(b *branchStore, row uint64) []byte {
	id := unpackIndexID(b.row(row), c.segment(b, row).PAC.Placement.From)
	return id[:]
}

func (c *recordContext) resolve(b *branchStore, ref uint64) (uint64, error) { return b.searchRow(ref) }
