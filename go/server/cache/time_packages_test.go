package cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestSemanticPackagesDSTAndPlanning(t *testing.T) {
	l := testLayout(t, false)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	setTestPackagePeriod(t, s, "12h")
	loc := s.location
	for _, c := range []struct {
		month      time.Month
		day, hours int
	}{{time.March, 8, 12}, {time.November, 1, 12}, {time.December, 31, 12}} {
		start := time.Date(2026, c.month, c.day, 0, 0, 0, 0, loc)
		noon := time.Date(2026, c.month, c.day, 12, 0, 0, 0, loc)
		windows, err := s.PlanPackages(start.Add(time.Minute).UTC(), noon.UTC())
		if err != nil || len(windows) != 1 {
			t.Fatalf("plan %v %v", windows, err)
		}
		w := windows[0]
		if !w.From.Equal(start) || !w.To.Equal(noon) || w.To.Sub(w.From) != time.Duration(c.hours)*time.Hour || w.IndexPeriod != "12h" || w.Timezone != "UTC" {
			t.Fatalf("window %+v", w)
		}
		windows, err = s.PlanPackages(noon.Add(-time.Nanosecond), noon.Add(time.Nanosecond))
		if err != nil || len(windows) != 2 || !windows[0].To.Equal(windows[1].From) {
			t.Fatalf("cross boundary %+v %v", windows, err)
		}
	}
	for _, zone := range []string{"UTC", "Asia/Kathmandu", "Australia/Lord_Howe"} {
		z, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 10, 4, 8, 0, 0, 0, z)
		a, b := semanticBounds(at, 12, z)
		if a.Hour() != 0 || b.Hour() != 12 || !a.Before(at) || !b.After(at) {
			t.Fatalf("%s: %v %v", zone, a, b)
		}
	}
	if _, err = s.PlanPackages(time.Now(), time.Now().Add(-time.Hour)); err == nil {
		t.Fatal("invalid range accepted")
	}
}

func TestSemanticCollectionRestartAndPolicy(t *testing.T) {
	l := testLayout(t, false)
	loc := time.UTC
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	noon := time.Date(2026, 11, 1, 12, 0, 0, 0, loc)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, Now: func() time.Time { return at.AddDate(0, 0, 3) }}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	setTestPackagePeriod(t, s, "12h")
	appendAt := func(at time.Time, n uint32) {
		t.Helper()
		commitLegacyFixture(t, s, Write, testFrame(t, l, at, n, false))
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	appendAt(at.Add(time.Hour), 1)
	appendAt(at.Add(2*time.Hour), 4)
	appendAt(noon, 2)
	appendAt(at.AddDate(0, 0, 1), 3)
	request := CollectionRequest{Branch: Write, At: at, IndexPeriod: "12h", PopulationFrom: at, PopulationTo: noon}
	var manifest Manifest
	var chunks []NativeChunk
	pull := func() {
		t.Helper()
		chunks = nil
		err = s.Collect(context.Background(), request, func(m Manifest) error { manifest = m; return nil }, func(c NativeChunk) error { chunks = append(chunks, c); return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	pull()
	if !reflect.DeepEqual(manifest.Index, []Span{{1, 2}, {3, 4}}) || manifest.Provisional || !manifest.To.Equal(noon) {
		t.Fatalf("initial %+v", manifest)
	}
	request.Have = Coverage{Definition: manifest.Definition, Generation: manifest.Generation, Index: manifest.Index, Population: manifest.Population, TrieNodes: manifest.TrieNodes}
	pull()
	for _, c := range chunks {
		if c.Kind == "index" || c.Kind == "pac" {
			t.Fatal("duplicate transfer")
		}
	}
	before := manifest
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pull()
	if !reflect.DeepEqual(before, manifest) {
		t.Fatalf("restart changed boundaries\nbefore=%+v\nafter=%+v", before, manifest)
	}
	// Exact search still excludes noon.
	if got := collect(t, s, Write, at.UTC(), noon.UTC()); len(got) != 2 {
		t.Fatalf("exact search %d", len(got))
	}
	b := s.branches[Write]
	originalRows := append([]byte(nil), b.rows...)
	originalGeneration := b.generation
	setPolicy := func(period string) {
		t.Helper()
		raw := fmt.Sprintf("version=1\ntimezone='America/New_York'\n[defaults.replication]\nindex_period=%q\nprovisional_tail='30m'\n", period)
		if err := os.WriteFile(filepath.Join(s.root, "registry.cfg"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	setPolicy("6h")
	if err = s.ReloadPolicy(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(originalRows, b.rows) || originalGeneration != b.generation {
		t.Fatal("resizing changed storage")
	}
	if err = s.Collect(context.Background(), request, func(Manifest) error { return nil }, func(NativeChunk) error { return nil }); err == nil {
		t.Fatal("stale plan accepted")
	}
	request.IndexPeriod = "6h"
	request.PopulationTo = time.Date(2026, 11, 1, 6, 0, 0, 0, loc)
	pull()
	if !reflect.DeepEqual(before.Index, manifest.Index) || manifest.IndexPeriod != "6h" {
		t.Fatalf("resized %+v", manifest)
	}
	// Earlier 12-hour source-row coverage is reusable after resizing.
	request.Have = Coverage{Definition: before.Definition, Generation: before.Generation, Index: before.Index, Population: before.Population, TrieNodes: before.TrieNodes}
	pull()
	for _, c := range chunks {
		if c.Kind == "index" || c.Kind == "pac" {
			t.Fatal("resize duplicated cached bytes")
		}
	}
	request.At = at.AddDate(0, 0, -1)
	request.PopulationFrom = time.Time{}
	request.PopulationTo = time.Time{}
	pull()
	if len(manifest.Index) != 0 || manifest.Checkpoint == 0 {
		t.Fatal("known empty package")
	}
	setPolicy("5h")
	if err = s.ReloadPolicy(); err == nil || s.policy.Replication.hours() != 6 {
		t.Fatal("invalid sizing replaced policy")
	}
	// A larger configured provisional tail is respected.
	request.At = at
	request.IndexPeriod = "6h"
	end := time.Date(2026, 11, 1, 6, 0, 0, 0, loc)
	s.config.Now = func() time.Time { return end.Add(20 * time.Minute) }
	pull()
	if !manifest.Provisional {
		t.Fatal("ignored 30-minute tail")
	}
	s.config.Now = func() time.Time { return end.Add(30 * time.Minute) }
	pull()
	if manifest.Provisional {
		t.Fatal("closed package still provisional")
	}
}

// Compare the actual time directory to a full scan across many interleaved runs,
// including DST, year rollover, empty days, partial endpoints and exact edges.
func TestSemanticCandidatesMatchScan(t *testing.T) {
	for _, hours := range []int{1, 4, 6, 12, 24} {
		loc, _ := time.LoadLocation("America/New_York")
		base := time.Date(2026, 10, 31, 0, 0, 0, 0, loc)
		b := branchStore{}
		var times []time.Time
		for _, day := range []int{0, 4, 1, 63, 2, 0, 64} {
			for i := 0; i < 50; i++ {
				times = append(times, base.AddDate(0, 0, day).Add(time.Duration(i)*30*time.Minute))
			}
		}
		sort.SliceStable(times, func(i, j int) bool { return times[i].Before(times[j]) })
		for _, at := range times {
			hour := utcHour(at.UnixMilli())
			if len(b.runs) == 0 || b.runs[len(b.runs)-1].PAC.Placement.From != hour {
				start := b.tail() + 1
				b.rows = append(b.rows, indexHeader(hour)...)
				b.runs = append(b.runs, Run{Start: start, End: start, MinMS: at.UnixMilli(), PAC: &StoredPAC{Placement: PACPlacement{From: hour, To: hour + TimeHourMS, MemberStart: 4}}})
			}
			run := &b.runs[len(b.runs)-1]
			row := make([]byte, IndexRowSize)
			if e := packIndexID(row, [16]byte(uuid(at.UnixMilli(), uint32(run.End)).Bytes), hour); e != nil {
				t.Fatal(e)
			}
			b.rows = append(b.rows, row...)
			run.End++
			run.MaxMS = at.UnixMilli()
			copy(b.row(run.Start-1), indexHeader(hour, run.End-run.Start))
		}
		var err error
		b.time, err = buildTimeDirectory(b.runs, b.runs[0].PAC.Placement.From, b.runs[len(b.runs)-1].PAC.Placement.To, b.tail(), b.generation)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 200; i++ {
			from := base.Add(time.Duration(i*9) * time.Hour).UTC()
			to := from.Add(time.Duration(1+i%37) * time.Hour)
			if i%3 == 0 {
				from = from.Add(time.Nanosecond)
			}
			got := map[uint64]bool{}
			for _, c := range b.candidates(from, to, IndexRowSize) {
				for n := c.at; n < c.end; n++ {
					if got[n] {
						t.Fatal("duplicate row")
					}
					got[n] = true
				}
			}
			for n := b.nextRecord(0); n < b.tail(); n = b.nextRecord(n + 1) {
				ms := b.rowTime(n)
				want := ms >= timeBound(from) && ms < timeBound(to)
				if got[n] != want {
					t.Fatalf("hours %d query %d row %d", hours, i, n)
				}
			}
		}
	}
}

func TestSemanticSkippedMidnight(t *testing.T) {
	for _, c := range []struct {
		zone  string
		year  int
		month time.Month
		day   int
	}{
		{"America/Sao_Paulo", 2018, time.November, 4},
		{"Pacific/Apia", 2011, time.December, 30},
	} {
		loc, err := time.LoadLocation(c.zone)
		if err != nil {
			t.Fatal(err)
		}
		boundary := calendarBoundary(c.year, c.month, c.day, 0, loc)
		prior := time.Date(c.year, c.month, c.day-1, 12, 0, 0, 0, loc)
		_, end := semanticBounds(prior, 12, loc)
		start, next := semanticBounds(boundary, 12, loc)
		if !end.Equal(start) || !start.Equal(boundary) || !next.After(start) {
			t.Fatalf("gap %s: %v %v %v", c.zone, end, start, next)
		}
		tr := newTimeTrie(12)
		for i, at := range []time.Time{prior, boundary, next} {
			tr.append(at.UnixMilli(), uint64(i), loc)
		}
		var ranges []Span
		tr.visit(prior, next.Add(time.Millisecond), func(p *Period) {
			for _, r := range p.Ranges {
				ranges = append(ranges, Span{uint64(r.Start), r.end(3)})
			}
		})
		ranges, err = Normalize(ranges)
		if err != nil || !reflect.DeepEqual(ranges, []Span{{0, 3}}) {
			t.Fatalf("gap traversal %s: %v %v", c.zone, ranges, err)
		}
	}
}

func TestSemanticRepeatedMidnight(t *testing.T) {
	loc, err := time.LoadLocation("America/Havana")
	if err != nil {
		t.Fatal(err)
	}
	midnight := calendarBoundary(2026, time.November, 1, 0, loc)
	repeated := midnight.Add(time.Hour)
	if repeated.In(loc).Hour() != 0 {
		t.Fatal("fixture must repeat midnight")
	}
	a, b := semanticBounds(midnight, 12, loc)
	c, d := semanticBounds(repeated, 12, loc)
	if !a.Equal(c) || !b.Equal(d) || !a.Equal(midnight) || b.Sub(a) != 13*time.Hour {
		t.Fatalf("fold %v %v %v %v", a, b, c, d)
	}
}

func setTestPackagePeriod(t *testing.T, s *Store, period string) {
	t.Helper()
	raw := fmt.Sprintf("version=1\ntimezone=%q\n[defaults.replication]\nindex_period=%q\n", s.location.String(), period)
	if err := os.WriteFile(filepath.Join(s.root, "registry.cfg"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadPolicy(); err != nil {
		t.Fatal(err)
	}
}
