package cache

// A positional UTC YYYY/DDD/HH directory. Blank and complete-empty are distinct.
// All segment addresses live at the hour leaf; a direct reference indexes that
// leaf arithmetically, without a time search or a scan of earlier hours.
import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	TimeTrieHeader          = 64
	TimeTrieDayBytes        = 24 * 16
	TimeTrieYearBytes       = 366 * TimeTrieDayBytes
	TimeHourMS        int64 = 3600000
	TimeComplete            = 2
)

var timeTrieMagic = []byte("TTTIME00")

type TimeAnchor struct {
	First  int64
	Origin int64
}
type timeDirectory struct{ raw []byte }
type TimeLeaf struct {
	From, To   int64
	Start, End uint64
	Segments   uint32
	State      byte
	Pods, Sods uint64
}

func utcHour(ms int64) int64           { return time.UnixMilli(ms).UTC().Truncate(time.Hour).UnixMilli() }
func (t *timeDirectory) first() int64  { return int64(order.Uint64(t.raw[8:16])) }
func (t *timeDirectory) origin() int64 { return int64(order.Uint64(t.raw[16:24])) }
func (t *timeDirectory) sealed() int64 { return int64(order.Uint64(t.raw[32:40])) }
func (t *timeDirectory) baseYear() int { return time.UnixMilli(t.first()).UTC().Year() }
func (t *timeDirectory) leafBytes() int {
	return int(t.raw[40]) + int(t.raw[41]) + int(t.raw[42]) + int(t.raw[43])
}
func (t *timeDirectory) yearBytes() int { return 366 * 24 * t.leafBytes() }
func (t *timeDirectory) positions(at time.Time) (year, day, hour int, ok bool) {
	if len(t.raw) < TimeTrieHeader {
		return
	}
	at = at.UTC()
	y := at.Year() - t.baseYear()
	yb := t.yearBytes()
	if y < 0 || y >= (len(t.raw)-TimeTrieHeader)/yb {
		return
	}
	year = TimeTrieHeader + y*yb
	d := at.YearDay() - 1
	day = year + d*24*t.leafBytes()
	hour = day + at.Hour()*t.leafBytes()
	ok = true
	return
}
func (t *timeDirectory) leaf(ms int64) (TimeLeaf, bool) {
	ms = utcHour(ms)
	if len(t.raw) < TimeTrieHeader || ms < t.first() || ms >= t.sealed() {
		return TimeLeaf{}, false
	}
	_, _, h, ok := t.positions(time.UnixMilli(ms))
	if !ok {
		return TimeLeaf{}, false
	}
	at := h
	read := func(w byte) uint64 { v := getUintWidth(t.raw[at : at+int(w)]); at += int(w); return v }
	start, pods, sods, count := read(t.raw[40]), read(t.raw[41]), read(t.raw[42]), read(t.raw[43])
	end := start
	segments := uint32(0)
	if count > 0 {
		end += IndexHeaderSlots + count
		segments = 1
	}
	// Every hour within the published bounds is sealed, including empty hours.
	// Client file-replication masks are never stored in this server directory.
	return TimeLeaf{From: ms, To: ms + TimeHourMS, Start: start, End: end, Segments: segments, State: TimeComplete, Pods: pods, Sods: sods}, true
}
func (t *timeDirectory) boundary(ms int64) (uint64, byte, bool) {
	if len(t.raw) < TimeTrieHeader {
		return 0, 0, false
	}
	if ms == t.sealed() {
		return order.Uint64(t.raw[24:32]), TimeComplete, true
	}
	l, ok := t.leaf(ms)
	return l.Start, l.State, ok
}
func buildTimeDirectory(runs []Run, first, sealed int64, tail uint64, source [16]byte) (timeDirectory, error) {
	return buildHourDirectory(runs, first, sealed, tail, source, first, [6]byte{4, 5, 5, 2, 2, 2})
}
func buildHourDirectory(runs []Run, first, sealed int64, tail uint64, source [16]byte, origin int64, widths [6]byte) (timeDirectory, error) {
	first, sealed, origin = utcHour(first), utcHour(sealed), utcHour(origin)
	if first > sealed || origin > first {
		return timeDirectory{}, fmt.Errorf("invalid UTC directory bounds")
	}
	if widths == ([6]byte{}) {
		widths = [6]byte{4, 5, 5, 2, 2, 2}
	}
	grow := func(i int, n uint64) { widths[i] = byte(uintWidth(n, int(widths[i]))) }
	grow(0, tail)
	if sealed > origin {
		grow(4, uint64((sealed-1-origin)/TimeHourMS))
	}
	for _, r := range runs {
		if r.PAC == nil || r.End <= r.Start {
			return timeDirectory{}, fmt.Errorf("invalid hourly segment")
		}
		grow(1, r.PodsStart)
		grow(2, r.PAC.Placement.MemberStart)
		grow(3, r.End-r.Start)
		grow(5, r.End-r.Start)
	}
	a := time.UnixMilli(first).UTC()
	last := a.Year()
	if sealed > first {
		last = time.UnixMilli(sealed - 1).UTC().Year()
	}
	if a.Year() < 1 || last > 9999 {
		return timeDirectory{}, fmt.Errorf("unsupported calendar year")
	}
	t := timeDirectory{raw: make([]byte, TimeTrieHeader)}
	copy(t.raw, timeTrieMagic)
	copy(t.raw[40:46], widths[:])
	copy(t.raw[48:64], source[:])
	order.PutUint64(t.raw[8:16], uint64(first))
	order.PutUint64(t.raw[16:24], uint64(origin))
	order.PutUint64(t.raw[24:32], tail)
	order.PutUint64(t.raw[32:40], uint64(sealed))
	t.raw = append(t.raw, make([]byte, (last-a.Year()+1)*t.yearBytes())...)
	row := tail
	if len(runs) > 0 {
		row = runs[0].Start - IndexHeaderSlots
	}
	i := 0
	for ms := first; ms < sealed; ms += TimeHourMS {
		_, _, h, _ := t.positions(time.UnixMilli(ms))
		pods, sods, count := uint64(0), uint64(0), uint64(0)
		start := row
		if i < len(runs) && runs[i].PAC.Placement.From == ms {
			r := runs[i]
			if r.Start != row+IndexHeaderSlots || r.PAC.Placement.To != ms+TimeHourMS {
				return timeDirectory{}, fmt.Errorf("noncontiguous hourly segment")
			}
			pods, sods, count = r.PodsStart, r.PAC.Placement.MemberStart-4-4*(r.End-r.Start), r.End-r.Start
			row = r.End
			i++
			if i < len(runs) && runs[i].PAC.Placement.From == ms {
				return timeDirectory{}, fmt.Errorf("multiple segments in sealed hour")
			}
		}
		at := h
		for k, v := range []uint64{start, pods, sods, count} {
			w := int(widths[k])
			putUintWidth(t.raw[at:at+w], v)
			at += w
		}
	}
	if i != len(runs) || row != tail {
		return timeDirectory{}, fmt.Errorf("segments outside sealed directory")
	}
	return t, nil
}
func parseTimeDirectory(raw []byte, source [16]byte) (timeDirectory, error) {
	t := timeDirectory{raw: raw}
	if len(raw) < TimeTrieHeader || !bytes.Equal(raw[:8], timeTrieMagic) || !bytes.Equal(raw[48:64], source[:]) {
		return timeDirectory{}, fmt.Errorf("invalid time trie")
	}
	for i, w := range raw[40:46] {
		minimum := byte(2)
		if i == 0 {
			minimum = 4
		}
		if i == 1 || i == 2 {
			minimum = 5
		}
		if w < minimum || w > 8 {
			return timeDirectory{}, fmt.Errorf("invalid directory width")
		}
	}
	if raw[46] != 0 || raw[47] != 0 || t.origin() > t.first() || t.first() > t.sealed() || t.first() != utcHour(t.first()) || t.origin() != utcHour(t.origin()) || t.sealed() != utcHour(t.sealed()) {
		return timeDirectory{}, fmt.Errorf("invalid directory bounds")
	}
	last := t.baseYear()
	if t.sealed() > t.first() {
		last = time.UnixMilli(t.sealed() - 1).UTC().Year()
	}
	if t.baseYear() < 1 || last > 9999 || len(raw) != TimeTrieHeader+(last-t.baseYear()+1)*t.yearBytes() {
		return timeDirectory{}, fmt.Errorf("invalid directory extent")
	}
	return t, nil
}
func (s *Store) publishTime(b *branchStore) error {
	n := 0
	for n < len(b.runs) && b.runs[n].PAC.Placement.To <= b.finalizedBefore {
		n++
	}
	runs := b.runs[:n]
	tail := b.baseRows
	if n > 0 {
		tail = runs[n-1].End
	}
	sealed := utcHour(b.finalizedBefore)
	if b.finalizedBefore == 0 {
		sealed = utcHour(s.config.Now().UnixMilli())
	}
	first := sealed
	if b.timeAnchor != nil {
		first = min(first, b.timeAnchor.First)
	}
	if len(runs) > 0 {
		first = min(first, runs[0].PAC.Placement.From)
	}
	path := filepath.Join(s.dir, s.prefix(b.branch)+".tri")
	old, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	origin := first
	widths := [6]byte{4, 5, 5, 2, 2, 2}
	if prior, err := parseTimeDirectory(old, b.generation); err == nil {
		first = min(first, prior.first())
		origin = min(origin, prior.origin())
		copy(widths[:], prior.raw[40:46])
	}
	if b.timeAnchor != nil {
		origin = b.timeAnchor.Origin
	}
	t, e := buildHourDirectory(runs, first, sealed, tail, b.generation, origin, widths)
	if e != nil {
		return e
	}
	if !bytes.Equal(old, t.raw) {
		if e = replace(path, t.raw); e != nil {
			return e
		}
	}
	b.time = t
	b.timeDirty = false
	return nil
}

// Physical directory columns may widen without changing the source-bound hour IDs.
func (t *timeDirectory) preserveBases(old *timeDirectory) error {
	if len(old.raw) >= TimeTrieHeader {
		order.PutUint64(t.raw[16:24], uint64(old.origin()))
	}
	return nil
}

// A replication patch carries server leaves only. Header bounds distinguish
// sealed empty hours from unavailable hours; clients add their own receipt byte.
func (t *timeDirectory) window(from, to int64, retired bool) ([]byte, error) {
	if from != utcHour(from) || to != utcHour(to) || from >= to || to > t.sealed() || retired && from < t.first() {
		return nil, fmt.Errorf("invalid time window")
	}
	raw := append([]byte(nil), t.raw[:TimeTrieHeader]...)
	raw = order.AppendUint64(raw, uint64(from))
	raw = order.AppendUint64(raw, uint64(to))
	for ms := from; ms < to; ms += TimeHourMS {
		if ms < t.first() {
			raw = append(raw, make([]byte, t.leafBytes())...)
			continue
		}
		l, ok := t.leaf(ms)
		if !ok || l.State != TimeComplete {
			return nil, fmt.Errorf("incomplete time hour")
		}
		_, _, h, _ := t.positions(time.UnixMilli(ms))
		raw = append(raw, t.raw[h:h+t.leafBytes()]...)
	}
	return raw, nil
}
func (b *branchStore) visitPeriods(from, to time.Time, fn func(*Period)) {
	if len(b.time.raw) < TimeTrieHeader {
		return
	}
	start := max(utcHour(timeBound(from)), b.time.first())
	end := min(timeBound(to), b.time.sealed())
	for ms := start; ms < end; ms += TimeHourMS {
		l, ok := b.time.leaf(ms)
		if !ok || l.Segments == 0 {
			continue
		}
		e := l.End
		fn(&Period{From: ms, To: ms + TimeHourMS, Ranges: []RowRange{{Start: l.Start + IndexHeaderSlots, End: &e}}})
	}
}

// podsBoundary resolves a half-open time boundary without reading event indexes.
// Empty/blank siblings have no usable address. Walk backward to a populated leaf
// and read only its length prefix. Coverage validation remains window's job;
// finding an address never changes blank hours into complete-empty hours.
func (t *timeDirectory) podsBoundary(file *os.File, retired uint64, ms int64) (uint64, error) {
	if len(t.raw) < TimeTrieHeader || ms != utcHour(ms) || ms < t.first() || ms > t.sealed() {
		return 0, fmt.Errorf("invalid PAC time boundary")
	}
	if l, ok := t.leaf(ms); ok && l.Segments > 0 {
		return l.Pods, nil
	}
	for at := ms - TimeHourMS; at >= t.first(); at -= TimeHourMS {
		if l, ok := t.leaf(at); ok && l.Segments > 0 {
			_, end, err := storedSegmentRange(file, retired, l.Pods)
			return end, err
		}
	}
	return retired + headerSize, nil
}
