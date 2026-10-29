package cache

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"time"
)

// Every event row has the same width, independently of object identity types.
// Object/revision values and their presence states remain in the PIL payload.
type RowLayout struct{ Width, End int }

func (l *Layout) RowLayout() RowLayout { return RowLayout{Width: IndexRowSize, End: 0} }

// The fixed row selects a directory entry without decoding the PAC. The returned
// extent is the complete encoded block, not an address inside compressed bytes.
func (b *branchStore) podsRange(number uint64, r RowLayout) (uint64, uint64, error) {
	if number < b.baseRows || number >= b.tail() {
		return 0, 0, fmt.Errorf("payload row outside committed index")
	}
	i := b.runIndex(number)
	if i == len(b.runs) || number < b.runs[i].Start {
		return 0, 0, fmt.Errorf("row outside payload segment")
	}
	run := b.runs[i]
	leaf, ok := b.time.leaf(run.PAC.Placement.From)
	if !ok { // The commit journal is authoritative while a new hour is being sealed.
		return run.PodsStart, run.PodsEnd, nil
	}
	return storedSegmentRange(b.pods, b.podsRetired, leaf.Pods)

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
	Start uint64  `pil:"0" json:"start"`
	End   *uint64 `pil:"1" json:"end"`
}
type Period struct {
	Year     int `pil:"0" json:"year"`
	Day      int `pil:"1" json:"day"`
	From, To int64
	Ranges   []RowRange
	Packages map[int][]*Period // local hour; repeated DST hours have distinct bounds
}
type Run struct {
	Start      uint64            `pil:"0"`
	End        uint64            `pil:"1"`
	PodsStart  uint64            `pil:"2"`
	PodsEnd    uint64            `pil:"3"`
	MinMS      int64             `pil:"4"`
	MaxMS      int64             `pil:"5"`
	IndexCRC   uint32            `pil:"6"`
	PodsCRC    uint32            `pil:"7"`
	TrieSize   int               `pil:"8"`
	References map[uint16]uint64 `pil:"10,omitempty"`
	PAC        *StoredPAC        `pil:"9"`
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
func (b *branchStore) rowLeaf(n uint64) uint16 { return order.Uint16(b.row(n)[4:6]) }
func (b *branchStore) rowTime(n uint64) int64 {
	return indexTime(b.row(n), b.runs[b.runIndex(n)].PAC.Placement.From)
}
func (b *branchStore) rowID(n uint64) ([]byte, error) { var c recordContext; return c.identity(b, n) }
func (b *branchStore) assemblyRow(n uint64) []byte    { return timeKey(b.rowTime(n)) }
func (b *branchStore) memberLength(n uint64) (uint32, error) {
	var r podsReader
	if _, err := r.read(b, n, b.layout.RowLayout()); err != nil {
		return 0, err
	}
	return uint32(len(r.pac.Members[n-r.context.segment(b, n).Start])), nil
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
	for start < end {
		mid := start + (end-start)/2
		if indexTime(b.row(mid), base) < ms {
			start = mid + 1
		} else {
			end = mid
		}
	}
	return start
}
