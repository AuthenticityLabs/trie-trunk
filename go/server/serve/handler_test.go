package serve

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"trie-trunk/go/server/search"
)

func TestRequestValidationAndStreamErrors(t *testing.T) {
	h, err := New("/store", "token", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h.run = func(ctx context.Context, c search.Config, w io.Writer) error {
		calls++
		if c.Root != "/store" {
			t.Fatal("client changed store root")
		}
		_, err := io.WriteString(w, "{\"message\":\"ok\"}\n")
		return err
	}
	for _, test := range []struct {
		body, accept string
		status       int
	}{
		{`{}`, "", 200},
		{`{"root":"/etc"}`, "", 400},
		{`{"tap":"../test"}`, "", 400},
		{`{"unknown":true}`, "", 400},
		{`{} {}`, "", 400},
		{`{"from":"2026-09-26T00:00:00Z","to":"2026-09-25T00:00:00Z"}`, "", 400},
		{strings.Repeat(" ", 65537) + `{}`, "", 400},
		{`{}`, "image/png", 406},
	} {
		req := httptest.NewRequest("POST", "/events", strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Accept", test.accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != test.status {
			t.Fatalf("got %d %s want %d", w.Code, w.Body.String(), test.status)
		}
		if test.status == 200 && w.Result().Trailer.Get("Trunk-Status") != "ok" {
			t.Fatal("missing completion marker")
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	for _, partial := range []bool{false, true} {
		h.run = func(ctx context.Context, c search.Config, w io.Writer) error {
			if partial {
				io.WriteString(w, "{}\n")
			}
			return errors.New("test failure")
		}
		req := httptest.NewRequest("POST", "/events", bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if partial {
			if w.Code != 200 || w.Result().Trailer.Get("Trunk-Status") != "error" {
				t.Fatal("partial result reported success")
			}
		} else if w.Code != 422 {
			t.Fatal(w.Code)
		}
	}
}

func TestRequestHeadersDoNotMutateServerDefaults(t *testing.T) {
	h, err := New("/store", "", []string{"id", "message"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var seen [][]string
	h.run = func(ctx context.Context, c search.Config, w io.Writer) error {
		seen = append(seen, append([]string{}, c.Headers...))
		return nil
	}
	for _, body := range []string{`{"headers":["message"]}`, `{}`, `{"headers":[]}`} {
		req := httptest.NewRequest("POST", "/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(seen) != 3 || strings.Join(seen[0], ",") != "message" || strings.Join(seen[1], ",") != "id,message" || len(seen[2]) != 0 {
		t.Fatal(seen)
	}
}

func TestDictionaryContentNegotiation(t *testing.T) {
	h, err := New("/store", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, accept := range []string{"application/x-ndjson", search.DictionaryJSONL, search.PIL} {
		h.run = func(_ context.Context, c search.Config, w io.Writer) error {
			if c.Compact != (accept == search.DictionaryJSONL) {
				t.Fatalf("wrong projection for %s", accept)
			}
			return nil
		}
		req := httptest.NewRequest("POST", "/events", strings.NewReader(`{"negotiation":"`+h.schemaToken("", nil)+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 || w.Header().Get("Content-Type") != accept || w.Header().Get("Vary") != "Accept, PIL-Version, Definition-Version" {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
	}
	for _, body := range []string{`{"pretty":true}`, `{"compact":true}`} {
		req := httptest.NewRequest("POST", "/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", search.DictionaryJSONL)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestBrowserStreamCompletion(t *testing.T) {
	for _, test := range []struct {
		name       string
		body       string
		failure    bool
		completion bool
	}{
		{"events", "[]\n", false, true},
		{"empty", "", false, true},
		{"partial", "[]\n", true, true},
		{"failure", "", true, true},
		{"legacy", "[]\n", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, err := New(t.TempDir(), "", nil, 1)
			if err != nil {
				t.Fatal(err)
			}
			h.run = func(_ context.Context, _ search.Config, w io.Writer) error {
				if test.body != "" {
					if _, err := io.WriteString(w, test.body); err != nil {
						return err
					}
				}
				if test.failure {
					return errors.New("interrupted")
				}
				return nil
			}
			r := httptest.NewRequest("POST", "/events", strings.NewReader(`{"negotiation":"`+h.schemaToken("", nil)+`"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", search.DictionaryJSONL)
			if test.completion {
				r.Header.Set("Trunk-Completion", "body")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if got := strings.Contains(w.Body.String(), `{"complete":true}`); got != (test.completion && !test.failure) {
				t.Fatalf("unexpected completion: %s", w.Body.String())
			}
			if test.completion && w.Header().Get("Trunk-Completion") != "body" {
				t.Fatal("missing completion negotiation")
			}
			if !test.completion && w.Body.String() != test.body {
				t.Fatal("legacy stream changed")
			}
			if !test.failure && w.Result().Trailer.Get("Trunk-Status") != "ok" {
				t.Fatal("missing status trailer")
			}
		})
	}
}

func TestCollectFailureDoesNotEmitSuccessMarker(t *testing.T) {
	h, err := New(t.TempDir(), "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	h.run = func(_ context.Context, _ search.Config, w io.Writer) error {
		w.Write([]byte("TPIL\x02\xff"))
		return errors.New("failed after final search frame")
	}
	req := httptest.NewRequest("GET", "/collect", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", search.PIL)
	req.Header.Set("PIL-Version", "2")
	req.Header.Set("Definition-Version", h.schemaToken("", nil))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != 200 || bytes.HasSuffix(response.Body.Bytes(), []byte{254}) || response.Result().Trailer.Get("Trunk-Status") != "error" {
		t.Fatal("partial collect claimed success", response.Body.Bytes())
	}
}
