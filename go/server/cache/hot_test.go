package cache

import (
	"bytes"
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHotReplayStagingRestartPromotionAndExpiry(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	now := at.Add(4 * time.Minute)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }}
	p, e := OpenEndpoint(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { p.Close() }()
	frames := [][]byte{testFrame(t, l, at, 1, false), testFrame(t, l, at.Add(time.Minute), 2, true)}
	for i := len(frames) - 1; i >= 0; i-- {
		if _, e = p.api.Store.Append(Write, frames[i]); e != nil {
			t.Fatal(e)
		}
	}
	check := func(provisional bool, resident bool) {
		t.Helper()
		h, e := p.snapshotHistory(l.Definition.Identity, 1, testObject(42), at, at.Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		defer h.close()
		if len(h.locations) != 2 {
			t.Fatalf("history lost events: %d", len(h.locations))
		}
		for _, v := range h.locations {
			if (v.row == ProvisionalRow) != provisional {
				t.Fatal("wrong reference state")
			}
			if (v.raw != nil) != resident {
				t.Fatal("wrong resident state")
			}
			if resident && v.source.branch.pods != nil {
				t.Fatal("resident history opened payload file")
			}
			if !resident && !v.source.reader.point {
				t.Fatal("cold history used read-ahead")
			}
		}
		n := 0
		if e = h.visit(context.Background(), func(_ EventReference, raw []byte) error {
			if !sameSeedData(t, l, raw, frames[n]) {
				t.Fatal("history altered or unordered")
			}
			n++
			raw[len(raw)-1] ^= 1 // consumer buffer must not mutate resident bytes
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
	check(true, true)
	check(true, true)
	if len(p.api.Store.branches[Write].rows) != 0 {
		t.Fatal("staged events written to idx")
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	p, e = OpenEndpoint(cfg)
	if e != nil {
		t.Fatal(e)
	}
	check(true, true)
	now = now.Add(80 * time.Minute)
	if e = p.api.Store.Flush(); e != nil {
		t.Fatal(e)
	}
	check(false, true)
	if p.api.Store.branches[Write].live.count(0) != 0 {
		t.Fatal("promoted object tail retained")
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	p, e = OpenEndpoint(cfg)
	if e != nil {
		t.Fatal(e)
	}
	check(false, true) // restart warms finalized records
	now = now.Add(25 * time.Hour)
	check(false, false)
	if p.api.Store.branches[Write].live.count(1) != 0 {
		t.Fatal("expired resident window retained")
	}
}

func TestLiveViewPaginationSurvivesFinalization(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	now := at.Add(4 * time.Minute)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	originals := [][]byte{testFrame(t, l, at.Add(-2*time.Hour), 1, false), testFrame(t, l, at, 2, false), testFrame(t, l, at.Add(time.Minute), 3, false)}
	for _, f := range originals {
		if _, e = s.Append(Write, f); e != nil {
			t.Fatal(e)
		}
	}
	h := &HTTP{Store: s, Token: "test"}
	path := "/events?tap=" + url.QueryEscape(h.tap(Write)) + "&from=2026-10-18T10:00:00Z&to=2026-10-18T14:00:00Z&page=2&include=summary,page,statistics"
	get := func(cursor string) *httptest.ResponseRecorder {
		t.Helper()
		u := path
		if cursor != "" {
			u += "&after=" + url.QueryEscape(cursor)
		}
		r := httptest.NewRequest("GET", u, nil)
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays,adaptive-integers,view-buckets")
		w := httptest.NewRecorder()
		h.serveHTTP(w, r)
		return w
	}
	first := get("")
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	d := collectView(t, l, h, first.Body.Bytes())
	if d.result.Summary.Count != 3 || d.result.Summary.Status != "live-snapshot" || len(d.frames) != 2 || d.result.Next == "" {
		t.Fatalf("live summary/page: %+v", d.result)
	}
	if !sameSeedData(t, l, d.frames[0], originals[0]) || !sameSeedData(t, l, d.frames[1], originals[1]) {
		t.Fatal("mixed page order")
	}
	if d.result.Summary.Statistics == nil || d.result.Summary.Statistics.Duration.Count != 3 || d.result.Summary.Statistics.Duration.Average != 12 {
		t.Fatal("live statistics omitted pending values")
	}
	if d.result.Page[0].Provisional || !d.result.Page[1].Provisional {
		t.Fatal("incorrect provisional flags")
	}
	for _, b := range d.result.Buckets {
		for _, r := range b.Ranges {
			if d.result.Page[1].Row >= r.Start && d.result.Page[1].Row < r.End {
				t.Fatal("provisional row certified")
			}
		}
	}
	// Add an event inside the live time window, then promote all pending to disk.
	if _, e = s.Append(Write, testFrame(t, l, at.Add(30*time.Second), 4, false)); e != nil {
		t.Fatal(e)
	}
	now = now.Add(85 * time.Minute)
	second := get(d.result.Next)
	if second.Code != 200 {
		t.Fatal(second.Code, second.Body.String())
	}
	d2 := collectView(t, l, h, second.Body.Bytes())
	if dir := os.Getenv("PIL_LIVE_VIEW_FIXTURE_DIR"); dir != "" {
		if e = os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
		exportViewNative(t, dir, h, at.Add(-2*time.Hour), at.Add(-time.Hour))
		for name, raw := range map[string][]byte{"first.pil": first.Body.Bytes(), "next.pil": second.Body.Bytes()} {
			if e = os.WriteFile(filepath.Join(dir, name), raw, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
	if d2.result.Summary.Count != 3 || len(d2.frames) != 1 || d2.result.Next != "" || !sameSeedData(t, l, d2.frames[0], originals[2]) {
		t.Fatal("snapshot drifted after finalization")
	}
	fresh := get("")
	if fresh.Code != 200 {
		t.Fatal(fresh.Code, fresh.Body.String())
	}
	if collectView(t, l, h, fresh.Body.Bytes()).result.Summary.Count != 4 {
		t.Fatal("new query lost live insertion")
	}
	// Expired continuation must explicitly restart, not quietly omit live events.
	s.branches[Write].liveViews = nil
	expired := get(d.result.Next)
	if expired.Code != 409 {
		t.Fatalf("expired cursor: %d %s", expired.Code, expired.Body.String())
	}
}

func TestLiveHistoryObjectAndTimeBounds(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	now := at.Add(3 * time.Minute)
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	// Deliberately unordered, with two UUIDs in one millisecond and another object.
	for _, n := range []uint32{4, 2, 1, 3} {
		when := at
		if n == 4 {
			when = at.Add(time.Minute)
		}
		raw := testFrame(t, l, when, n, false)
		if n == 3 {
			var f Frame
			if e = l.Scan(raw, &f); e != nil {
				t.Fatal(e)
			}
			f.Values[l.Object] = testObject(99)
			raw, e = l.Encode(nil, f.Values)
			if e != nil {
				t.Fatal(e)
			}
		}
		if _, e = p.api.Store.Append(Write, raw); e != nil {
			t.Fatal(e)
		}
	}
	for _, promoted := range []bool{false, true} {
		if promoted {
			now = now.Add(time.Hour)
		}
		var got [][]byte
		e = p.History(context.Background(), l.Definition.Identity, 1, testObject(42), at, at.Add(time.Millisecond), func(_ EventReference, raw []byte) error { got = append(got, bytes.Clone(raw)); return nil })
		if e != nil || len(got) != 2 {
			t.Fatal("bounded history", len(got), e)
		}
		for i, raw := range got {
			if !sameSeedData(t, l, raw, testFrame(t, l, at, uint32(2-i), false)) {
				t.Fatal("cross-object or equal-time arrival ordering error")
			}
		}
	}
}
