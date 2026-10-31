package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestCoordinatorRejectionsIncludeEveryMinor(t *testing.T) {
	base := testLayout(t, false)
	d := base.Definition
	d.Version++
	d.Resource.Minor++
	next, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: base, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = p.Prefer(next); e != nil {
		t.Fatal(e)
	}
	for _, l := range []*Layout{base, next} {
		s := p.apis[fmtDigest(l)].Store
		if _, e = s.Append(Write, testFrame(t, l, at, 1, false)); e != nil {
			t.Fatal(e)
		}
		if e = s.Flush(); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Minute), 2, false)); e == nil {
			t.Fatal("late event accepted")
		}
	}
	for _, digest := range []string{"", fmtDigest(base)} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", "/rejections?definition="+digest, nil))
		if w.Code != 200 || w.Result().Trailer.Get("Trunk-Status") != "ok" {
			t.Fatal(w.Code, w.Body.String())
		}
		lines := bytes.Split(bytes.TrimSpace(w.Body.Bytes()), []byte{'\n'})
		want := 2
		if digest != "" {
			want = 1
		}
		if len(lines) != want {
			t.Fatal("rejection count", len(lines), want)
		}
		for _, line := range lines {
			var record struct {
				Definition string         `json:"definition_version"`
				Source     string         `json:"source"`
				Event      map[string]any `json:"event"`
			}
			if e = json.Unmarshal(line, &record); e != nil {
				t.Fatal(e)
			}
			if record.Source == "" || record.Event["message"] != "request completed" || digest != "" && record.Definition != digest {
				t.Fatal(string(line))
			}
		}
	}
}

func TestCoordinatorMinorsRecoveryReplayAndRetention(t *testing.T) {
	base := testLayout(t, false)
	d := base.Definition
	d.Version++
	d.Resource.Minor++
	next, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	d = base.Definition
	d.Identity = "customer/accounts"
	d.Resource.Service = "customer"
	d.Resource.Resource = "accounts"
	other, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	now := at.Add(time.Minute)
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Layout: base, ObjectIndex: true, Now: func() time.Time { return now }}
	p, err := OpenEndpoint(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.Close() }()
	old := p.apis[fmtDigest(base)].Store
	if _, err := old.Append(Write, testFrame(t, base, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	if err := p.Prefer(next); err != nil {
		t.Fatal(err)
	}
	current := p.apis[fmtDigest(next)].Store
	if _, err := current.Append(Write, testFrame(t, next, at.Add(time.Second), 2, true)); err != nil {
		t.Fatal(err)
	}
	// Interleaved minors must share postings/state, not overwrite each other's
	// source watermarks or invalidation sequences.
	if old.objects != current.objects || old.state != current.state {
		t.Fatal("journals not coordinated")
	}
	if _, err := old.Append(Error, testFrame(t, base, at.Add(2*time.Second), 3, false)); err != nil {
		t.Fatal(err)
	}
	before, err := current.Token(testObject(42))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.Append(Error, testFrame(t, next, at.Add(3*time.Second), 4, false)); err != nil {
		t.Fatal(err)
	}
	valid, err := old.StillCurrent(testObject(42), before)
	if err != nil || valid {
		t.Fatal("invalidation lost across minors", err)
	}
	if err := p.Prefer(other); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(c); err == nil {
		t.Fatal("second writer obtained lease")
	}
	// Simulate restart while both minors still have durable unfinalized events.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	c.Layout = nil
	now = now.Add(2 * time.Hour)
	p, err = OpenEndpoint(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, api := range p.sources() {
		if err := api.Store.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	var refs []EventReference
	var seen [][]byte
	err = p.History(context.Background(), "payment/transactions", 1, testObject(42), at.Add(-time.Hour), now, func(ref EventReference, raw []byte) error {
		refs = append(refs, ref)
		seen = append(seen, append([]byte(nil), raw...))
		return nil
	})
	if err != nil || len(refs) != 2 {
		t.Fatal("cross-minor history", len(refs), err)
	}
	if refs[0].Definition != base.Digest || refs[1].Definition != next.Digest {
		t.Fatal("event order or definition binding lost")
	}
	for i, ref := range refs {
		raw, e := p.Resolve(ref)
		if e != nil || !bytes.Equal(raw, seen[i]) {
			t.Fatal("reference resolution", e)
		}
	}
	// Collection listing deduplicates minors; collection requests include both
	// immutable maps/PACs in the same TPIL stream.
	r := httptest.NewRequest("GET", "/collect?tap="+url.QueryEscape("demo/payment/transactions.v1/write")+"&from="+url.QueryEscape(at.Add(-time.Hour).Format(time.RFC3339))+"&to="+url.QueryEscape(now.Format(time.RFC3339)), nil)
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("PIL-Features", "native-ranges")
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("Accept", "application/pil")
	w := httptest.NewRecorder()
	p.serveHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, chunks := readNativeTest(t, w.Body.Bytes())
	pacs := 0
	for _, c := range chunks {
		if c.Kind == "pac" {
			pacs++
		}
	}
	if pacs != 2 {
		t.Fatal("collection omitted historical PAC", pacs)
	}
	raw := ungzipTest(t, w.Body.Bytes())
	for _, l := range []*Layout{base, next} {
		if !bytes.Contains(raw, l.Bytes) {
			t.Fatal("missing source map")
		}
	}
	// Retention in one minor updates the catalog for every peer and rejects stale
	// physical references, without touching another minor's retained history.
	old = p.apis[fmtDigest(base)].Store
	old.policy.Retention.Write = "30m"
	if err := old.ApplyRetention(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(refs[0]); err == nil {
		t.Fatal("stale reference silently rebound")
	}
	if _, err := p.Resolve(refs[1]); err != nil {
		t.Fatal("unrelated minor invalidated", err)
	}
	refs = nil
	err = p.History(context.Background(), "payment/transactions", 1, testObject(42), at.Add(-time.Hour), now, func(ref EventReference, _ []byte) error { refs = append(refs, ref); return nil })
	if err != nil || len(refs) != 1 {
		t.Fatal("retention postings", len(refs), err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = OpenEndpoint(c)
	if err != nil {
		t.Fatal("retained multi-minor restart", err)
	}
}

func TestCoordinatorRegistrationSelectsCollectionAndPreferred(t *testing.T) {
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test"})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	first := testLayout(t, false)
	second, _ := GenericDefinition()
	for _, l := range []*Layout{first, second} {
		r := httptest.NewRequest("PUT", "/definitions", bytes.NewReader(l.Bytes))
		definitionHeaders(r.Header, l)
		r.Header.Set("Content-Type", "application/pil")
		w := httptest.NewRecorder()
		p.serveHTTP(w, r)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	d := first.Definition
	d.Resource.Minor++
	d.Version++
	next, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Prefer(next); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("PUT", "/definitions", bytes.NewReader(first.Bytes))
	definitionHeaders(r.Header, first)
	r.Header.Set("Content-Type", "application/pil")
	w := httptest.NewRecorder()
	p.serveHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Definition-Version") != fmtDigest(next) || !bytes.Equal(w.Body.Bytes(), next.Bytes) {
		t.Fatal("preferred negotiation", w.Code)
	}
	r = httptest.NewRequest("HEAD", "/definitions", nil)
	definitionHeaders(r.Header, second)
	w = httptest.NewRecorder()
	p.serveHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("HEAD", "/definitions", nil)
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("Definition-Version", fmt.Sprintf("%x", first.Digest))
	w = httptest.NewRecorder()
	p.serveHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("ambiguous registration", w.Code)
	}
}
