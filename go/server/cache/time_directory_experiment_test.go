package cache

// Experimental only: immutable chronological index, no storage-format changes.
import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"math/rand"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

type tdSpan struct{ start, end uint64 }
type tdDay struct {
	start, end int64
	hours      []uint64
}
type tdSemantic struct {
	years map[int][]tdDay
	bytes int
}

func tdCalendar(rows []byte, from, to time.Time) tdSemantic {
	s := tdSemantic{years: map[int][]tdDay{}}
	for day := from; day.Before(to); day = day.AddDate(0, 0, 1) {
		y, d := day.Year(), day.YearDay()
		if s.years[y] == nil {
			s.years[y] = make([]tdDay, 367)
			s.bytes += 367 * int(unsafe.Sizeof(tdDay{}))
		}
		next := day.AddDate(0, 0, 1)
		p := tdDay{start: day.UnixMilli(), end: next.UnixMilli()}
		for hour := day; !hour.After(next); hour = hour.Add(time.Hour) {
			p.hours = append(p.hours, lower(rows, 26, 0, uint64(len(rows)/26), hour.UnixMilli()))
		}
		s.bytes += cap(p.hours) * 8
		s.years[y][d] = p
	}
	return s
}
func (s tdSemantic) edge(year, day, hour int) uint64 { return s.years[year][day].hours[hour] }

// Prefix compression skips unary levels. Only internal nodes carry a bitmap.
type tdNode struct {
	prefix, start, end uint64
	branch             uint32
	depth              uint8
}
type tdBranch struct {
	mask  [4]uint64
	first uint32
}
type tdRadix struct {
	nodes    []tdNode
	branches []tdBranch
	rows     []byte
	depth    int
}

func tdStamp(rows []byte, i uint64) uint64 { return binary.BigEndian.Uint64(rows[i*26:]) >> 16 }

// Fixed 26-byte rows have at least eight readable bytes at every UUID.
func tdLower(rows []byte, lo, hi uint64, ms int64) uint64 {
	for lo < hi {
		mid := lo + (hi-lo)/2
		if tdStamp(rows, mid) < uint64(ms) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
func (s tdSemantic) epochEdge(ms int64, loc *time.Location) uint64 {
	t := time.UnixMilli(ms).In(loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	year, d := day.Year(), day.YearDay()
	h := int(t.Sub(day) / time.Hour)
	if s.years[year] == nil || len(s.years[year][d].hours) == 0 {
		day = day.AddDate(0, 0, -1)
		year, d = day.Year(), day.YearDay()
		h = int(t.Sub(day) / time.Hour)
	}
	return s.edge(year, d, h)
}
func tdBuild(rows []byte, depth int) tdRadix {
	r := tdRadix{rows: rows, depth: depth, nodes: make([]tdNode, 1)}
	var build func(uint32, uint64, uint64, int)
	build = func(at uint32, lo, hi uint64, d int) {
		first, last := tdStamp(rows, lo), tdStamp(rows, hi-1)
		for d < depth && first>>uint(8*(5-d)) == last>>uint(8*(5-d)) {
			d++
		}
		r.nodes[at] = tdNode{prefix: first, start: lo, end: hi, depth: uint8(d)}
		if d == depth {
			return
		}
		shift := uint(8 * (5 - d))
		starts := []uint64{lo}
		var keys []byte
		prev := byte(first >> shift)
		keys = append(keys, prev)
		for i := lo + 1; i < hi; i++ {
			k := byte(tdStamp(rows, i) >> shift)
			if k != prev {
				starts = append(starts, i)
				keys = append(keys, k)
				prev = k
			}
		}
		starts = append(starts, hi)
		branch := tdBranch{first: uint32(len(r.nodes))}
		for _, k := range keys {
			branch.mask[k/64] |= 1 << uint(k%64)
		}
		r.nodes[at].branch = uint32(len(r.branches))
		r.branches = append(r.branches, branch)
		r.nodes = append(r.nodes, make([]tdNode, len(keys))...)
		for j := range keys {
			build(branch.first+uint32(j), starts[j], starts[j+1], d+1)
		}
	}
	build(0, 0, uint64(len(rows)/26), 0)
	return r
}
func (r *tdRadix) edge(ms int64) uint64 {
	key := uint64(ms)
	at := uint32(0)
	for {
		n := r.nodes[at]
		shift := uint(8 * (6 - int(n.depth)))
		if key>>shift < n.prefix>>shift {
			return n.start
		}
		if key>>shift > n.prefix>>shift {
			return n.end
		}
		if int(n.depth) == r.depth {
			if r.depth == 6 {
				return n.start
			}
			return tdLower(r.rows, n.start, n.end, ms)
		}
		b := r.branches[n.branch]
		k := byte(key >> uint(8*(5-int(n.depth))))
		word, bit := int(k/64), uint(k%64)
		rank := 0
		for i := 0; i < word; i++ {
			rank += bits.OnesCount64(b.mask[i])
		}
		rank += bits.OnesCount64(b.mask[word] & ((uint64(1) << bit) - 1))
		if b.mask[word]&(uint64(1)<<bit) != 0 {
			at = b.first + uint32(rank)
			continue
		}
		remaining := b.mask[word] & (^uint64(0) << bit)
		for i := word + 1; i < 4; i++ {
			remaining |= b.mask[i]
		}
		if remaining == 0 {
			return n.end
		}
		return r.nodes[b.first+uint32(rank)].start
	}
}

type tdQuery struct {
	from, to               int64
	fy, fd, fh, ty, td, th int
	expected               tdSpan
}
type tdFixture struct {
	rows     []byte
	semantic tdSemantic
	radices  []tdRadix
	queries  map[string][]tdQuery
}

func tdData(n, days int, bursty bool) tdFixture {
	loc, _ := time.LoadLocation("America/New_York")
	from := time.Date(2025, 10, 1, 0, 0, 0, 0, loc)
	if days == 30 {
		from = time.Date(2026, 10, 20, 0, 0, 0, 0, loc)
	}
	to := from.AddDate(0, 0, days)
	rows := make([]byte, n*26)
	duration := to.UnixMilli() - from.UnixMilli()
	for i := 0; i < n; i++ {
		delta := int64(i) * duration / int64(n)
		if bursty { // active first 10 minutes per six hours, with long empty gaps
			const cycle = int64(6 * time.Hour / time.Millisecond)
			delta = delta/cycle*cycle + (delta%cycle)/36
		}
		ms := from.UnixMilli() + delta
		binary.BigEndian.PutUint64(rows[i*26:], uint64(ms)<<16)
		rows[i*26+6] = 0x70
		rows[i*26+8] = 0x80
		binary.BigEndian.PutUint32(rows[i*26+12:], uint32(i))
	}
	f := tdFixture{rows: rows, semantic: tdCalendar(rows, from, to), queries: map[string][]tdQuery{}}
	for _, depth := range []int{3, 4, 6} {
		f.radices = append(f.radices, tdBuild(rows, depth))
	}
	rng := rand.New(rand.NewSource(71))
	hours := int(to.Sub(from) / time.Hour)
	for _, kind := range []string{"day", "hour", "seven_days"} {
		for i := 0; i < 1024; i++ {
			var a, b time.Time
			switch kind {
			case "day":
				a = from.AddDate(0, 0, rng.Intn(days))
				b = a.AddDate(0, 0, 1)
			case "seven_days":
				a = from.AddDate(0, 0, rng.Intn(days-6))
				b = a.AddDate(0, 0, 7)
			default:
				a = from.Add(time.Duration(rng.Intn(hours)) * time.Hour)
				b = a.Add(time.Hour)
			}
			// An exclusive endpoint at the overall end uses the last day's final boundary.
			key := func(t time.Time) (int, int, int) {
				day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
				if t.Equal(to) {
					day = day.AddDate(0, 0, -1)
				}
				return day.Year(), day.YearDay(), int(t.Sub(day) / time.Hour)
			}
			fy, fd, fh := key(a)
			ty, td, th := key(b)
			q := tdQuery{from: a.UnixMilli(), to: b.UnixMilli(), fy: fy, fd: fd, fh: fh, ty: ty, td: td, th: th}
			q.expected = tdSpan{lower(rows, 26, 0, uint64(n), q.from), lower(rows, 26, 0, uint64(n), q.to)}
			f.queries[kind] = append(f.queries[kind], q)
		}
	}
	return f
}

var tdSink tdSpan

func TestTimeDirectories(t *testing.T) {
	for _, bursty := range []bool{false, true} {
		f := tdData(100003, 365, bursty)
		for _, qs := range f.queries {
			for _, q := range qs {
				got := tdSpan{f.semantic.edge(q.fy, q.fd, q.fh), f.semantic.edge(q.ty, q.td, q.th)}
				if got != q.expected {
					t.Fatalf("calendar %v != %v", got, q.expected)
				}
				loc, _ := time.LoadLocation("America/New_York")
				got = tdSpan{f.semantic.epochEdge(q.from, loc), f.semantic.epochEdge(q.to, loc)}
				if got != q.expected {
					t.Fatalf("epoch calendar %v != %v", got, q.expected)
				}
				got = tdSpan{tdLower(f.rows, 0, uint64(len(f.rows)/26), q.from), tdLower(f.rows, 0, uint64(len(f.rows)/26), q.to)}
				if got != q.expected {
					t.Fatal("word binary mismatch")
				}

				for i := range f.radices {
					r := &f.radices[i]
					got = tdSpan{r.edge(q.from), r.edge(q.to)}
					if got != q.expected {
						t.Fatalf("radix%d %v != %v", r.depth, got, q.expected)
					}
				}
			}
		}
		rng := rand.New(rand.NewSource(17))
		for i := 0; i < 10000; i++ {
			ms := int64(tdStamp(f.rows, 0)) - 100000 + int64(rng.Intn(370*86400))*1000
			want := lower(f.rows, 26, 0, uint64(len(f.rows)/26), ms)
			for j := range f.radices {
				r := &f.radices[j]
				if got := r.edge(ms); got != want {
					t.Fatalf("radix%d arbitrary %d: %d != %d", r.depth, ms, got, want)
				}
			}
		}
	}
}
func BenchmarkTimeDirectories(b *testing.B) {
	fixtures := []struct {
		n, days int
		burst   bool
	}{{100000, 30, false}, {1000000, 30, false}, {1000000, 30, true}, {1000000, 365, false}}
	if os.Getenv("TIME_DIRECTORY_LARGE") == "1" {
		fixtures = append(fixtures, struct {
			n, days int
			burst   bool
		}{10000000, 30, false})
	}
	for _, cfg := range fixtures {
		b.Run(fmt.Sprintf("n%d_days%d_burst%t", cfg.n, cfg.days, cfg.burst), func(b *testing.B) {
			f := tdData(cfg.n, cfg.days, cfg.burst)
			loc, _ := time.LoadLocation("America/New_York")
			for _, kind := range []string{"day", "hour", "seven_days"} {
				qs := f.queries[kind]
				for _, method := range []string{"binary_current", "binary_word", "semantic", "semantic_epoch_input", "radix3", "radix4", "radix6"} {
					b.Run(kind+"/"+method, func(b *testing.B) {
						bytes := 0
						var r *tdRadix
						switch method {
						case "semantic", "semantic_epoch_input":
							bytes = f.semantic.bytes
						case "radix3", "radix4", "radix6":
							i := 0
							if method == "radix4" {
								i = 1
							}
							if method == "radix6" {
								i = 2
							}
							r = &f.radices[i]
							bytes = cap(r.nodes)*int(unsafe.Sizeof(tdNode{})) + cap(r.branches)*int(unsafe.Sizeof(tdBranch{}))
						}
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							q := qs[i%len(qs)]
							var got tdSpan
							switch method {
							case "binary_current":
								got = tdSpan{lower(f.rows, 26, 0, uint64(cfg.n), q.from), lower(f.rows, 26, 0, uint64(cfg.n), q.to)}
							case "binary_word":
								got = tdSpan{tdLower(f.rows, 0, uint64(cfg.n), q.from), tdLower(f.rows, 0, uint64(cfg.n), q.to)}
							case "semantic_epoch_input":
								got = tdSpan{f.semantic.epochEdge(q.from, loc), f.semantic.epochEdge(q.to, loc)}
							case "semantic":
								got = tdSpan{f.semantic.edge(q.fy, q.fd, q.fh), f.semantic.edge(q.ty, q.td, q.th)}
							default:
								got = tdSpan{r.edge(q.from), r.edge(q.to)}
							}
							tdSink = got
						}
						b.StopTimer()
						b.ReportMetric(float64(bytes), "aux-B")
						if r != nil {
							b.ReportMetric(float64(len(r.nodes)), "nodes")
						}
						runtime.KeepAlive(f)
					})
				}
			}
		})
	}
}

func TestTimeDirectoriesDuplicateMilliseconds(t *testing.T) {
	rows := make([]byte, 4096*26)
	base := uint64(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	for i := 0; i < 4096; i++ {
		binary.BigEndian.PutUint64(rows[i*26:], (base+uint64(i/1024)*65536)<<16)
	}
	for _, depth := range []int{3, 4, 6} {
		r := tdBuild(rows, depth)
		for _, delta := range []int64{-1, 0, 1, 65535, 65536, 65537, 131072, 196608, 196609, 1 << 24} {
			ms := int64(base) + delta
			want := lower(rows, 26, 0, 4096, ms)
			if got := r.edge(ms); got != want {
				t.Fatalf("depth%d delta%d: %d != %d", depth, delta, got, want)
			}
		}
	}
}

// Verify every timed fixture and query, including the ten-million-row case.
func TestTimeDirectoryMeasuredFixtures(t *testing.T) {
	if os.Getenv("TIME_DIRECTORY_LARGE") != "1" {
		t.Skip("set TIME_DIRECTORY_LARGE=1 for full benchmark fixtures")
	}
	for _, cfg := range []struct {
		n, days int
		burst   bool
	}{{100000, 30, false}, {1000000, 30, false}, {1000000, 30, true}, {1000000, 365, false}, {10000000, 30, false}} {
		f := tdData(cfg.n, cfg.days, cfg.burst)
		loc, _ := time.LoadLocation("America/New_York")
		for _, qs := range f.queries {
			for _, q := range qs {
				got := tdSpan{f.semantic.edge(q.fy, q.fd, q.fh), f.semantic.edge(q.ty, q.td, q.th)}
				if got != q.expected {
					t.Fatal("semantic boundary mismatch")
				}
				got = tdSpan{f.semantic.epochEdge(q.from, loc), f.semantic.epochEdge(q.to, loc)}
				if got != q.expected {
					t.Fatal("semantic epoch conversion mismatch")
				}
				got = tdSpan{tdLower(f.rows, 0, uint64(cfg.n), q.from), tdLower(f.rows, 0, uint64(cfg.n), q.to)}
				if got != q.expected {
					t.Fatal("word binary mismatch")
				}
				for i := range f.radices {
					r := &f.radices[i]
					got = tdSpan{r.edge(q.from), r.edge(q.to)}
					if got != q.expected {
						t.Fatalf("%d rows radix%d boundary mismatch", cfg.n, r.depth)
					}
				}
			}
		}
	}
}
