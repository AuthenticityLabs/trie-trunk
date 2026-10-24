package cache

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"time"
)

// Every event row has the same width, independently of object identity types.
// Object/revision values and their presence states remain in the PIL population.
type RowLayout struct{ Width, End int }

func (l *Layout) RowLayout() RowLayout { return RowLayout{Width: IndexRowSize, End: 12} }

// The fixed row selects a directory entry without decoding the PAC. The returned
// extent is the complete encoded block, not an address inside compressed bytes.
func (b *branchStore) populationRange(number uint64, r RowLayout) (uint64, uint64, error) {
	if number < b.baseRows || number >= b.tail() {
		return 0, 0, fmt.Errorf("population row outside committed index")
	}
	i := b.runIndex(number)
	if i == len(b.runs) || number < b.runs[i].Start {
		return 0, 0, fmt.Errorf("row outside population segment")
	}
	run := b.runs[i]
	leaf, ok := b.time.leaf(run.PAC.Placement.From)
	if !ok { // The commit journal is authoritative while a new hour is being sealed.
		return run.PopStart, run.PopEnd, nil
	}
	return storedSegmentRange(b.pop, b.popRetired, leaf.Pods)

}

// The readable PAC length eliminates a second physical address in the directory.
func storedSegmentRange(f *os.File, retired, start uint64) (uint64, uint64, error) {
	if start < retired+headerSize {
		return 0, 0, fmt.Errorf("retired PAC address")
	}
	var raw [4]byte
	if _, err := f.ReadAt(raw[:], int64(start-retired)); err != nil {
		return 0, 0, err
	}
	length := uint64(order.Uint32(raw[:]))
	if length < PACCompressedHeaderSize || start+length < start {
		return 0, 0, fmt.Errorf("invalid PAC stored length")
	}
	return start, start + length, nil
}
func eventTime(id []byte) int64 {
	var v uint64
	for _, b := range id[:6] {
		v = v<<8 | uint64(b)
	}
	return int64(v)
}
func eventID(v Value) ([16]byte, error) {
	var id [16]byte
	if v.State != Direct || len(v.Bytes) != 16 || v.Bytes[6]>>4 != 7 || v.Bytes[8]>>6 != 2 {
		return id, fmt.Errorf("event_id must be UUIDv7")
	}
	copy(id[:], v.Bytes)
	return id, nil
}

type RowRange struct {
	Start uint64  `cbor:"0,keyasint" json:"start"`
	End   *uint64 `cbor:"1,keyasint" json:"end"`
}
type Period struct {
	Year     int `cbor:"0,keyasint" json:"year"`
	Day      int `cbor:"1,keyasint" json:"day"`
	From, To int64
	Ranges   []RowRange
	Packages map[int][]*Period // local hour; repeated DST hours have distinct bounds
}
type Run struct {
	Start      uint64            `cbor:"0,keyasint"`
	End        uint64            `cbor:"1,keyasint"`
	PopStart   uint64            `cbor:"2,keyasint"`
	PopEnd     uint64            `cbor:"3,keyasint"`
	MinMS      int64             `cbor:"4,keyasint"`
	MaxMS      int64             `cbor:"5,keyasint"`
	IndexCRC   uint32            `cbor:"6,keyasint"`
	PopCRC     uint32            `cbor:"7,keyasint"`
	TrieSize   int               `cbor:"8,keyasint"`
	References map[uint16]uint64 `cbor:"10,keyasint,omitempty"`
	PAC        *StoredPAC        `cbor:"9,keyasint"`
}

func (r RowRange) end(tail uint64) uint64 {
	if r.End == nil {
		return tail
	}
	return *r.End
}
func lower(rows []byte, w int, start, end uint64, ms int64) uint64 {
	for start < end {
		mid := start + (end-start)/2
		if eventTime(rows[mid*uint64(w):]) < ms {
			start = mid + 1
		} else {
			end = mid
		}
	}
	return start
}
func equalID(row []byte, id [16]byte) bool { return bytes.Equal(row[:16], id[:]) }

func timeBound(t time.Time) int64 {
	ms := t.UnixMilli()
	if t.Nanosecond()%1_000_000 != 0 {
		ms++
	}
	return ms
}
func (b *branchStore) candidates(from, to time.Time, width int) []cursor {
	lo, hi := timeBound(from), timeBound(to)
	tail := b.tail()
	var spans []Span
	b.visitPeriods(from, to, func(period *Period) {
		for _, r := range period.Ranges {
			spans = append(spans, Span{uint64(r.Start), r.end(tail)})
		}
	})
	spans, _ = Normalize(spans)
	var out []cursor
	for _, span := range spans {
		first := sort.Search(len(b.runs), func(i int) bool { return b.runs[i].End > span.Start })
		for i := first; i < len(b.runs) && b.runs[i].Start < span.End; i++ {
			run := b.runs[i]
			if run.MaxMS < lo || run.MinMS >= hi {
				continue
			}
			start, end := run.Start, run.End
			if start < span.Start {
				start = span.Start
			}
			if end > span.End {
				end = span.End
			}
			left, right := start, end
			if b.rowTime(start) < lo {
				left = b.lower(start, end, lo)
			}
			if b.rowTime(end-1) >= hi {
				right = b.lower(left, end, hi)
			}
			if left < right {
				out = append(out, cursor{left, right})
			}
		}
	}
	return out
}

func (b *branchStore) tail() uint64 { return b.baseRows + uint64(len(b.rows)/IndexRowSize) }
func (b *branchStore) row(n uint64) []byte {
	at := (n - b.baseRows) * IndexRowSize
	return b.rows[at : at+IndexRowSize]
}
func (b *branchStore) runIndex(n uint64) int {
	return sort.Search(len(b.runs), func(i int) bool { return b.runs[i].End > n })
}
func (b *branchStore) rowTime(n uint64) int64 {
	return b.runs[b.runIndex(n)].PAC.Placement.From + indexDelta(b.row(n))
}
func (b *branchStore) rowID(n uint64) []byte {
	id := unpackIndexID(b.row(n), b.runs[b.runIndex(n)].PAC.Placement.From)
	return id[:]
}

// This transient assembly identity is a full UUID, not an on-disk index row.
// Only payload assembly needs a full UUID. Time searches compare the packed delta.
func (b *branchStore) assemblyRow(n uint64) []byte {
	out := make([]byte, 16)
	copy(out, b.rowID(n))
	// Shared references, when enabled, are carried in PIL.
	return out
}
func (b *branchStore) memberLength(n uint64) uint32 {
	run := b.runs[b.runIndex(n)]
	end := run.PAC.Placement.MemberBytes
	if n+1 < run.End {
		end = uint64(order.Uint32(b.row(n + 1)[12:]))
	}
	return uint32(end - uint64(order.Uint32(b.row(n)[12:])))
}
func (b *branchStore) lower(start, end uint64, ms int64) uint64 {
	if start == end {
		return start
	}
	// Most time searches already identify one segment. Intersections can merge
	// adjacent runs, so narrow those spans before comparing hour-relative deltas.
	i := b.runIndex(start)
	if end > b.runs[i].End {
		i += sort.Search(len(b.runs)-i, func(j int) bool { return b.runs[i+j].MaxMS >= ms })
		if i == len(b.runs) || b.runs[i].Start >= end {
			return end
		}
		start = max(start, b.runs[i].Start)
		end = min(end, b.runs[i].End)
	}
	base := b.runs[i].PAC.Placement.From
	delta := ms - base
	for start < end {
		mid := start + (end-start)/2
		if indexDelta(b.row(mid)) < delta {
			start = mid + 1
		} else {
			end = mid
		}
	}
	return start
}
