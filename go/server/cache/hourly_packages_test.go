package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHourlyDefaultAndCalendarDays(t *testing.T) {
	for _, tc := range []struct {
		zone       string
		month      time.Month
		day, count int
	}{
		{"America/New_York", time.October, 17, 24},
		{"America/New_York", time.March, 8, 23},
		{"America/New_York", time.November, 1, 25},
		{"Australia/Lord_Howe", time.April, 5, 25},
		{"Australia/Lord_Howe", time.October, 4, 24},
		{"Asia/Kathmandu", time.October, 17, 24},
	} {
		t.Run(tc.zone+tc.month.String(), func(t *testing.T) {
			def := testLayout(t, false).Definition
			def.Resource.Timezone = tc.zone
			l, err := Compile(def)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			raw, err := os.ReadFile(filepath.Join(s.root, "registry.cfg"))
			if err != nil {
				t.Fatal(err)
			}
			// Defaults are implicit in new files so an ancestor policy can
			// override them. The resolved calendar behavior remains hourly.
			config, err := ParseConfiguration(raw)
			if err != nil || config.Defaults.Replication.hours() != 1 || s.policy.Replication.hours() != 1 || newTimeTrie().hours != 1 {
				t.Fatal("default is not hourly")
			}
			start := time.Date(2026, tc.month, tc.day, 0, 0, 0, 0, s.location)
			end := start.AddDate(0, 0, 1)
			windows, err := s.PlanPackages(start.UTC(), end.UTC())
			if err != nil || len(windows) != 24 {
				t.Fatalf("%d windows, want %d: %v", len(windows), 24, err)
			}
			at := start
			for _, w := range windows {
				if !w.From.Equal(at) || !w.To.After(w.From) || w.IndexPeriod != "1h" {
					t.Fatalf("gap or overlap %+v", w)
				}
				// Every instant resolves to this precise occurrence, including repeated hours.
				for probe := w.From; probe.Before(w.To); probe = probe.Add(time.Minute) {
					a, b := semanticBounds(probe, 1, s.location)
					if !a.Equal(w.From) || !b.Equal(w.To) {
						t.Fatalf("inconsistent bounds at %v: %v %v", probe, a, b)
					}
				}
				at = w.To
			}
			if !at.Equal(end) {
				t.Fatal("day incomplete")
			}
			// An exclusive hour edge must not pull the following package.
			w := windows[4]
			exact, err := s.PlanPackages(w.From.Add(time.Minute), w.To)
			if err != nil || len(exact) != 1 || !exact[0].From.Equal(w.From) {
				t.Fatal("exclusive edge widened")
			}
		})
	}
	if (ReplicationPolicy{}).hours() != 1 {
		t.Fatal("omitted period not hourly")
	}
}

func TestHourlyRepeatedHourReplication(t *testing.T) {
	l := testLayout(t, false)
	loc, _ := time.LoadLocation("America/New_York")
	midnight := time.Date(2026, time.November, 1, 0, 0, 0, 0, loc)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "owner", Layout: l, Now: func() time.Time { return midnight.AddDate(0, 0, 3) }}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	first := midnight.Add(time.Hour)
	second := first.Add(time.Hour)
	if first.Hour() != 1 || second.Hour() != 1 {
		t.Fatal("expected repeated 01:00")
	}
	// Arrival order may differ while both occurrences remain staged.
	for i, at := range []time.Time{second, first} {
		if _, err = s.Append(Write, testFrame(t, l, at.Add(10*time.Minute), uint32(i+1), false)); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		for i, at := range []time.Time{first, second} {
			var m Manifest
			err = s.Collect(context.Background(), CollectionRequest{Branch: Write, At: at, IndexPeriod: "1h", DataFrom: at, DataTo: at.Add(time.Hour)}, func(v Manifest) error { m = v; return nil }, func(NativeChunk) error { return nil })
			if err != nil || len(m.Index) != 1 || m.Index[0] != (Span{uint64(i*2 + 1), uint64(i*2 + 2)}) || !m.From.Equal(at) || !m.To.Equal(at.Add(time.Hour)) {
				t.Fatalf("fold %d: %+v %v", i, m, err)
			}
			if got := collect(t, s, Write, at, at.Add(time.Hour)); len(got) != 1 {
				t.Fatalf("fold search %d", len(got))
			}
		}
	}
	check()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	check()
	// The repeated local hour is two ordinary, independently addressed UTC hours.
	a, ok := s.branches[Write].time.leaf(first.UnixMilli())
	z, ok2 := s.branches[Write].time.leaf(second.UnixMilli())
	if !ok || !ok2 || a.Start == z.Start || a.Segments != 1 || z.Segments != 1 {
		t.Fatal("UTC hours collapsed")
	}
}
