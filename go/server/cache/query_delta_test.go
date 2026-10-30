package cache

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryDeltaLiveCheckpointLateArrivalAndSeal(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	now := at.Add(10 * time.Minute)
	endpoint, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	api, s := endpoint.api, endpoint.api.Store
	fixtureDir := os.Getenv("TRUNK_QUERY_DELTA_FIXTURES")
	fixtureNumber := 0
	if fixtureDir != "" {
		if err := os.MkdirAll(fixtureDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixtureDir, "definition.rx"), l.Bytes, 0644); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent := func(ms time.Time, n uint32) {
		t.Helper()
		if _, err := s.Append(Write, testFrame(t, l, ms, n, false)); err != nil {
			t.Fatal(err)
		}
	}
	pull := func(q queryDeltaRequest) queryDeltaResponse {
		t.Helper()
		raw, err := encoder.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/query-delta?tap="+api.tap(Write), bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/pil")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("delta %d: %s", w.Code, w.Body.String())
		}
		if fixtureDir != "" {
			if err := os.WriteFile(filepath.Join(fixtureDir, fmt.Sprintf("%d.pil", fixtureNumber)), w.Body.Bytes(), 0644); err != nil {
				t.Fatal(err)
			}
			fixtureNumber++
		}
		var response queryDeltaResponse
		if err := decoder.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Branches) != 1 || response.Branches[0].Tap != api.tap(Write) {
			t.Fatalf("wrong branches: %+v", response)
		}
		return response
	}
	appendEvent(at.Add(time.Minute), 1)
	initial := queryDeltaRequest{From: at.UnixMilli(), To: now.UnixMilli(), Now: true}
	first := pull(initial)
	b := first.Branches[0]
	if len(b.Frames) != 1 || len(s.branches[Write].rows) != 0 {
		t.Fatal("missing live event, or refresh sealed events")
	}
	cp := at.Add(time.Minute).UnixMilli()
	next := initial
	next.Epoch = first.Epoch
	next.Checkpoint = &cp
	next.PreviousTo = first.To
	next.Known = []QueryReceipt{b.QueryReceipt}
	now = now.Add(time.Second)
	unchanged := pull(next)
	if len(unchanged.Branches[0].Frames) != 0 || len(unchanged.Branches[0].TimeTrie) != 0 {
		t.Fatal("unchanged refresh transferred bytes")
	}
	appendEvent(at.Add(11*time.Minute), 2)
	now = at.Add(12 * time.Minute)
	second := pull(next)
	if len(second.Branches[0].Frames) != 2 || second.Branches[0].ReplaceFrom != cp-1 {
		t.Fatal("checkpoint overlap not honored")
	}
	cp = at.Add(11 * time.Minute).UnixMilli()
	next.PreviousTo = second.To
	next.Known = []QueryReceipt{second.Branches[0].QueryReceipt}
	appendEvent(at.Add(30*time.Second), 3)
	late := pull(next)
	if len(late.Branches[0].Frames) != 3 || late.Branches[0].ReplaceFrom != at.Add(30*time.Second).UnixMilli() {
		t.Fatalf("late arrival omitted: %+v", late.Branches[0])
	}
	next.PreviousTo = late.To
	next.Known = []QueryReceipt{late.Branches[0].QueryReceipt}
	// Restart invalidates receipt knowledge even when generation and checkpoint match.
	next.Epoch = "previous-process"
	restarted := pull(next)
	if len(restarted.Branches[0].Frames) != 3 {
		t.Fatal("restart trusted old receipt")
	}
	next.Epoch = first.Epoch
	now = at.Add(2 * time.Hour)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	sealed := pull(next)
	if len(sealed.Branches[0].Frames) != 0 || len(sealed.Branches[0].TimeTrie) == 0 || sealed.Branches[0].Sealed <= at.UnixMilli() {
		t.Fatal("seal transition not advertised")
	}
	if fixtureDir != "" {
		req := httptest.NewRequest("GET", "/collect?tap="+api.tap(Write)+"&from="+at.Format(time.RFC3339)+"&to="+now.Format(time.RFC3339), nil)
		req.Header.Set("Accept", "application/pil")
		req.Header.Set("PIL-Version", "2")
		req.Header.Set("PIL-Features", "native-ranges,adaptive-integers")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if err := os.WriteFile(filepath.Join(fixtureDir, "sealed.pil"), w.Body.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing on this path silently enables predicate execution or disk writes.
}

func TestQueryDeltaAuthorizationAndBounds(t *testing.T) {
	l := testLayout(t, false)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ep, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	ep.api.Token = "private"
	req := httptest.NewRequest("POST", "/query-delta", nil)
	w := httptest.NewRecorder()
	ep.api.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, q := range []queryDeltaRequest{{From: now.UnixMilli(), To: now.UnixMilli()}, {From: -1, To: now.UnixMilli()}} {
		raw, _ := encoder.Marshal(q)
		req = httptest.NewRequest("POST", "/query-delta", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer private")
		req.Header.Set("Content-Type", "application/pil")
		w = httptest.NewRecorder()
		ep.api.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("invalid range accepted: %d", w.Code)
		}
	}
}

func TestQueryDeltaEmptyBranchThenBackdatedFirstEvent(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	now := at.Add(10 * time.Minute)
	endpoint, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	request := func(q queryDeltaRequest) queryDeltaResponse {
		body, e := encoder.Marshal(q)
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest("POST", "/query-delta?tap="+endpoint.api.tap(Write), bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/pil")
		w := httptest.NewRecorder()
		endpoint.api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var d queryDeltaResponse
		if e := decoder.Unmarshal(w.Body.Bytes(), &d); e != nil {
			t.Fatal(e)
		}
		return d
	}
	q := queryDeltaRequest{From: at.Add(-time.Hour).UnixMilli(), To: now.UnixMilli(), Now: true}
	empty := request(q)
	if len(empty.Branches[0].Frames) != 0 {
		t.Fatal("expected empty")
	}
	cp := empty.To - 1
	q.Checkpoint = &cp
	q.Epoch = empty.Epoch
	q.PreviousTo = empty.To
	q.Known = []QueryReceipt{empty.Branches[0].QueryReceipt}
	stamp := now.Add(-20 * time.Minute)
	if _, e := endpoint.api.Store.Append(Write, testFrame(t, l, stamp, 1, false)); e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Minute)
	delta := request(q)
	if len(delta.Branches[0].Frames) != 1 || delta.Branches[0].ReplaceFrom > stamp.UnixMilli() {
		t.Fatal("missed first event behind empty checkpoint", delta.Branches[0])
	}
}

func TestQueryDeltaEndpointIncludesSelectedDefinitions(t *testing.T) {
	l := testLayout(t, false)
	now := time.Now()
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	other, e := GenericDefinition()
	if e != nil {
		t.Fatal(e)
	}
	if e = p.install(other); e != nil {
		t.Fatal(e)
	}
	var targets []QueryReceipt
	for _, api := range p.sources() {
		targets = append(targets, QueryReceipt{Tap: api.tap(Write), Definition: fmtDigestNative(api.Store.layout.Digest)})
	}
	raw, e := encoder.Marshal(queryDeltaRequest{From: now.Add(-time.Hour).UnixMilli(), To: now.UnixMilli(), Now: true, Targets: targets})
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "/query-delta", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/pil")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var d queryDeltaResponse
	if e := decoder.Unmarshal(w.Body.Bytes(), &d); e != nil {
		t.Fatal(e)
	}
	if len(d.Branches) != 2 {
		t.Fatalf("lost registered definitions: %d", len(d.Branches))
	}
}
