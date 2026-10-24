package cache

// Epoch containers are an experimental coverage unit over existing index rows.
// This file adds no wire envelope, production format or new PIL PACK behavior.
import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/bits"
	"os"
	"testing"
	"time"
	"unsafe"
)

const ecWidth int64 = 1 << 24
const ecLimit int64 = 1 << 48

type ecDirectory struct{ radix tdRadix }
type ecContainer struct {
	ID    [3]byte
	Rows  tdSpan
	Index []byte
}

func ecID(prefix uint32) [3]byte { return [3]byte{byte(prefix >> 16), byte(prefix >> 8), byte(prefix)} }

// A boundary is aligned, so its lower bound is a recorded pointer, never a search.
func (d *ecDirectory) edge(ms int64) uint64 {
	if len(d.radix.nodes) == 0 {
		return 0
	}
	if ms == ecLimit {
		return uint64(len(d.radix.rows) / 26)
	}
	key := uint64(ms)
	at := uint32(0)
	for {
		n := d.radix.nodes[at]
		shift := uint(8 * (6 - int(n.depth)))
		if key>>shift < n.prefix>>shift {
			return n.start
		}
		if key>>shift > n.prefix>>shift {
			return n.end
		}
		if n.depth == 3 {
			return n.start
		}
		b := d.radix.branches[n.branch]
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
		return d.radix.nodes[b.first+uint32(rank)].start
	}
}
func ecBounds(from, to int64) (int64, int64, error) {
	if from < 0 || to <= from || to > ecLimit {
		return 0, 0, fmt.Errorf("invalid epoch window")
	}
	return from / ecWidth * ecWidth, ((to-1)/ecWidth + 1) * ecWidth, nil
}
func (d *ecDirectory) window(from, to int64) (tdSpan, error) {
	lo, hi, e := ecBounds(from, to)
	if e != nil {
		return tdSpan{}, e
	}
	return tdSpan{d.edge(lo), d.edge(hi)}, nil
}
func (d *ecDirectory) container(prefix uint32) (ecContainer, error) {
	if prefix > 0xffffff {
		return ecContainer{}, fmt.Errorf("prefix exceeds three bytes")
	}
	lo := int64(prefix) * ecWidth
	span := tdSpan{d.edge(lo), d.edge(lo + ecWidth)}
	return ecContainer{ecID(prefix), span, d.radix.rows[span.start*26 : span.end*26]}, nil
}
func ecRows(n int, burst bool) ([]byte, time.Time, time.Time) {
	loc, _ := time.LoadLocation("America/New_York")
	from := time.Date(2026, 10, 20, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 30)
	rows := make([]byte, n*26)
	duration := to.UnixMilli() - from.UnixMilli()
	for i := 0; i < n; i++ {
		delta := int64(i) * duration / int64(n)
		if burst {
			const cycle = int64(6 * time.Hour / time.Millisecond)
			delta = delta/cycle*cycle + (delta%cycle)/36
		}
		binary.BigEndian.PutUint64(rows[i*26:], uint64(from.UnixMilli()+delta)<<16)
		rows[i*26+6] = 0x70
		rows[i*26+8] = 0x80
		binary.BigEndian.PutUint32(rows[i*26+12:], uint32(i))
	}
	return rows, from, to
}
func ecBuild(rows []byte) ecDirectory {
	if len(rows) == 0 {
		return ecDirectory{}
	}
	return ecDirectory{tdBuild(rows, 3)}
}
func TestEpochContainers(t *testing.T) {
	empty := ecBuild(nil)
	if c, e := empty.container(123); e != nil || len(c.Index) != 0 || c.Rows != (tdSpan{}) {
		t.Fatal("empty source")
	}
	for _, burst := range []bool{false, true} {
		rows, from, to := ecRows(100003, burst)
		d := ecBuild(rows)
		lo, hi, _ := ecBounds(from.UnixMilli(), to.UnixMilli())
		var count uint64
		for ms := lo - ecWidth; ms <= hi; ms += ecWidth {
			c, e := d.container(uint32(ms / ecWidth))
			if e != nil {
				t.Fatal(e)
			}
			want := tdSpan{tdLower(rows, 0, 100003, ms), tdLower(rows, 0, 100003, ms+ecWidth)}
			if c.Rows != want || len(c.Index) != int(want.end-want.start)*26 {
				t.Fatal("container extent mismatch")
			}
			if len(c.Index) > 0 && &c.Index[0] != &rows[want.start*26] {
				t.Fatal("container copies rows")
			}
			for i := want.start; i < want.end; i++ {
				id := rows[i*26 : i*26+3]
				if id[0] != c.ID[0] || id[1] != c.ID[1] || id[2] != c.ID[2] {
					t.Fatal("prefix mismatch")
				}
			}
			if ms >= lo && ms < hi {
				count += want.end - want.start
			}
		}
		if count != 100003 {
			t.Fatal("lost or duplicate rows")
		}
		for i := 0; i < 1000; i++ {
			a := from.UnixMilli() + int64(i)*60001
			b := a + int64(i+1)*3600000
			got, e := d.window(a, b)
			if e != nil {
				t.Fatal(e)
			}
			l, h, _ := ecBounds(a, b)
			want := tdSpan{tdLower(rows, 0, 100003, l), tdLower(rows, 0, 100003, h)}
			if got != want {
				t.Fatal("window mismatch")
			}
		}
		at := lo + ecWidth
		got, _ := d.window(at, at+ecWidth)
		c, _ := d.container(uint32(at / ecWidth))
		if got != c.Rows {
			t.Fatal("exclusive end added container")
		}
	}
	for _, q := range [][2]int64{{-1, 1}, {1, 1}, {2, 1}, {0, ecLimit + 1}} {
		if _, _, e := ecBounds(q[0], q[1]); e == nil {
			t.Fatal("accepted invalid range")
		}
	}
	rows := make([]byte, 26)
	binary.BigEndian.PutUint64(rows, uint64(ecLimit-1)<<16)
	d := ecBuild(rows)
	c, e := d.container(0xffffff)
	if e != nil || c.Rows != (tdSpan{0, 1}) {
		t.Fatal("last prefix")
	}
	if _, e = d.container(0x1000000); e == nil {
		t.Fatal("overflow prefix")
	}
}

func BenchmarkEpochContainers(b *testing.B) {
	for _, n := range []int{100000, 1000000, 10000000} {
		b.Run(fmt.Sprintf("n%d", n), func(b *testing.B) {
			rows, from, to := ecRows(n, false)
			d := ecBuild(rows)
			calendar := tdCalendar(rows, from, to)
			for _, method := range []string{"epoch24_complete", "epoch24_exact", "binary_word_exact", "semantic_complete_hour"} {
				b.Run(method, func(b *testing.B) {
					type query struct {
						from, to  int64
						y, day, h int
					}
					qs := make([]query, 1024)
					for i := range qs {
						at := from.Add(time.Duration((i*137)%int(to.Sub(from)/time.Hour-1)) * time.Hour)
						start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
						qs[i] = query{at.UnixMilli(), at.Add(time.Hour).UnixMilli(), at.Year(), at.YearDay(), int(at.Sub(start) / time.Hour)}
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						q := qs[i%len(qs)]
						switch method {
						case "epoch24_complete":
							tdSink, _ = d.window(q.from, q.to)
						case "epoch24_exact":
							tdSink = tdSpan{d.radix.edge(q.from), d.radix.edge(q.to)}
						case "binary_word_exact":
							tdSink = tdSpan{tdLower(rows, 0, uint64(n), q.from), tdLower(rows, 0, uint64(n), q.to)}
						default:
							tdSink = tdSpan{calendar.edge(q.y, q.day, q.h), calendar.edge(q.y, q.day, q.h+1)}
						}
					}
					b.StopTimer()
					aux := 0
					if method == "semantic_complete_hour" {
						aux = calendar.bytes
					} else if method != "binary_word_exact" {
						aux = cap(d.radix.nodes)*int(unsafe.Sizeof(tdNode{})) + cap(d.radix.branches)*int(unsafe.Sizeof(tdBranch{}))
					}
					b.ReportMetric(float64(aux), "aux-B")
				})
			}
		})
	}
}

type ecRequest struct{ From, To time.Time }
type ecStep struct {
	Query                                int
	NewContainers                        int
	NewRows, MatchedRows, CumulativeRows uint64
}
type ecResult struct {
	Workload, Packaging                            string
	Steps                                          []ecStep
	UniqueMatchedRows, TransferredRows, IndexBytes uint64
	Containers                                     int
}

func ecSequence(rows []byte, d *ecDirectory, requests []ecRequest, kind string) ecResult {
	out := ecResult{Packaging: kind}
	have := map[int64]bool{}
	var exact []Span
	for i, q := range requests {
		exact = append(exact, Span{tdLower(rows, 0, uint64(len(rows)/26), q.From.UnixMilli()), tdLower(rows, 0, uint64(len(rows)/26), q.To.UnixMilli())})
		step := ecStep{Query: i + 1, MatchedRows: exact[len(exact)-1].End - exact[len(exact)-1].Start}
		var at, end time.Time
		switch kind {
		case "epoch24":
			lo, hi, _ := ecBounds(q.From.UnixMilli(), q.To.UnixMilli())
			at = time.UnixMilli(lo)
			end = time.UnixMilli(hi)
		case "day":
			at = time.Date(q.From.Year(), q.From.Month(), q.From.Day(), 0, 0, 0, 0, q.From.Location())
			end = q.To
		default:
			day := time.Date(q.From.Year(), q.From.Month(), q.From.Day(), 0, 0, 0, 0, q.From.Location())
			at = day.Add(q.From.Sub(day).Truncate(time.Hour))
			end = q.To
		}
		for at.Before(end) {
			var next time.Time
			switch kind {
			case "epoch24":
				next = at.Add(time.Duration(ecWidth) * time.Millisecond)
			case "day":
				next = at.AddDate(0, 0, 1)
			default:
				next = at.Add(time.Hour)
			}
			key := at.UnixMilli()
			if !have[key] {
				have[key] = true
				step.NewContainers++
				var span tdSpan
				if kind == "epoch24" {
					c, _ := d.container(uint32(key / ecWidth))
					span = c.Rows
				} else {
					span = tdSpan{tdLower(rows, 0, uint64(len(rows)/26), key), tdLower(rows, 0, uint64(len(rows)/26), next.UnixMilli())}
				}
				step.NewRows += span.end - span.start
			}
			at = next
		}
		out.TransferredRows += step.NewRows
		step.CumulativeRows = out.TransferredRows
		out.Steps = append(out.Steps, step)
	}
	nonempty := exact[:0]
	for _, s := range exact {
		if s.Start < s.End {
			nonempty = append(nonempty, s)
		}
	}
	normalized, _ := Normalize(nonempty)
	for _, s := range normalized {
		out.UniqueMatchedRows += s.End - s.Start
	}
	out.Containers = len(have)
	out.IndexBytes = out.TransferredRows * 26
	return out
}

type ecObjectResult struct {
	Objects, RevisionsPerObject int
	ExactRows, ContainerRows    uint64
	Containers                  int
}

func ecObjects(rows []byte, d *ecDirectory, objects int) ecObjectResult {
	out := ecObjectResult{Objects: objects, RevisionsPerObject: 10}
	have := map[uint32]bool{}
	// Synthetic authoritative postings: 100k objects, ten revisions each.
	for j := 0; j < objects; j++ {
		id := (j*7919 + 137) % 100000
		for rev := 0; rev < 10; rev++ {
			row := uint64(rev*100000 + id)
			out.ExactRows++
			prefix := uint32(tdStamp(rows, row) >> 24)
			if !have[prefix] {
				have[prefix] = true
				c, _ := d.container(prefix)
				out.ContainerRows += c.Rows.end - c.Rows.start
			}
		}
	}
	out.Containers = len(have)
	return out
}
func TestEpochContainerWorkloads(t *testing.T) {
	if os.Getenv("EPOCH_CONTAINER_REPORT") != "1" {
		t.Skip("set EPOCH_CONTAINER_REPORT=1 for measurement output")
	}
	type report struct {
		Time    []ecResult
		Objects []ecObjectResult
	}
	var out report
	for _, burst := range []bool{false, true} {
		rows, from, _ := ecRows(1000000, burst)
		d := ecBuild(rows)
		base := from.AddDate(0, 0, 3)
		nearby := [][2]int{{9, 12}, {10, 11}, {9, 12}, {8, 14}, {12, 15}, {6, 18}, {9, 12}, {18, 21}, {0, 24}, {9, 12}}
		var a, b, c []ecRequest
		for _, h := range nearby {
			a = append(a, ecRequest{base.Add(time.Duration(h[0]) * time.Hour), base.Add(time.Duration(h[1]) * time.Hour)})
		}
		for i := 0; i < 10; i++ {
			at := from.AddDate(0, 0, i*3).Add(10 * time.Hour)
			b = append(b, ecRequest{at, at.Add(time.Hour)})
			at = from.AddDate(0, 0, 8+i)
			c = append(c, ecRequest{at.AddDate(0, 0, -7), at})
		}
		for _, work := range []struct {
			name    string
			queries []ecRequest
		}{{"nearby", a}, {"scattered", b}, {"rolling_week", c}} {
			for _, kind := range []string{"hour", "epoch24", "day"} {
				r := ecSequence(rows, &d, work.queries, kind)
				r.Workload = fmt.Sprintf("%s_burst%t", work.name, burst)
				out.Time = append(out.Time, r)
			}
		}
		if !burst {
			for _, n := range []int{1, 10, 100} {
				out.Objects = append(out.Objects, ecObjects(rows, &d, n))
			}
		}
	}
	raw, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	fmt.Println(string(raw))
}

func TestEpochContainerReuse(t *testing.T) {
	rows, from, _ := ecRows(100003, false)
	d := ecBuild(rows)
	base := (from.UnixMilli()/ecWidth + 2) * ecWidth
	first := ecRequest{time.UnixMilli(base + 3600000), time.UnixMilli(base + 7200000)}
	extend := ecRequest{time.UnixMilli(base + 7200000), time.UnixMilli(base + ecWidth + 3600000)}
	result := ecSequence(rows, &d, []ecRequest{first, first, extend, first}, "epoch24")
	if result.Containers != 2 || result.Steps[0].NewContainers != 1 || result.Steps[1].NewRows != 0 || result.Steps[2].NewContainers != 1 || result.Steps[3].NewRows != 0 {
		t.Fatal("incorrect cache reuse", result)
	}
	span, _ := d.window(base, base+2*ecWidth)
	if result.TransferredRows != span.end-span.start {
		t.Fatal("duplicate or missing transfer")
	}
}
