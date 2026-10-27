package cache

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func requireSearch(t testing.TB) { t.Helper() }
func searchRows(t testing.TB, s *Store, from, to time.Time, q QueryPlan) []uint64 {
	t.Helper()
	v, e := s.viewSnapshot(Write, from, to, nil, nil, true)
	if e != nil {
		t.Fatal(e)
	}
	defer v.close()
	v.api = &HTTP{Store: s}
	if e = v.filterQuery(context.Background(), &q); e != nil {
		t.Fatal(e)
	}
	var rows []uint64
	for _, span := range v.ranges {
		for row := span.at; row < span.end; row++ {
			rows = append(rows, row)
		}
	}
	return rows
}
func TestTantivyIndexedAndStagedSearch(t *testing.T) {
	requireSearch(t)
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.Close() }()
	// Disable background work for deterministic cold-build and scan comparisons.
	off := false
	s.policy.SearchIndex = &off
	texts := []string{"connection timeout at gateway", "connection completed successfully", "payment authorization denied", "timeout connection reversed"}
	for i, text := range texts {
		var f Frame
		raw := testFrame(t, l, at.Add(time.Duration(i)*time.Minute), uint32(i+1), false)
		if e = l.Scan(raw, &f); e != nil {
			t.Fatal(e)
		}
		f.Values[5] = Text(text)
		if i == 2 {
			f.Values[6] = Uint(^uint64(0), 8)
		}
		raw, e = l.Encode(nil, f.Values)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(Write, raw); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	on := true
	s.policy.SearchIndex = &on
	check := func(text, mode string, want []uint64) {
		t.Helper()
		got := searchRows(t, s, at, now, QueryPlan{Search: &QueryText{text, mode}, Order: "asc"})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s %q: %v != %v", mode, text, got, want)
		}
	}
	check("connection timeout", "terms", []uint64{1, 4})
	check("connection timeout", "phrase", []uint64{1})
	check("connection time", "prefix", []uint64{1})
	check("timout", "fuzzy", []uint64{1, 4})
	check("authorized", "terms", []uint64{1, 4})
	check("TIME", "contains", []uint64{1, 4})
	q := QueryPlan{Filters: []QueryFilter{{"amount", "==", QueryLiteral{"number", "18446744073709551615"}}}, Order: "asc"}
	if got := searchRows(t, s, at, now, q); !reflect.DeepEqual(got, []uint64{3}) {
		t.Fatal("U64 precision", got)
	}
	q = QueryPlan{Filters: []QueryFilter{{"state", "==", QueryLiteral{"string", "authorized"}}, {"duration", ">=", QueryLiteral{"duration", "13us"}}}, Search: &QueryText{"timeout", "terms"}, Order: "desc"}
	if got := searchRows(t, s, at, now, q); !reflect.DeepEqual(got, []uint64{4}) {
		t.Fatal("intersection", got)
	}
	// A staged event is searchable immediately with the same analyzer, not only
	// after the hourly PAC seals or the index background worker commits.
	if _, e = s.Append(Write, testFrame(t, l, now.Add(-time.Minute), 5, false)); e != nil {
		t.Fatal(e)
	}
	check("request completed", "phrase", []uint64{5})
	// Close and reopen the source; persisted index checkpoint and staged recovery.
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(c)
	if e != nil {
		t.Fatal(e)
	}
	s.policy.SearchIndex = &on
	check("connection timeout", "terms", []uint64{1, 4})
	check("request completed", "phrase", []uint64{5})
	// Retention changes physical file bases; logical row references remain valid.
	now = now.Add(3 * time.Hour)
	s.policy.SearchIndex = &off
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	s.policy.SearchIndex = &on
	s.policy.Retention.Write = "4h"
	if e = s.ApplyRetention(); e != nil {
		t.Fatal(e)
	}
	if got := searchRows(t, s, at, now, QueryPlan{Search: &QueryText{"connection", "terms"}, Order: "asc"}); len(got) != 0 {
		t.Fatal("retired text resurfaced", got)
	}
	if got := searchRows(t, s, at, now, QueryPlan{Search: &QueryText{"request completed", "phrase"}, Order: "asc"}); !reflect.DeepEqual(got, []uint64{6}) {
		t.Fatalf("retention moved row %v; runs=%+v origin=%d leaf=%+v", got, s.branches[Write].runs, s.branches[Write].time.origin(), s.branches[Write].time.raw[:64])
	}
}

func TestTantivySnapshotHighWaterAndLiteralSemantics(t *testing.T) {
	requireSearch(t)
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	off := false
	s.policy.SearchIndex = &off
	if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	v, e := s.viewSnapshot(Write, at, now, nil, nil, true)
	if e != nil {
		t.Fatal(e)
	}
	defer v.close()
	v.api = &HTTP{Store: s}
	on := true
	s.policy.SearchIndex = &on
	// Index newer rows first, then query an older pinned snapshot without rolling
	// back its checkpoint or letting those rows leak into the page.
	now = now.Add(2 * time.Hour)
	s.policy.SearchIndex = &off
	if _, e = s.Append(Write, testFrame(t, l, at.Add(2*time.Hour), 2, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	s.policy.SearchIndex = &on
	q := QueryPlan{Search: &QueryText{"request", "terms"}, Order: "asc"}
	if got := searchRows(t, s, at, now, q); len(got) != 2 {
		t.Fatal(got)
	}
	if e = v.filterQuery(context.Background(), &q); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(v.ranges, []cursor{{1, 2}}) {
		t.Fatal(v.ranges)
	}
	// A fractional bound must not be rounded by the index into a false negative.
	for _, value := range []string{"11.5", "1.15e1", "18446744073709551616"} {
		q = QueryPlan{Filters: []QueryFilter{{"duration", ">", QueryLiteral{"duration", value + "us"}}}, Order: "asc"}
		indexed := searchRows(t, s, at, now, q)
		s.policy.SearchIndex = &off
		scanned := searchRows(t, s, at, now, q)
		s.policy.SearchIndex = &on
		if !reflect.DeepEqual(indexed, scanned) {
			t.Fatal(value, indexed, scanned)
		}
	}
}

func TestTantivyTypedValuesAndLiteralDictionaryEscapes(t *testing.T) {
	requireSearch(t)
	base := testLayout(t, false)
	d := base.Definition
	d.Fields = append(d.Fields, Field{Name: "ratio", Type: "F32"}, Field{Name: "signed", Type: "I64"}, Field{Name: "enabled", Type: "BOOL"}, Field{Name: "tags", Type: "ARRAY<STRING>"}, Field{Name: "@fields", Type: "CBOR"})
	l, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	off, on := false, true
	s.policy.SearchIndex = &off
	for i := 0; i < 3; i++ {
		var f Frame
		if e = base.Scan(testFrame(t, base, at.Add(time.Duration(i)*time.Minute), uint32(i+1), false), &f); e != nil {
			t.Fatal(e)
		}
		f.Values[4] = Text([]string{"authorized", "unknown mapped escape", "pending"}[i])
		var ratio [4]byte
		order.PutUint32(ratio[:], math.Float32bits(float32(i)+0.1))
		var signed [8]byte
		order.PutUint64(signed[:], uint64(int64(i)-2))
		tags := extended(nil, 2)
		tags = extended(tags, 10)
		tags = extended(tags, 17)
		tags = append(tags, []byte("connectiontimeout")...)
		extension, _ := encoder.Marshal(map[string]any{"nested": []any{"specific extension text", nil}})
		f.Values = append(f.Values, Value{Direct, ratio[:]}, Value{Direct, signed[:]}, Uint(uint64(i%2), 1), Value{Offset, tags}, Value{Offset, extension})
		if i == 2 {
			f.Values[7] = Value{Null, nil}
			f.Values[9] = Value{}
		}
		raw, e := l.Encode(nil, f.Values)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(Write, raw); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	for _, filter := range []QueryFilter{{"ratio", "==", QueryLiteral{"number", "0.1"}}, {"ratio", ">", QueryLiteral{"number", "0.10000000000001"}}, {"ratio", ">=", QueryLiteral{"number", "1.1"}}, {"signed", "<", QueryLiteral{"number", "-1"}}, {"enabled", "==", QueryLiteral{"boolean", "false"}}, {"state", "==", QueryLiteral{"string", "authorized"}}, {"state", "==", QueryLiteral{"string", "unknown mapped escape"}}, {"ratio", "==", QueryLiteral{"null", ""}}, {"enabled", "!=", QueryLiteral{"boolean", "true"}}} {
		q := QueryPlan{Filters: []QueryFilter{filter}, Order: "asc"}
		s.policy.SearchIndex = &off
		want := searchRows(t, s, at, now, q)
		s.policy.SearchIndex = &on
		got := searchRows(t, s, at, now, q)
		if !reflect.DeepEqual(got, want) {
			t.Fatal(filter, got, want)
		}
	}
	for _, search := range []QueryText{{"connection timeout", "terms"}, {"specific extension", "phrase"}, {"unknown mapped escape", "phrase"}} {
		q := QueryPlan{Search: &search, Order: "asc"}
		if got := searchRows(t, s, at, now, q); len(got) == 0 {
			t.Fatal("array/extension/dictionary text missing", search)
		}
	}
	if got := searchRows(t, s, at, now, QueryPlan{Search: &QueryText{"connection timeout", "phrase"}, Order: "asc"}); len(got) != 0 {
		t.Fatal("phrase crossed array elements", got)
	}
}

func TestTantivyHTTPPaginationAndFullRangeSummary(t *testing.T) {
	requireSearch(t)
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(2 * time.Hour)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	off, on := false, true
	s.policy.SearchIndex = &off
	for i := 1; i <= 7; i++ {
		if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Duration(i)*time.Minute), uint32(i), false)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	s.policy.SearchIndex = &on
	h := &HTTP{Store: s}
	raw, _ := json.Marshal(QueryPlan{Search: &QueryText{"request completed", "phrase"}, Order: "desc"})
	base := "/events?tap=" + url.QueryEscape(h.tap(Write)) + "&from=2026-09-01T12:00:00Z&to=2026-09-01T14:00:00Z&page=2&include=summary,page,statistics&query=" + url.QueryEscape(string(raw))
	after := ""
	seen := map[string]bool{}
	for page := 0; page < 4; page++ {
		r := httptest.NewRequest("GET", base+"&after="+url.QueryEscape(after), nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays,adaptive-integers,view-buckets")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		v := decodeView(t, l, w.Body.Bytes())
		if v.result.Summary.Count != 7 || v.result.Summary.Statistics.Duration.Count != 7 || len(v.result.Buckets) != 0 {
			t.Fatal("page constrained full-range statistics or certified coverage", v.result)
		}
		for _, ref := range v.result.Page {
			if seen[ref.Key] {
				t.Fatal("duplicate page row")
			}
			seen[ref.Key] = true
		}
		after = v.result.Next
	}
	if len(seen) != 7 || after != "" {
		t.Fatal(seen, after)
	}
}

func TestTantivyColdCatchupWithoutHTTP(t *testing.T) {
	requireSearch(t)
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(72 * time.Hour) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	off := false
	s.policy.SearchIndex = &off
	if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	v, e := s.viewSnapshot(Write, at, at.Add(time.Hour), nil, nil, true)
	if e != nil {
		t.Fatal(e)
	}
	defer v.close()
	if v.resident(1) != nil {
		t.Fatal("fixture must read cold PAC")
	}
	s.searchGate(Write).Lock()
	reply, e := v.catchupSearch(context.Background(), v.searchRequest())
	s.searchGate(Write).Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if reply.Tail != 2 {
		t.Fatal(reply)
	}
}

func TestIndexSnapshotCannotFinalizeAnUncollectedHour(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(75 * time.Minute)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	off := false
	s.policy.SearchIndex = &off
	if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	before := s.branches[Write].finalizedBefore
	now = now.Add(time.Hour)
	v, e := s.indexSnapshot(Write)
	if e != nil {
		t.Fatal(e)
	}
	v.close()
	if s.branches[Write].finalizedBefore != before {
		t.Fatal("index reader advanced ingestion watermark")
	}
	if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Hour), 2, false)); e != nil {
		t.Fatal("indexing rejected a previously acceptable event", e)
	}
}
