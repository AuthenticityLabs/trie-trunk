package cache

// Controlled experiment only: identical packed boundary tables for both schemes.
import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"
	"unsafe"
)

type fhScheme struct {
	name     string
	location *time.Location
	width    int64
}

func fhSchemes(loc *time.Location) []fhScheme {
	return []fhScheme{{"semantic4h", loc, 0}, {"epoch24", loc, ecWidth}, {"epoch4h_control", loc, 14400000}}
}
func (s fhScheme) key(ms int64) int64 {
	if s.width != 0 {
		return ms / s.width
	}
	t := time.UnixMilli(ms).In(s.location)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Unix() / 86400
	return day*6 + int64(t.Hour()/4)
}
func (s fhScheme) boundary(key int64) int64 {
	if s.width != 0 {
		return key * s.width
	}
	day := time.Unix(key/6*86400, 0).UTC()
	return time.Date(day.Year(), day.Month(), day.Day(), int(key%6)*4, 0, 0, 0, s.location).UnixMilli()
}
func (s fhScheme) keys(from, to int64) (int64, int64) { return s.key(from), s.key(to-1) + 1 }

type fhDirectory struct {
	base  int64
	edges []uint64
}

func fhBuild(rows []byte, from, to int64, s fhScheme) fhDirectory {
	lo, hi := s.keys(from, to)
	d := fhDirectory{base: lo, edges: make([]uint64, hi-lo+1)}
	for i := range d.edges {
		d.edges[i] = tdLower(rows, 0, uint64(len(rows)/26), s.boundary(lo+int64(i)))
	}
	return d
}
func (d fhDirectory) edge(key int64) uint64 {
	i := key - d.base
	if i < 0 {
		return 0
	}
	if i >= int64(len(d.edges)) {
		return d.edges[len(d.edges)-1]
	}
	return d.edges[i]
}
func (d fhDirectory) span(lo, hi int64) tdSpan { return tdSpan{d.edge(lo), d.edge(hi)} }
func (d fhDirectory) bytes() int               { return int(unsafe.Sizeof(d)) + cap(d.edges)*8 }
func fhClock(base time.Time, day, hour int) time.Time {
	a := base.AddDate(0, 0, day)
	return time.Date(a.Year(), a.Month(), a.Day(), hour, 0, 0, 0, a.Location())
}
func fhWorkloads(from time.Time) map[string][]ecRequest {
	out := map[string][]ecRequest{}
	for _, h := range [][2]int{{9, 12}, {10, 11}, {9, 12}, {8, 14}, {12, 15}, {6, 18}, {9, 12}, {18, 21}, {0, 24}, {9, 12}} {
		out["nearby"] = append(out["nearby"], ecRequest{fhClock(from, 3, h[0]), fhClock(from, 3, h[1])})
	}
	for i := 0; i < 10; i++ {
		a := fhClock(from, i*3, 10)
		out["scattered_hour"] = append(out["scattered_hour"], ecRequest{a, a.Add(time.Hour)})
		a = fhClock(from, i*3, 8)
		out["aligned_4h"] = append(out["aligned_4h"], ecRequest{a, fhClock(from, i*3, 12)})
		a = from.AddDate(0, 0, 8+i)
		out["rolling_week"] = append(out["rolling_week"], ecRequest{a.AddDate(0, 0, -7), a})
	}
	return out
}

type fhStep struct {
	Query                                int
	Containers                           int
	NewRows, MatchedRows, CumulativeRows uint64
}
type fhResult struct {
	Data, Workload, Scheme                          string
	Containers, DirectoryBytes, DirectoryContainers int
	MatchedRows, CollectedRows                      uint64
	Steps                                           []fhStep
}

func fhSequence(rows []byte, d fhDirectory, s fhScheme, requests []ecRequest) fhResult {
	out := fhResult{Scheme: s.name, DirectoryBytes: d.bytes(), DirectoryContainers: len(d.edges) - 1}
	have := map[int64]bool{}
	var wanted []Span
	for i, q := range requests {
		from, to := q.From.UnixMilli(), q.To.UnixMilli()
		lo, hi := s.keys(from, to)
		exact := Span{tdLower(rows, 0, uint64(len(rows)/26), from), tdLower(rows, 0, uint64(len(rows)/26), to)}
		if exact.Start < exact.End {
			wanted = append(wanted, exact)
		}
		step := fhStep{Query: i + 1, MatchedRows: exact.End - exact.Start}
		for k := lo; k < hi; k++ {
			if !have[k] {
				have[k] = true
				span := d.span(k, k+1)
				step.NewRows += span.end - span.start
				step.Containers++
			}
		}
		out.CollectedRows += step.NewRows
		step.CumulativeRows = out.CollectedRows
		out.Steps = append(out.Steps, step)
	}
	normalized, _ := Normalize(wanted)
	for _, r := range normalized {
		out.MatchedRows += r.End - r.Start
	}
	out.Containers = len(have)
	return out
}

// A cache-free randomized-window control separates cold overfetch from reuse.
type fhCold struct {
	Scheme    string
	Hours     int
	Samples   int
	MeanHours float64
	MaxHours  float64
}

func fhColdWindows(from, to time.Time, s fhScheme, hours int) fhCold {
	out := fhCold{Scheme: s.name, Hours: hours, Samples: 10000}
	rng := rand.New(rand.NewSource(97))
	duration := int64(hours) * 3600000
	available := to.UnixMilli() - from.UnixMilli() - duration
	for i := 0; i < out.Samples; i++ {
		a := from.UnixMilli() + rng.Int63n(available)
		lo, hi := s.keys(a, a+duration)
		h := float64(s.boundary(hi)-s.boundary(lo)) / 3600000
		out.MeanHours += h
		if h > out.MaxHours {
			out.MaxHours = h
		}
	}
	out.MeanHours /= float64(out.Samples)
	return out
}
func TestFourHourComparison(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	for _, burst := range []bool{false, true} {
		rows, from, to := ecRows(100003, burst)
		for _, s := range fhSchemes(loc) {
			d := fhBuild(rows, from.UnixMilli(), to.UnixMilli(), s)
			var count uint64
			for i := 0; i < len(d.edges)-1; i++ {
				k := d.base + int64(i)
				span := d.span(k, k+1)
				count += span.end - span.start
				for r := span.start; r < span.end; r++ {
					if s.key(int64(tdStamp(rows, r))) != k {
						t.Fatal("record in wrong package")
					}
				}
			}
			if count != 100003 {
				t.Fatal("missing/duplicate rows")
			}
			for _, qs := range fhWorkloads(from) {
				for _, q := range qs {
					lo, hi := s.keys(q.From.UnixMilli(), q.To.UnixMilli())
					got := d.span(lo, hi)
					want := tdSpan{tdLower(rows, 0, 100003, s.boundary(lo)), tdLower(rows, 0, 100003, s.boundary(hi))}
					if got != want {
						t.Fatal("window mismatch")
					}
				}
			}
			q := ecRequest{fhClock(from, 4, 8), fhClock(from, 4, 12)}
			r := fhSequence(rows, d, s, []ecRequest{q, q})
			if r.Steps[1].NewRows != 0 || r.Steps[1].Containers != 0 {
				t.Fatal("repeat not cached")
			}
			lo := d.base + 2
			a, b := s.boundary(lo), s.boundary(lo+1)
			x, y := s.keys(a, b)
			if x != lo || y != lo+1 {
				t.Fatal("exclusive end extends package")
			}
		}
	}
	// Calendar four-hour packages retain 00,04,08,... wall-clock edges over DST.
	semantic := fhSchemes(loc)[0]
	for _, c := range []struct {
		month     time.Month
		day, want int
	}{{time.March, 8, 3}, {time.November, 1, 5}} {
		a := time.Date(2026, c.month, c.day, 0, 0, 0, 0, loc)
		k := semantic.key(a.UnixMilli())
		duration := (semantic.boundary(k+1) - semantic.boundary(k)) / 3600000
		if duration != int64(c.want) {
			t.Fatal("DST package duration", duration)
		}
		rows := make([]byte, 48*26)
		for i := 0; i < 48; i++ {
			binary.BigEndian.PutUint64(rows[i*26:], uint64(a.Add(time.Duration(i)*30*time.Minute).UnixMilli())<<16)
		}
		d := fhBuild(rows, a.UnixMilli(), a.AddDate(0, 0, 1).UnixMilli(), semantic)
		span := d.span(k, k+1)
		if span.end-span.start != uint64(c.want*2) {
			t.Fatal("DST row range")
		}
	}
	for _, zone := range []string{"UTC", "Asia/Kathmandu"} {
		l, _ := time.LoadLocation(zone)
		s := fhSchemes(l)[0]
		a := time.Date(2026, 1, 1, 0, 0, 0, 0, l)
		for i := 0; i < 12; i++ {
			at := a.Add(time.Duration(i*4) * time.Hour)
			k := s.key(at.UnixMilli())
			if s.boundary(k) != at.UnixMilli() {
				t.Fatal("non-NY alignment")
			}
		}
	}
}
func BenchmarkFourHourComparison(b *testing.B) {
	for _, n := range []int{100000, 1000000, 10000000} {
		b.Run(fmt.Sprintf("n%d", n), func(b *testing.B) {
			rows, from, to := ecRows(n, false)
			for _, s := range fhSchemes(from.Location()) {
				d := fhBuild(rows, from.UnixMilli(), to.UnixMilli(), s)
				for _, input := range []string{"resolved_keys", "epoch_input"} {
					b.Run(s.name+"/"+input, func(b *testing.B) {
						type query struct{ from, to, lo, hi int64 }
						qs := make([]query, 1024)
						for i := range qs {
							a := from.Add(time.Duration((i*137)%int(to.Sub(from)/time.Hour-1)) * time.Hour)
							lo, hi := s.keys(a.UnixMilli(), a.Add(time.Hour).UnixMilli())
							qs[i] = query{a.UnixMilli(), a.Add(time.Hour).UnixMilli(), lo, hi}
						}
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							q := qs[i%len(qs)]
							lo, hi := q.lo, q.hi
							if input == "epoch_input" {
								lo, hi = s.keys(q.from, q.to)
							}
							tdSink = d.span(lo, hi)
						}
						b.StopTimer()
						b.ReportMetric(float64(d.bytes()), "directory-B")
						b.ReportMetric(float64(len(d.edges)-1), "containers")
					})
				}
			}
		})
	}
}
func TestFourHourWorkloads(t *testing.T) {
	if os.Getenv("FOUR_HOUR_REPORT") != "1" {
		t.Skip("set FOUR_HOUR_REPORT=1")
	}
	var out struct {
		Workloads []fhResult
		Cold      []fhCold
	}
	for _, burst := range []bool{false, true} {
		rows, from, to := ecRows(1000000, burst)
		work := fhWorkloads(from)
		for _, s := range fhSchemes(from.Location()) {
			d := fhBuild(rows, from.UnixMilli(), to.UnixMilli(), s)
			for _, name := range []string{"nearby", "scattered_hour", "aligned_4h", "rolling_week"} {
				r := fhSequence(rows, d, s, work[name])
				r.Data = fmt.Sprintf("burst%t", burst)
				r.Workload = name
				out.Workloads = append(out.Workloads, r)
			}
			if !burst {
				for _, h := range []int{1, 3, 4, 24, 168} {
					out.Cold = append(out.Cold, fhColdWindows(from, to, s, h))
				}
			}
		}
	}
	raw, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	fmt.Println(string(raw))
}
