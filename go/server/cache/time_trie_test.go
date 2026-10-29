package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func timeRun(ms int64, start, events uint64) Run {
	return Run{Start: start + 1, End: start + 1 + events, MinMS: ms, MaxMS: ms, PAC: &StoredPAC{Placement: PACPlacement{From: ms, To: ms + TimeHourMS, MemberStart: 4 + 4*events}}}
}

func TestPositionalTimeTrieNullZeroAndHourlySegments(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	runs := []Run{timeRun(from+TimeHourMS, 0, 6), timeRun(from+3*TimeHourMS, 7, 1)}
	tree, err := buildTimeDirectory(runs, from, from+5*TimeHourMS, 9, [16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.raw) != TimeTrieHeader+TimeTrieYearBytes {
		t.Fatal("variable annual footprint", len(tree.raw))
	}
	for h, want := range []TimeLeaf{{Start: 0, End: 0, Segments: 0}, {Start: 0, End: 7, Segments: 1}, {Start: 7, End: 7}, {Start: 7, End: 9, Segments: 1}, {Start: 9, End: 9}} {
		got, ok := tree.leaf(from + int64(h)*TimeHourMS)
		if !ok || got.State != TimeComplete || got.Start != want.Start || got.End != want.End || got.Segments != want.Segments {
			t.Fatalf("hour %d: %+v", h, got)
		}
	}
	if _, ok := tree.leaf(from + 5*TimeHourMS); ok {
		t.Fatal("future hour is not blank")
	}
	unused := TimeTrieHeader + 365*TimeTrieDayBytes
	if !bytes.Equal(tree.raw[unused:unused+TimeTrieDayBytes], make([]byte, TimeTrieDayBytes)) {
		t.Fatal("non-leap day 366 not blank")
	}
	if len(tree.raw) != TimeTrieHeader+366*TimeTrieDayBytes {
		t.Fatal("server time trie contains a client status tail")
	}
	if os.Getenv("UPDATE_TIME_TRIE_FIXTURE") == "1" {
		dir := "../../../client/services/fixtures/time-trie"
		if e := os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
		patch, e := tree.window(from, from+5*TimeHourMS, false)
		if e != nil {
			t.Fatal(e)
		}
		for n, raw := range map[string][]byte{"server.tri": tree.raw, "window.bin": patch} {
			if e = os.WriteFile(filepath.Join(dir, n), raw, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
	parsed, err := parseTimeDirectory(tree.raw, [16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := parsed.leaf(from + TimeHourMS); !ok || got.Segments != 1 {
		t.Fatal("disk traversal changed result")
	}
	if _, err = parseTimeDirectory(tree.raw, [16]byte{2}); err == nil {
		t.Fatal("wrong idx source accepted")
	}
}

func TestPositionalTimeTrieLeapRolloverAndRetentionBases(t *testing.T) {
	from := time.Date(2028, 12, 30, 0, 0, 0, 0, time.UTC).UnixMilli()
	runs := []Run{timeRun(from, 100, 3), timeRun(from+25*TimeHourMS, 104, 7), timeRun(from+49*TimeHourMS, 112, 1)}
	old, e := buildTimeDirectory(runs, from, from+50*TimeHourMS, 114, [16]byte{1})
	if e != nil {
		t.Fatal(e)
	}
	retained, e := buildTimeDirectory(runs[1:], from+24*TimeHourMS, from+50*TimeHourMS, 114, [16]byte{1})
	if e != nil {
		t.Fatal(e)
	}
	if e = retained.preserveBases(&old); e != nil {
		t.Fatal(e)
	}
	for _, ms := range []int64{from + 25*TimeHourMS, from + 49*TimeHourMS} {
		a, _ := old.leaf(ms)
		b, ok := retained.leaf(ms)
		if !ok || a != b {
			t.Fatal("retention moved surviving reference", a, b)
		}
		if old.origin() != retained.origin() {
			t.Fatal("retention changed hour origin")
		}

	}
	last, e := buildTimeDirectory(runs[2:], from+48*TimeHourMS, from+50*TimeHourMS, 114, [16]byte{1})
	if e != nil {
		t.Fatal(e)
	}
	if e = last.preserveBases(&old); e != nil {
		t.Fatal(e)
	}
	if len(last.raw) != TimeTrieHeader+TimeTrieYearBytes {
		t.Fatal("retired year was not removed")
	}
	if a, _ := last.leaf(from + 49*TimeHourMS); a.Start != 112 {
		t.Fatal(a)
	}
}

func TestPositionalTimeTrieRestartRecoveryAndUnchangedPayloads(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 12, 31, 22, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(4 * time.Hour) }, BatchRecords: 1}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Duration(i)*time.Minute), uint32(i+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	b := s.branches[Write]
	leaf, ok := b.time.leaf(at.UnixMilli())
	if !ok || leaf.Segments != 1 || leaf.End-leaf.Start != 4 {
		t.Fatal(leaf)
	}
	files := map[string][]byte{}
	for _, ext := range []string{".idx", ".pods"} {
		raw, e := os.ReadFile(filepath.Join(s.dir, "write"+ext))
		if e != nil {
			t.Fatal(e)
		}
		files[ext] = raw
	}
	triPath := filepath.Join(s.dir, "write.tri")
	want := append([]byte(nil), b.time.raw...)
	if _, e = os.Stat(filepath.Join(s.dir, "write.journal")); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	// Crash/torn derived publication: recover only from the separate journal.
	if e = os.WriteFile(triPath, []byte("torn"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if !bytes.Equal(want, s.branches[Write].time.raw) {
		t.Fatal("time directory recovery changed coordinates")
	}
	for ext, want := range files {
		got, e := os.ReadFile(filepath.Join(s.dir, "write"+ext))
		if e != nil || !bytes.Equal(got, want) {
			t.Fatal("changed payload/index format", ext, e)
		}
	}
	if got := collect(t, s, Write, at, at.Add(time.Hour)); len(got) != 3 {
		t.Fatal("direct trie search lost events")
	}
}

func TestPositionalTimeTrieDoesNotPublishInterruptedHour(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(2 * time.Hour) }, BatchRecords: 1}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e = s.Append(Write, testFrame(t, l, at, uint32(i+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	b := s.branches[Write]
	s.mu.Lock()
	e = s.commitBatch(b, b.pendingAll())
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if e = releaseCrashedTestStore(s); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if leaf, ok := s.branches[Write].time.leaf(at.UnixMilli()); ok && leaf.State == TimeComplete {
		t.Fatal("partial hour advertised complete")
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	if leaf, ok := s.branches[Write].time.leaf(at.UnixMilli()); !ok || leaf.Segments != 1 || leaf.State != TimeComplete {
		t.Fatal("recovery did not finish hour", leaf)
	}
}

func TestPositionalTimeTrieRetentionRecovery(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	for i, h := range []int{0, 27, 50} {
		if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Duration(h)*time.Hour), uint32(i+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	before, _ := s.branches[Write].time.leaf(at.Add(27 * time.Hour).UnixMilli())
	s.mu.Lock()
	e = s.prune(Write, at.Add(24*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	want := append([]byte(nil), s.branches[Write].time.raw...)
	path := filepath.Join(s.dir, s.prefix(Write)+".tri")
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("torn"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if !bytes.Equal(want, s.branches[Write].time.raw) {
		t.Fatal("retention recovery changed parent displacements")
	}
	after, ok := s.branches[Write].time.leaf(at.Add(27 * time.Hour).UnixMilli())
	if !ok || before != after {
		t.Fatal("surviving reference changed", before, after)
	}
	if _, ok = s.branches[Write].time.leaf(at.UnixMilli()); ok {
		t.Fatal("retired hour still present")
	}
}

var timeLeafSink TimeLeaf

func BenchmarkPositionalTimeTrie(b *testing.B) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	for _, events := range []uint64{1, 10000} {
		var runs []Run
		row := uint64(0)
		baseline := newTimeTrie()
		for h := 0; h < 365*24; h++ {
			ms := start + int64(h)*TimeHourMS
			r := timeRun(ms, row, events)
			runs = append(runs, r)
			baseline.append(ms, r.Start, time.UTC)
			row = r.End
		}
		tree, e := buildTimeDirectory(runs, start+0, start+365*24*TimeHourMS, row, [16]byte{})
		if e != nil {
			b.Fatal(e)
		}
		for _, q := range []struct {
			name string
			hour int
		}{{"January", 12}, {"December", 8748}} {
			ms := start + int64(q.hour)*TimeHourMS
			for _, mode := range []string{"positional", "map_baseline"} {
				b.Run(fmt.Sprintf("%d_events_per_hour/%s/%s", events, q.name, mode), func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						if mode == "positional" {
							timeLeafSink, _ = tree.leaf(ms)
						} else {
							baseline.visit(time.UnixMilli(ms), time.UnixMilli(ms+TimeHourMS), func(p *Period) { timeLeafSink.Start = uint64(p.Ranges[0].Start) })
						}
					}
				})
			}
		}
	}
}
