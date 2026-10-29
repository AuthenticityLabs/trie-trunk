package cache

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompactReplicationBounds(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		hourly bool
		want   string
	}{
		{"2600100", true, "2026-01-01T00:00:00Z"}, {"2836623", true, "2028-12-31T23:00:00Z"}, {"26001", false, "2026-01-01T00:00:00Z"},
	} {
		got, err := compactTime(tc.raw, tc.hourly)
		if err != nil || got.Format(time.RFC3339) != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	for _, raw := range []string{"2600000", "2636600", "2836700", "2600124", "26x0100", "202600100"} {
		if _, err := compactTime(raw, true); err == nil {
			t.Fatal("accepted invalid UTC bound", raw)
		}
	}
}
func TestBranchTimeRangeContainsServerLeavesOnly(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(5 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for n := uint32(1); n <= 3; n++ {
		if _, err = p.api.Store.Append(Write, testFrame(t, l, at.Add(time.Hour), n, false)); err != nil {
			t.Fatal(err)
		}
	}
	path := "/demo/payment/transactions.v1/0/write.tri?begin=2630100&end=2630103"
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		p.Handler("secret").ServeHTTP(w, r)
		return w
	}
	w := request("GET", path)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Trunk-Format") != "time-trie/0" || w.Header().Get("Trunk-Source") != "demo/payment/transactions.v1/write" || w.Header().Get("Definition-Version") != fmtDigestNative(l.Digest) {
		t.Fatal(w.Header())
	}
	raw := w.Body.Bytes()
	// The requested range is clipped to the first retained hour. The next hour
	// is sealed and empty, so its count is present without an occupancy byte.
	if len(raw) != 80+2*16 || order.Uint64(raw[64:72]) != uint64(at.Add(time.Hour).UnixMilli()) || order.Uint64(raw[72:80]) != uint64(at.Add(3*time.Hour).UnixMilli()) {
		t.Fatal("invalid leaf range", len(raw))
	}
	if getUintWidth(raw[94:96]) != 3 || getUintWidth(raw[110:112]) != 0 {
		t.Fatal("lost hourly counts")
	}
	if raw[46] != 0 || !bytes.Equal(raw[48:64], p.api.Store.branches[Write].generation[:]) {
		t.Fatal("invalid server binding")
	}
	for _, tc := range []struct {
		method, suffix string
		status         int
	}{{"POST", "", 405}, {"GET", "&end=2630104", 400}, {"GET", "&states=C3", 400}, {"GET", "&definition=missing", 400}} {
		if got := request(tc.method, path+tc.suffix); got.Code != tc.status {
			t.Fatal(tc, got.Code, got.Body.String())
		}
	}
	if got := request("GET", "/demo/payment/transactions.v1/0/write.tri?begin=2630110&end=2630111"); got.Code != 204 || got.Body.Len() != 0 {
		t.Fatal("unsealed hours advertised", got.Code)
	}
	if got := request("GET", "/other/payment/transactions.v1/0/write.tri?begin=2630100&end=2630103"); got.Code != 404 {
		t.Fatal("wrong cache selected", got.Code)
	}
	next := l.Definition
	next.Version++
	next.Resource.Minor++
	newer, err := Compile(next)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	err = p.install(newer)
	p.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := request("GET", path); got.Code != 200 {
		t.Fatal("minor installation changed an existing file path", got.Code)
	}
	newPath := strings.Replace(path, "/0/write.tri", "/1/write.tri", 1)
	if got := request("GET", newPath); got.Code != 204 || got.Header().Get("Definition-Version") != fmtDigestNative(newer.Digest) {
		t.Fatal("wrong minor path", got.Code, got.Header())
	}
}

func TestBranchIndexReplicationDoesNotCollectPayload(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(5 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for n := uint32(1); n <= 3; n++ {
		if _, err = p.api.Store.Append(Write, testFrame(t, l, at.Add(time.Hour), n, false)); err != nil {
			t.Fatal(err)
		}
	}
	request := func(ext, have string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/demo/payment/transactions.v1/0/write."+ext+"?begin=2630100&end=2630103&states=17", nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "native-ranges,native-state-pages,adaptive-integers")
		if have != "" {
			r.Header.Set("Trunk-Coverage", have)
		}
		w := httptest.NewRecorder()
		p.Handler("").ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return w
	}
	first := request("idx", "")
	manifests, chunks := readNativeTest(t, first.Body.Bytes())
	var indexes int
	var coverage Coverage
	for _, m := range manifests {
		if !m.IndexOnly || len(m.MemberRanges) != 0 {
			t.Fatal("index-only manifest advertises payload", m)
		}
		coverage.Definition = m.Definition
		coverage.Generation = m.Generation
		coverage.Index = append(coverage.Index, m.Index...)
	}
	for _, c := range chunks {
		if c.Kind == "pac" {
			t.Fatal("index request copied event payload")
		}
		if c.Kind == "index" {
			indexes++
		}
	}
	if indexes == 0 {
		t.Fatal("missing index rows")
	}
	raw, _ := json.Marshal([]Coverage{coverage})
	have := base64.RawURLEncoding.EncodeToString(raw)
	warm := request("idx", have)
	_, chunks = readNativeTest(t, warm.Body.Bytes())
	for _, c := range chunks {
		if c.Kind == "index" || c.Kind == "pac" {
			t.Fatal("repeated covered index/payload", c.Kind)
		}
	}
	full := request("pods", have)
	manifests, chunks = readNativeTest(t, full.Body.Bytes())
	packs := 0
	for _, m := range manifests {
		if m.IndexOnly {
			t.Fatal("pods marked index-only")
		}
	}
	for _, c := range chunks {
		if c.Kind == "pac" {
			packs++
		}
	}
	if packs != 1 {
		t.Fatal("expected one whole hour pack", packs)
	}
}
