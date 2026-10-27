package cache

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestTextQueryPaginationSummariesAndSnapshot(t *testing.T) {
	requireSearch(t)
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(90 * time.Minute)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	frames := map[uint32][]byte{}
	for i := uint32(1); i <= 7; i++ {
		f := testFrame(t, l, at.Add(time.Duration(i)*time.Minute), i, false)
		frames[i] = f
		if _, e = s.Append(Write, f); e != nil {
			t.Fatal(e)
		}
	}
	h := &HTTP{Store: s}
	base := "/events?tap=" + url.QueryEscape(h.tap(Write)) + "&from=2026-09-01T12:00:00Z&to=2026-09-01T14:00:00Z&page=1&include=summary,page,statistics"
	get := func(q QueryPlan, after string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(q)
		r := httptest.NewRequest("GET", base+"&query="+url.QueryEscape(string(raw))+"&after="+url.QueryEscape(after), nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays,adaptive-integers,view-buckets")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	q := QueryPlan{Filters: []QueryFilter{{"state", "==", QueryLiteral{"string", "authorized"}}, {"duration", ">=", QueryLiteral{"duration", "14us"}}}, Search: &QueryText{"REQUEST", "contains"}, Order: "desc"}
	first := get(q, "")
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	a := collectView(t, l, h, first.Body.Bytes())
	if a.result.Summary.Count != 2 || len(a.frames) != 1 || !bytes.Equal(a.frames[0], frames[7]) || len(a.result.Buckets) != 0 || a.result.Next == "" {
		t.Fatalf("bad filtered view %+v", a.result)
	}
	if a.result.Summary.Statistics.Duration.Count != 2 {
		t.Fatal("statistics must cover both pages")
	}
	next := get(q, a.result.Next)
	if next.Code != 200 {
		t.Fatal(next.Body.String())
	}
	b := collectView(t, l, h, next.Body.Bytes())
	if len(b.frames) != 1 || !bytes.Equal(b.frames[0], frames[4]) || b.result.Next != "" {
		t.Fatal("descending continuation")
	}
	q.Search.Text = "missing"
	if w := get(q, a.result.Next); w.Code != 409 {
		t.Fatal("cursor accepted changed predicate", w.Code)
	}
	empty := get(q, "")
	if empty.Code != 200 {
		t.Fatal(empty.Body.String())
	}
	if x := collectView(t, l, h, empty.Body.Bytes()); x.result.Summary.Count != 0 || len(x.frames) != 0 {
		t.Fatal("zero matches")
	}
	q = QueryPlan{Filters: []QueryFilter{{"id", "==", QueryLiteral{"string", "00000000-0000-7000-8000-00000000002a"}}}, Order: "asc"}
	obj := get(q, "")
	if obj.Code != 200 {
		t.Fatal(obj.Body.String())
	}
	if x := collectView(t, l, h, obj.Body.Bytes()); x.result.Summary.Count != 7 {
		t.Fatal("object candidates")
	}
	live := testFrame(t, l, now.Add(-time.Minute), 8, false)
	if _, e = s.Append(Write, live); e != nil {
		t.Fatal(e)
	}
	q = QueryPlan{Filters: []QueryFilter{{"revision_id", "==", QueryLiteral{"number", "8"}}}, Order: "desc"}
	w := get(q, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if x := collectView(t, l, h, w.Body.Bytes()); x.result.Summary.Count != 1 || !x.result.Page[0].Provisional {
		t.Fatal("staged matches missing")
	}
}
func TestQueryScalarPrecisionAndStates(t *testing.T) {
	if !compareQuery(QueryLiteral{"number", "18446744073709551615"}, QueryLiteral{"number", "18446744073709551614"}, ">") {
		t.Fatal("U64 precision")
	}
	if compareQuery(QueryLiteral{"absent", ""}, QueryLiteral{"null", ""}, "==") || !compareQuery(QueryLiteral{"null", ""}, QueryLiteral{"null", ""}, "==") {
		t.Fatal("absent/null")
	}
	if !compareQuery(QueryLiteral{"number", "-32768"}, QueryLiteral{"number", "-1"}, "<") || !compareQuery(QueryLiteral{"boolean", "false"}, QueryLiteral{"boolean", "false"}, "==") {
		t.Fatal("typed comparisons")
	}
	for _, value := range []string{"1e999999999", "0x12", "1/2", "NaN"} {
		q := QueryPlan{Filters: []QueryFilter{{"duration", "==", QueryLiteral{"number", value}}}, Order: "asc"}
		raw, _ := json.Marshal(q)
		if _, _, e := parseQueryPlan(string(raw)); e == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	if _, _, e := parseQueryPlan(`{"filters":[],"order":"asc","execute":"bad"}`); e == nil {
		t.Fatal("unknown query members")
	}
	l := testLayout(t, false)
	f := Frame{Values: make([]Value, len(l.Widths))}
	f.Values[3] = Uint(^uint64(0), 8)
	if !matchQuery(l, &f, &QueryPlan{Filters: []QueryFilter{{"duration", ">", QueryLiteral{"duration", "18446744073709551614us"}}}}) {
		t.Fatal("full width integer")
	}
	f.Values[5] = Value{Null, nil}
	if !matchQuery(l, &f, &QueryPlan{Filters: []QueryFilter{{"message", "==", QueryLiteral{"null", ""}}}}) {
		t.Fatal("null")
	}
}

func TestLevelAndAccessAreIndependentQueryFields(t *testing.T) {
	base := testLayout(t, false)
	d := base.Definition
	d.Fields = append(d.Fields, Field{Name: "access", Type: "STRING", Values: []string{"read", "write"}}, Field{Name: "level", Type: "STRING", Values: []string{"INFO", "ERROR", "DEBUG", "TRACE"}})
	d.Resource.Path = append(d.Resource.Path, "access", "level")
	l, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for access := 0; access < 2; access++ {
		for level := 0; level < 4; level++ {
			n := uint32(access*4 + level + 1)
			var f Frame
			if err = base.Scan(testFrame(t, base, at.Add(time.Duration(n)*time.Second), n, false), &f); err != nil {
				t.Fatal(err)
			}
			raw, e := l.Encode(nil, append(f.Values, Uint(uint64(access), 1), Uint(uint64(level), 1)))
			if e != nil {
				t.Fatal(e)
			}
			branch := []Branch{Read, Error, Debug, Trace}[level]
			if level == 0 && access == 1 {
				branch = Write
			}
			if _, e = s.Append(branch, raw); e != nil {
				t.Fatal(e)
			}
		}
	}
	h := &HTTP{Store: s}
	for _, access := range []string{"read", "write"} {
		for _, level := range []string{"INFO", "ERROR", "DEBUG", "TRACE"} {
			q := QueryPlan{Order: "asc", Filters: []QueryFilter{{"access", "==", QueryLiteral{"string", access}}, {"level", "==", QueryLiteral{"string", level}}}}
			raw, _ := json.Marshal(q)
			r := httptest.NewRequest("GET", "/events?tap=demo/payment/transactions.v1/*&from=2026-10-20T10:00:00Z&to=2026-10-20T11:00:00Z&include=summary,page&query="+url.QueryEscape(string(raw)), nil)
			r.Header.Set("Accept", "application/pil")
			r.Header.Set("PIL-Version", "2")
			r.Header.Set("PIL-Features", "pac-arrays,adaptive-integers,view-buckets")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			result := collectView(t, l, h, w.Body.Bytes())
			if result.result.Summary.Count != 1 || len(result.frames) != 1 {
				t.Fatalf("%s/%s: %+v", access, level, result.result)
			}
		}
	}
	// Diagnostic records share object ID 42, but only the actual write is replayable.
	count := 0
	if err = s.SearchLive(t.Context(), Query{Branch: Write, From: at, To: at.Add(time.Hour), Equals: map[string]Value{"id": testObject(42)}}, func([]byte) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("diagnostics entered replay candidates: %d", count)
	}
}
