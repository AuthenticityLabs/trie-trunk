package cache

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestDurationQueryUnitsAndBoundaries(t *testing.T) {
	l := testLayout(t, false)
	f := Frame{Values: make([]Value, len(l.Widths))}
	f.Values[l.Duration] = Uint(10000000, 8)
	for _, tc := range []struct {
		op, kind, value string
		want            bool
	}{
		{"<=", "number", "10000", true}, {"<", "number", "10000", false},
		{">=", "number", "10000", true}, {">", "number", "10000", false},
		{"==", "number", "10000", true}, {"!=", "number", "10000", false},
		{"<=", "duration", "10000us", false}, {"==", "duration", "10s", true},
		{"==", "duration", "1e4ms", true}, {"==", "duration", "10000000µs", true},
		{"==", "duration", "10000000μs", true}, {"==", "duration", "10000000000ns", true},
		{"<", "duration", "10000.0001ms", true}, {">", "duration", "9999999999ns", true},
	} {
		q := QueryPlan{Filters: []QueryFilter{{"duration", tc.op, QueryLiteral{tc.kind, tc.value}}}, Order: "asc"}
		raw, _ := json.Marshal(q)
		parsed, _, err := parseQueryPlan(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := matchQuery(l, &f, parsed); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
	// Units follow the role even if the field has a different name.
	l.Definition.Fields[l.Duration].Name = "elapsed"
	if !matchQuery(l, &f, &QueryPlan{Filters: []QueryFilter{{"elapsed", "==", QueryLiteral{"number", "10000"}}}}) {
		t.Fatal("custom duration role")
	}
	f.Values[2] = Uint(10000, 2)
	if !matchQuery(l, &f, &QueryPlan{Filters: []QueryFilter{{"revision_id", "==", QueryLiteral{"number", "10000"}}}}) {
		t.Fatal("unrelated numeric field was scaled")
	}
	for _, value := range []string{"1e999ms", "NaNms", "10minutes", "0x10us"} {
		raw, _ := json.Marshal(QueryPlan{Filters: []QueryFilter{{"elapsed", "==", QueryLiteral{"duration", value}}}, Order: "asc"})
		if _, _, err := parseQueryPlan(string(raw)); err == nil {
			t.Fatal("accepted", value)
		}
	}
	sources := []*viewSource{{api: &HTTP{Store: &Store{layout: l}}}}
	if err := validateQueryPlan(&QueryPlan{Filters: []QueryFilter{{"revision_id", "==", QueryLiteral{"duration", "10ms"}}}}, sources); err == nil {
		t.Fatal("units accepted on non-duration field")
	}
}

func TestDurationUnitsAgreeInIndexAndScan(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	off, on := false, true
	s.policy.SearchIndex = &off
	for i, us := range []uint64{0, 9999, 10000, 9999999, 10000000, 10000001} {
		var f Frame
		if err = l.Scan(testFrame(t, l, at.Add(time.Duration(i)*time.Minute), uint32(i+1), false), &f); err != nil {
			t.Fatal(err)
		}
		f.Values[l.Duration] = Uint(us, 8)
		raw, e := l.Encode(nil, f.Values)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(Write, raw); e != nil {
			t.Fatal(e)
		}
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		op, kind, value string
		count           int
	}{
		{"<=", "number", "10000", 5}, {"<=", "duration", "10000ms", 5},
		{"<=", "duration", "10s", 5}, {"<=", "duration", "10000us", 3},
		{"<=", "duration", "9999999999ns", 4},
		{">", "number", "10000", 1}, {">", "duration", "10000ms", 1},
		{">", "duration", "10s", 1}, {">", "duration", "10000us", 3},
	} {
		q := QueryPlan{Filters: []QueryFilter{{"duration", tc.op, QueryLiteral{tc.kind, tc.value}}}, Order: "asc"}
		s.policy.SearchIndex = &off
		scanned := searchRows(t, s, at, now, q)
		s.policy.SearchIndex = &on
		indexed := searchRows(t, s, at, now, q)
		if len(scanned) != tc.count || !reflect.DeepEqual(scanned, indexed) {
			t.Fatalf("%+v: scan %v index %v", tc, scanned, indexed)
		}
	}
}
