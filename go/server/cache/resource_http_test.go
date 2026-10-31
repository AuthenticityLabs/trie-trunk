package cache

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Existing operation tests continue to exercise storage/codec behavior directly.
// Resource routing is tested separately below, through the public handler.
func (p *Endpoint) operationHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		p.serveHTTP(w, r)
	})
}

func TestCacheResourceRoutes(t *testing.T) {
	l := adaptiveLayout(t, testLayout(t, false).Definition, "16")
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	p, err := OpenEndpoint(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, Now: func() time.Time { return at.Add(2 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	source := p.api
	branch := l.Definition.Resource.Service + "/" + fmt.Sprintf("%s.v%d/write", l.Definition.Resource.Resource, l.Definition.Resource.Major)
	if _, err := source.Store.Append(Write, testFrame(t, l, at, 1, false)); err != nil {
		t.Fatal(err)
	}
	if err := source.Store.Flush(); err != nil {
		t.Fatal(err)
	}
	request := func(path string, public bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "native-ranges,adaptive-integers,pac-arrays")
		w := httptest.NewRecorder()
		if public {
			p.Handler("secret").ServeHTTP(w, r)
		} else {
			p.serveHTTP(w, r)
		}
		return w
	}
	bounds := fmt.Sprintf("beginTime=%d&endTime=%d", at.UnixMilli(), at.Add(time.Hour).UnixMilli())
	for _, kind := range []string{"seeds", "indexes", "timelines", "summaries"} {
		t.Run(kind, func(t *testing.T) {
			rangeQuery := bounds
			if kind == "summaries" {
				rangeQuery = fmt.Sprintf("beginTime=%d&endTime=%d", at.Truncate(24*time.Hour).UnixMilli(), at.Truncate(24*time.Hour).Add(24*time.Hour).UnixMilli())
			}
			w := request("/demo/"+kind+"?branch="+branch+"&"+rangeQuery, true)
			if w.Code != 200 && w.Code != 204 {
				t.Fatal(w.Code, w.Body.String())
			}
			if kind == "seeds" {
				before := request("/collect?tap=demo/"+branch+"&from="+at.Format(time.RFC3339)+"&to="+at.Add(time.Hour).Format(time.RFC3339), false)
				if before.Code != 200 || !bytes.Equal(w.Body.Bytes(), before.Body.Bytes()) {
					t.Fatal("resource route changed seed response bytes", before.Code)
				}
			}
		})
	}
	if w := request("/demo/seeds?branch="+branch+fmt.Sprintf("&beginTime=%d", at.UnixMilli()), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("/demo/collections?branch="+branch+"&id=00000000-0000-7000-8000-00000000002a", true); w.Code != 200 {
		t.Fatal("unbounded object history", w.Code, w.Body.String())
	}
	if w := request("/demo/registries?kind=bindings&branch="+branch, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/other/seeds?branch=" + branch + "&" + bounds, "/demo/seeds?branch=../../read&" + bounds, "/demo/seeds?branch=" + branch + "&" + bounds + "&beginTime=0", "/demo/seeds?from=0", "/demo/timelines?branch=" + branch + "&beginTime=1&endTime=2", "/demo/indexes?mode=unknown&" + bounds} {
		if w := request(path, true); w.Code < 400 {
			t.Fatal("accepted invalid request", path, w.Code)
		}
	}
	for _, path := range []string{"/collect", "/events", "/schemas", "/summaries", "/demo/payment/transactions.v1/0/write.pods"} {
		if w := request(path, true); w.Code != 404 {
			t.Fatal("exposed removed route", path, w.Code)
		}
	}
}
