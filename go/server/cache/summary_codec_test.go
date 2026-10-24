package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func aggregateFixture() Summary {
	return summaryFromGroups(4, 10, []Group{
		{0, 0, Aggregate{Count: 2}},
		{0, 7, Aggregate{Count: 3, DurationCount: 2, Sum: 10, Min: 4, Max: 6}},
		{300000, 2, Aggregate{Count: 1, DurationCount: 1}},
	})
}
func TestFixedSummaryLayout(t *testing.T) {
	s := aggregateFixture()
	raw, e := encodeSummary(s)
	if e != nil {
		t.Fatal(e)
	}
	if string(raw[:8]) != "SUMCOL00" || len(raw) != 116+summaryEncodingSize+3*14+2 {
		t.Fatalf("layout %d", len(raw))
	}
	var got Summary
	if e = decodeSummary(raw, &got); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatal("round trip")
	}
	for n := 0; n < len(raw); n++ {
		if decodeSummary(raw[:n], &got) == nil {
			t.Fatalf("truncation %d", n)
		}
	}
	for _, at := range []int{0, 8, 16, 24, 32, 36, 40, 44, 48, 64, 68} {
		b := bytes.Clone(raw)
		for j := 0; j < 4; j++ {
			b[at+j] = 255
		}
		if decodeSummary(b, &got) == nil {
			t.Fatalf("corruption %d", at)
		}
	}
	if decodeSummary(append(bytes.Clone(raw), 0), &got) == nil {
		t.Fatal("trailing data")
	}
}
func TestFixedRollupLayout(t *testing.T) {
	r := rollup{From: -3600000, To: 0, Groups: []Group{{-3600000, 1, Aggregate{Count: 2, DurationCount: 1, Sum: 20, Min: 20, Max: 20}}}}
	for _, groups := range [][]Group{r.Groups, {}} {
		r.Groups = groups
		raw, e := encodeRollup(r)
		if e != nil {
			t.Fatal(e)
		}
		if len(raw) != 24+42*len(groups) {
			t.Fatal("rollup stride")
		}
		var got rollup
		if e = decodeRollup(raw, &got); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(got, r) {
			t.Fatal("rollup round trip")
		}
		for n := 0; n < len(raw); n++ {
			var got rollup
			if decodeRollup(raw[:n], &got) == nil {
				t.Fatalf("accepted truncation %d", n)
			}
		}
	}
}

func TestMixedAggregateRecoveryAndRollupPublication(t *testing.T) {
	l := testLayout(t, false)
	from := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return from.Add(72 * time.Hour) }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Append(Write, testFrame(t, l, from, 1, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	b := s.branches[Write]
	// Rewrite the first durable summary and verify committed recovery.
	legacy, e := encodeSummary(b.summaries[0])
	if e != nil {
		t.Fatal(e)
	}
	if e = b.sum.Truncate(headerSize); e != nil {
		t.Fatal(e)
	}
	if e = appendRecord(b.sum, legacy); e != nil {
		t.Fatal(e)
	}
	b.sumEnd = uint64(headerSize + 8 + len(legacy))
	commit, e := encoder.Marshal(Commit{Run: b.runs[0]})
	if e != nil {
		t.Fatal(e)
	}
	if e = b.journal.Truncate(headerSize); e != nil {
		t.Fatal(e)
	}
	if e = appendRecord(b.journal, commit); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	// Add a separate complete hour after recovery.
	commitLegacyFixture(t, s, Write, testFrame(t, l, from.Add(time.Hour), 2, true))
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	groups, e := s.Summaries(Write, from, from.Add(2*time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	var all Aggregate
	for _, g := range groups {
		for _, a := range g {
			if e = all.merge(a); e != nil {
				t.Fatal(e)
			}
		}
	}
	if all.Count != 2 || all.DurationCount != 1 || all.Sum != 11 {
		t.Fatalf("mixed totals %+v", all)
	}
	if e = s.PublishRollup(Write, Daily, from); e != nil {
		t.Fatal(e)
	}
	path, _, _, e := s.rollupPeriod(Write, Daily, from)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.OpenFile(path, os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	var value rollup
	if e = records(f, func(raw []byte) error { return decodeRollup(raw, &value) }); e != nil {
		t.Fatal(e)
	}
	raw, e := encodeRollup(value)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(headerSize); e != nil {
		t.Fatal(e)
	}
	if e = appendRecord(f, raw); e != nil {
		t.Fatal(e)
	}
	f.Close()
	// Publishing an equivalent rollup is idempotent, not a conflict.
	if e = s.PublishRollup(Write, Daily, from); e != nil {
		t.Fatal(e)
	}
	if e = s.PublishRollup(Write, Daily, from.AddDate(0, 0, 1)); e != nil {
		t.Fatal(e)
	}
	got, e := s.ReadRollup(Write, Daily, from)
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != len(value.Groups) {
		t.Fatal("mixed rollup read")
	}
	for _, branch := range []Branch{Debug, Trace} {
		if _, e = s.Append(branch, testFrame(t, l, from, 3, false)); e != nil {
			t.Fatal(e)
		}
		if e = s.Flush(); e != nil {
			t.Fatal(e)
		}
		if _, e = os.Stat(filepath.Join(s.dir, branch.Prefix()+".sum")); !os.IsNotExist(e) {
			t.Fatal("unexpected aggregate file")
		}
	}
}
