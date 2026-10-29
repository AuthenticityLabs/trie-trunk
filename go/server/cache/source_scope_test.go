package cache

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSourceScopeSelection(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := &HTTP{Store: s}
	for _, b := range []Branch{Write, Error} {
		if _, err = s.Append(b, testFrame(t, l, at, 1, false)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		scope string
		want  []Branch
	}{
		{"demo/payment/*/*", []Branch{Read, Write, Error, Debug, Trace}},
		{"*/*/*/error", []Branch{Error}},
		{"demo/*/transactions.v1/write", []Branch{Write}},
		{"demo/customer/*/*", nil}, {"dem*/*/*/*", nil}, {"demo/*", nil},
	} {
		got, _ := h.selectedBranches(tc.scope)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: %v want %v", tc.scope, got, tc.want)
		}
	}
	for _, scope := range []string{"demo/payment/*/*", "*/*/*/error"} {
		r := httptest.NewRequest("GET", "/collect?format=native&tap="+url.QueryEscape(scope)+"&from=2026-10-16T12:00:00Z&to=2026-10-16T13:00:00Z", nil)
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "native-ranges")
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		manifests, _ := readNativeTest(t, w.Body.Bytes())
		for _, m := range manifests {
			if strings.HasSuffix(scope, "/error") && m.Branch != Error {
				t.Fatal("other branch escaped", m)
			}
		}
		if len(manifests) == 0 {
			t.Fatal("no manifests")
		}
	}
	r := httptest.NewRequest("POST", "/schemas", strings.NewReader(`{"tap":"demo/payment/*/write"}`))
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("Accept", "application/pil")
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(h.tap(Write))) {
		t.Fatal("scoped schemas", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/ingest?tap=demo%2Fpayment%2F*%2Fwrite", bytes.NewReader(testFrame(t, l, at, 2, false)))
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("Content-Type", "application/pil")
	r.Header.Set("Definition-Version", fmtDigest(l))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 412 {
		t.Fatal("ingress accepted wildcard", w.Code)
	}
}
