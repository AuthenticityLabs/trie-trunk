package cache

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestStagingLimitsPreserveAcknowledgedAndAllowRetry(t *testing.T) {
	l := testLayout(t, false)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "local", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	first := testFrame(t, l, now.Add(-time.Minute), 1, false)
	next := testFrame(t, l, now.Add(time.Second), 2, false)
	s.policy.Staging = &StagingLimits{MaxRecords: 1, MaxBytes: int64(len(first))}
	if _, e = s.Append(Write, first); e != nil {
		t.Fatal(e)
	}
	if dup, e := s.Append(Write, first); !dup || e != nil {
		t.Fatal("retry of accepted event rejected", e)
	}
	if _, e = s.Append(Read, next); !errors.Is(e, ErrStagingFull) {
		t.Fatal("branch evaded limit", e)
	}
	if s.branches[Write].live.count(0) != 1 || s.branches[Read].live.count(0) != 0 || s.failed != nil {
		t.Fatal("capacity rejection damaged store")
	}
	now = now.Add(time.Hour)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	// Use a current event: the rejected event may now be behind the finalization
	// boundary and must follow the ordinary explicit late-event policy.
	next = testFrame(t, l, now, 3, false)
	if _, e = s.Append(Read, next); e != nil {
		t.Fatal(e)
	}
	s.policy.Staging = &StagingLimits{MaxDiskBytes: 1}
	if _, e = s.Append(Error, testFrame(t, l, now, 4, false)); !errors.Is(e, ErrStagingFull) {
		t.Fatal("disk budget", e)
	}
}
func TestStagingPolicyExplicitUnlimitedOverride(t *testing.T) {
	c, e := ParseConfiguration([]byte("version=1\ntimezone='UTC'\n[defaults.staging]\nmax_bytes=1024\n[resources.'payment/transactions.v1'.staging]\nmax_bytes=0\n"))
	if e != nil {
		t.Fatal(e)
	}
	if c.Resolve("payment/transactions.v1").Staging.MaxBytes != 0 || c.Resolve("other/events.v1").Staging.MaxBytes != 1024 {
		t.Fatal("override")
	}
	if _, e = ParseConfiguration([]byte("version=1\ntimezone='UTC'\n[defaults.staging]\nmax_records=-1\n")); e == nil {
		t.Fatal("negative budget")
	}
}

func TestStagingCapacityHTTPAcknowledgesDurablePrefix(t *testing.T) {
	l := testLayout(t, false)
	now := time.Now()
	p, e := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	p.api.Store.policy.Staging = &StagingLimits{MaxRecords: 1}
	first := testFrame(t, l, now, 1, false)
	second := testFrame(t, l, now, 2, false)
	r := httptest.NewRequest("POST", "/ingest?tap="+url.QueryEscape(p.api.tap(Write)), bytes.NewReader(append(first, second...)))
	r.Header.Set("Content-Type", "application/pil")
	definitionHeaders(r.Header, l)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 503 || w.Header().Get("Trunk-Received") != "1" || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if p.api.Store.failed != nil || p.api.Store.branches[Write].live.count(0) != 1 {
		t.Fatal("prefix not retained")
	}
}
