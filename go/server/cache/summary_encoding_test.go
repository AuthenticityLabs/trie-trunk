package cache

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSummaryNumericWidthsAndBases(t *testing.T) {
	// Every event in the hour may fall in the same five-minute bucket.
	for _, n := range []uint64{255, 256, 65535, 65536, 1<<24 - 1, 1 << 24} {
		s := summaryFromGroups(1, n+1, []Group{{0, 0, Aggregate{Count: n, DurationCount: n, Sum: n * 50000, Min: 50000, Max: 50000}}})
		s.DurationWidth = 2
		s.Statistics = []statisticsGroup{{Count: n, Durations: [][2]uint64{{50000, n}}}}
		for _, based := range []bool{false, true} {
			enc, e := chooseSummaryEncoding([]Summary{s}, based)
			if e != nil {
				t.Fatal(e)
			}
			if enc[0].Width != summaryValueWidth(n) || enc[8].Width != summaryValueWidth(n) {
				t.Fatal("count narrowed incorrectly", n, enc)
			}
			if based && (enc[3].Base != 50000 || enc[7].Width != 1) {
				t.Fatal(enc)
			}
			raw, e := encodeSummaryDayColumns(0, 0, summaryDayMS, []Summary{s}, true, 6, based)
			if e != nil {
				t.Fatal(e)
			}
			if dir := os.Getenv("TRUNK_SUMMARY_FIXTURE"); dir != "" && based {
				dir = filepath.Join(dir, "counts")
				if e = os.MkdirAll(dir, 0755); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.sumday", n)), raw, 0644); e != nil {
					t.Fatal(e)
				}
			}
			_, _, _, got, e := decodeSummaryDay(raw)
			if e != nil || !reflect.DeepEqual(got, []Summary{s}) {
				t.Fatal("round trip", e, got)
			}
		}
	}
	// Full U64 extremes must remain exact; zero-duration rows do not pull the base down.
	s := summaryFromGroups(1, 3, []Group{{0, 0, Aggregate{Count: 1}}, {0, 1, Aggregate{Count: 1, DurationCount: 1, Sum: math.MaxUint64, Min: math.MaxUint64, Max: math.MaxUint64}}})
	enc, e := chooseSummaryEncoding([]Summary{s}, true)
	if e != nil || enc[3].Base != math.MaxUint64 {
		t.Fatal(e, enc)
	}
	raw, e := encodeSummaryDay(0, 0, summaryDayMS, []Summary{s}, true)
	if e != nil {
		t.Fatal(e)
	}
	_, _, _, got, e := decodeSummaryDay(raw)
	if e != nil || !reflect.DeepEqual(got, []Summary{s}) {
		t.Fatal(e, got)
	}
	if _, e = summaryGet([]byte{1}, summaryNumber{1, math.MaxUint64}); e == nil {
		t.Fatal("base overflow accepted")
	}
	s.DurationWidth = 4
	if _, e = encodeSummary(s); e == nil {
		t.Fatal("Definition bound ignored")
	}
}

func TestSummaryDurationDefinitionWidths(t *testing.T) {
	for _, typ := range []string{"U8", "U16", "U32", "U64"} {
		d := testLayout(t, false).Definition
		d.Fields[3].Type = typ
		l, e := Compile(d)
		if e != nil {
			t.Fatal(e)
		}
		at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
		s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(25 * time.Hour) }})
		if e != nil {
			t.Fatal(e)
		}
		var f Frame
		old := testLayout(t, false)
		if e = old.Scan(testFrame(t, old, at, 1, false), &f); e != nil {
			t.Fatal(e)
		}
		f.Values[l.Duration] = Uint(42, l.Widths[l.Duration])
		raw, e := l.Encode(nil, f.Values)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(Write, raw); e != nil {
			t.Fatal(e)
		}
		if e = s.Flush(); e != nil {
			t.Fatal(e)
		}
		sum := s.branches[Write].summaries[0]
		if sum.DurationWidth != byte(l.Widths[l.Duration]) || sum.Columns.Sum[0] != 42 {
			t.Fatal(sum)
		}
		encoded, e := encodeSummary(sum)
		if e != nil {
			t.Fatal(e)
		}
		var got Summary
		if e = decodeSummary(encoded, &got); e != nil || !reflect.DeepEqual(got, sum) {
			t.Fatal(e)
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
	}
}

func TestSummaryDescriptorCorruption(t *testing.T) {
	raw, e := encodeSummary(aggregateFixture())
	if e != nil {
		t.Fatal(e)
	}
	for _, at := range []int{116, 116 + 9*2, 116 + 9*5 + 1} {
		bad := bytes.Clone(raw)
		bad[at] = 0xff
		var got Summary
		if decodeSummary(bad, &got) == nil {
			t.Fatal("invalid descriptor accepted", at)
		}
	}
}

func TestSummaryColumnWidthsAndShuffleOwnership(t *testing.T) {
	for width := 1; width <= 8; width++ {
		maxValue := uint64(math.MaxUint64)
		if width < 8 {
			maxValue = uint64(1)<<(width*8) - 1
		}
		for _, base := range []uint64{0, math.MaxUint64 - maxValue} {
			c := summaryNumber{width, base}
			raw := make([]byte, width)
			for _, value := range []uint64{base, base + maxValue} {
				summaryPut(raw, value, c)
				got, e := summaryGet(raw, c)
				if e != nil || got != value {
					t.Fatal(width, base, value, got, e)
				}
			}
		}
	}
	s := aggregateFixture()
	raw, e := encodeSummaryDayFiltered(0, 0, summaryDayMS, []Summary{s}, false, 6, true, true)
	if e != nil {
		t.Fatal(e)
	}
	before := bytes.Clone(raw)
	for i := 0; i < 2; i++ {
		if _, _, _, _, e = decodeSummaryDay(raw); e != nil {
			t.Fatal(e)
		}
	}
	if !bytes.Equal(before, raw) {
		t.Fatal("decoder mutated supplied packet")
	}
}
