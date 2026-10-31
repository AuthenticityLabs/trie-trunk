package cache

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestViewBucketCoverageIsFullScopedAndSealed(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := at.Add(3 * time.Hour)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 6; i++ {
		if _, err = s.Append(Write, testFrame(t, l, at.Add(time.Duration(i)*10*time.Minute), uint32(i+1), false)); err != nil {
			t.Fatal(err)
		}
	}
	h := &HTTP{Store: s, Token: "test"}
	path := "/events?tap=" + url.QueryEscape(h.tap(Write)) + "&from=2026-09-01T12:10:00Z&to=2026-09-01T12:50:00Z&page=2"
	get := func(path, known string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays,adaptive-integers,view-buckets")
		r.Header.Set("Bucket-Known", known)
		w := httptest.NewRecorder()
		h.serveHTTP(w, r)
		return w
	}
	first := get(path, "")
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	d := collectView(t, l, h, first.Body.Bytes())
	if len(d.result.Buckets) != 1 || len(d.frames) != 2 {
		t.Fatalf("missing page/bucket: %+v", d.result)
	}
	b := d.result.Buckets[0]
	if !b.Sealed || b.From != at.UnixMilli() || b.To != at.Add(time.Hour).UnixMilli() || len(b.Ranges) != 1 || b.Ranges[0] != (Span{1, 7}) {
		t.Fatalf("partial search certified partial bucket: %+v", b)
	}
	// A different time query reuses the same immutable bucket receipt.
	fullPath := strings.Replace(strings.Replace(path, "12:10:00", "12:00:00", 1), "12:50:00", "13:00:00", 1)
	warm := get(fullPath, b.Key)
	if warm.Code != 200 {
		t.Fatal(warm.Body.String())
	}
	reused := collectView(t, l, h, warm.Body.Bytes())
	if len(reused.frames) != 0 || len(reused.result.Page) != 2 || !reused.result.Page[0].Cached || reused.result.Buckets[0].Key != b.Key {
		t.Fatal("bucket did not reuse payloads across queries")
	}
	if dir := os.Getenv("PIL_BUCKET_FIXTURE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		exportViewNative(t, dir, h, at, at.Add(time.Hour))
		p := fullPath
		for i := 0; i < 3; i++ {
			response := get(p, "")
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("page-%d.pil", i)), response.Body.Bytes(), 0644); err != nil {
				t.Fatal(err)
			}
			decoded := collectView(t, l, h, response.Body.Bytes())
			p = fullPath + "&after=" + url.QueryEscape(decoded.result.Next)
		}
		if err := os.WriteFile(filepath.Join(dir, "warm.pil"), warm.Body.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
	wrong := collectView(t, l, h, get(fullPath, strings.Repeat("0", 64)).Body.Bytes())
	if len(wrong.frames) != 2 {
		t.Fatal("unknown bucket suppressed payloads")
	}
	next := collectView(t, l, h, get(fullPath+"&after="+url.QueryEscape(reused.result.Next), b.Key).Body.Bytes())
	if len(next.frames) != 0 || next.result.Page[0].Row != 3 {
		t.Fatal("bucket continuation lost order")
	}
	// Retention/rewrite changes the generation even when row positions coincide.
	s.mu.Lock()
	s.branches[Write].generation[0] ^= 0xff
	s.mu.Unlock()
	changed := collectView(t, l, h, get(fullPath, b.Key).Body.Bytes())
	if len(changed.frames) != 2 || changed.result.Buckets[0].Key == b.Key {
		t.Fatal("stale generation reused")
	}
}

func TestOpenViewBucketCannotSuppressPayloads(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(40 * time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Append(Write, testFrame(t, l, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	h := &HTTP{Store: s}
	get := func(known string) viewDecoded {
		r := httptest.NewRequest("GET", "/events?tap="+url.QueryEscape(h.tap(Write))+"&from=2026-09-01T12:00:00Z&to=2026-09-01T13:00:00Z", nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays,adaptive-integers,view-buckets")
		r.Header.Set("Bucket-Known", known)
		w := httptest.NewRecorder()
		h.serveHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		return collectView(t, l, h, w.Body.Bytes())
	}
	first := get("")
	if len(first.result.Buckets) != 0 {
		t.Fatal("open bucket sealed")
	}
	next := get("")
	if len(next.frames) != 1 || next.result.Page[0].Cached {
		t.Fatal("open bucket treated as complete")
	}
}
