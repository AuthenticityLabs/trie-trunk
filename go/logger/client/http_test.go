package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"trie-trunk/go/codec"
	"trie-trunk/go/models"
)

func TestHTTPLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler Handler
		status  int
		code    string
	}{
		{"success", func(w http.ResponseWriter, r *http.Request) error {
			FromContext(r.Context()).Operation(1)
			_, err := io.WriteString(w, "ok")
			return err
		}, 200, ""},
		{"empty", func(http.ResponseWriter, *http.Request) error { return nil }, 200, ""},
		{"validation", func(http.ResponseWriter, *http.Request) error { return models.New(models.INVALID_INPUT) }, 400, "INVALID_INPUT"},
		{"unknown", func(http.ResponseWriter, *http.Request) error { return errors.New("secret") }, 500, "UNEXPECTED"},
		{"panic", func(http.ResponseWriter, *http.Request) error { panic("secret") }, 500, "UNEXPECTED"},
		{"status", func(w http.ResponseWriter, r *http.Request) error { w.WriteHeader(404); return nil }, 404, "UNEXPECTED"},
		{"committedError", func(w http.ResponseWriter, r *http.Request) error { w.WriteHeader(202); return errors.New("secret") }, 202, "UNEXPECTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			l := testLogger(t, &out, Config{Service: codec.Text("customers")})
			h := l.Handle(tc.handler, HTTPOptions{OnError: func(err error) { t.Error(err) }})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "http://example.com/customer?secret=123", nil))
			if w.Code != tc.status || bytes.Count(out.Bytes(), []byte{'\n'}) != 1 {
				t.Fatalf("status/writes: %d %s", w.Code, out.String())
			}
			var entry map[string]any
			if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["http_status"] != float64(tc.status) || entry["response_bytes"] != float64(w.Body.Len()) {
				t.Fatalf("wrong response metadata: %s", out.String())
			}
			wantLevel := "INFO"
			if tc.code != "" {
				wantLevel = "ERROR"
			}
			if entry["level"] != wantLevel {
				t.Fatalf("outcome level: want %s, got %v", wantLevel, entry["level"])
			}
			if tc.code != "" && entry["error"].(map[string]any)["code"] != tc.code {
				t.Fatalf("wrong error: %s", out.String())
			}
			if bytes.Contains(out.Bytes(), []byte("secret")) || bytes.Contains(w.Body.Bytes(), []byte("secret")) {
				t.Fatal("internal error/query leaked")
			}
			if entry["id"] != w.Header().Get(HeaderTransactionId) {
				t.Fatal("transaction correlation lost")
			}
		})
	}
}

func TestPanicAfterCommitAbortsAndLogsOnce(t *testing.T) {
	for _, status := range []int{200, 500} {
		var out bytes.Buffer
		l := testLogger(t, &out, Config{})
		h := l.Handle(func(w http.ResponseWriter, r *http.Request) error { w.WriteHeader(status); panic("late") }, HTTPOptions{})
		func() {
			defer func() {
				if recover() != http.ErrAbortHandler {
					t.Error("committed panic was swallowed")
				}
			}()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		}()
		if bytes.Count(out.Bytes(), []byte{'\n'}) != 1 {
			t.Fatal("wrong completion count")
		}
	}
}

func TestCancellationAndFlush(t *testing.T) {
	var out bytes.Buffer
	l := testLogger(t, &out, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	h := l.Handle(func(w http.ResponseWriter, r *http.Request) error {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("lost flusher")
		}
		if _, ok := w.(http.Hijacker); ok {
			t.Fatal("advertised unsupported hijacker")
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		cancel()
		return nil
	}, HTTPOptions{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	var entry map[string]any
	if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["level"] != "ERROR" {
		t.Fatalf("cancellation must be an error outcome: %s", out.String())
	}
	if !w.Flushed || !bytes.Contains(out.Bytes(), []byte("CANCELED")) {
		t.Fatalf("flush/cancel failed: %s", out.String())
	}
}

type informationalWriter struct {
	header   http.Header
	statuses []int
	body     bytes.Buffer
}

type controllerOnlyWriter struct{ informationalWriter }

func (w *controllerOnlyWriter) FlushError() error { w.WriteHeader(200); return nil }

func TestControllerOnlyFlushAndUnsupportedFlush(t *testing.T) {
	var out bytes.Buffer
	l := testLogger(t, &out, Config{})
	h := l.Handle(func(w http.ResponseWriter, r *http.Request) error {
		if err := http.NewResponseController(w).Flush(); err != nil {
			return models.New(models.BAD_REQUEST)
		}
		return models.New(models.INVALID_INPUT)
	}, HTTPOptions{})
	w := &controllerOnlyWriter{informationalWriter{header: make(http.Header)}}
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if len(w.statuses) != 1 || w.statuses[0] != 200 || !bytes.Contains(out.Bytes(), []byte(`"http_status":200`)) {
		t.Fatalf("lost controller commit: %v %s", w.statuses, out.String())
	}
	out.Reset()
	unsupported := &informationalWriter{header: make(http.Header)}
	h.ServeHTTP(unsupported, httptest.NewRequest("GET", "/", nil))
	if len(unsupported.statuses) != 1 || unsupported.statuses[0] != 400 {
		t.Fatalf("unsupported flush committed: %v", unsupported.statuses)
	}
}

func (w *informationalWriter) Header() http.Header         { return w.header }
func (w *informationalWriter) WriteHeader(status int)      { w.statuses = append(w.statuses, status) }
func (w *informationalWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func TestInformationalResponseDoesNotCommit(t *testing.T) {
	var out bytes.Buffer
	l := testLogger(t, &out, Config{})
	h := l.Handle(func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(103)
		w.WriteHeader(201)
		w.WriteHeader(500)
		_, err := io.WriteString(w, "ok")
		return err
	}, HTTPOptions{})
	w := &informationalWriter{header: make(http.Header)}
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if len(w.statuses) != 2 || w.statuses[0] != 103 || w.statuses[1] != 201 || !bytes.Contains(out.Bytes(), []byte(`"http_status":201`)) {
		t.Fatalf("incorrect informational handling: %v %s", w.statuses, out.String())
	}
}

func TestAbortBeforeResponseRetainsNoStatus(t *testing.T) {
	var out bytes.Buffer
	l := testLogger(t, &out, Config{})
	h := l.Handle(func(http.ResponseWriter, *http.Request) error { panic(http.ErrAbortHandler) }, HTTPOptions{})
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("abort swallowed")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}()
	var entry map[string]any
	if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if _, ok := entry["http_status"]; ok {
		t.Fatalf("invented response status: %s", out.String())
	}
}

func TestHTTPConfiguredErrorReporting(t *testing.T) {
	failure := errors.New("sink failed")
	for _, middleware := range []bool{false, true} {
		calls := 0
		l := testLogger(t, errorSink{failure}, Config{OnError: func(err error) {
			calls++
			if !errors.Is(err, failure) {
				t.Errorf("unexpected failure: %v", err)
			}
		}})
		handler := l.Handle(func(http.ResponseWriter, *http.Request) error { return nil })
		if middleware {
			handler = l.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		}
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		if calls != 1 {
			t.Fatalf("completion reports = %d", calls)
		}
		l.Handle(func(http.ResponseWriter, *http.Request) error { return nil }, HTTPOptions{}).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		if calls != 1 {
			t.Fatal("explicit override did not disable reporting")
		}
	}
}

type errorSink struct{ err error }

func (s errorSink) Write([]byte) (int, error) { return 0, s.err }

func TestHTTPLogLimitDoesNotTruncatePublicError(t *testing.T) {
	var output bytes.Buffer
	l := testLogger(t, &output, Config{MaxTextBytes: 4})
	h := l.Handle(func(http.ResponseWriter, *http.Request) error {
		return models.New(models.INVALID_INPUT).WithMessage("public message")
	})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if !bytes.Contains(response.Body.Bytes(), []byte("public message")) {
		t.Fatalf("public error truncated: %s", response.Body.String())
	}
	if bytes.Contains(output.Bytes(), []byte("public message")) {
		t.Fatal("log text limit bypassed")
	}
}

func TestDashedCorrelationHeadersAreCaseInsensitive(t *testing.T) {
	for _, names := range [][2]string{
		{"client-transaction-id", "client-id"},
		{"Client-Transaction-Id", "Client-Id"},
		{"CLIENT-TRANSACTION-ID", "CLIENT-ID"},
	} {
		var output bytes.Buffer
		l := testLogger(t, &output, Config{})
		handler := l.Handle(func(http.ResponseWriter, *http.Request) error { return nil })
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Set(names[0], "upstream-123")
		request.Header.Set(names[1], "client-456")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var entry map[string]any
		if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["client_transaction_id"] != "upstream-123" || entry["client_id"] != "client-456" {
			t.Fatalf("lost correlation for %v: %s", names, output.String())
		}
		if id := response.Header().Get("TRANSACTION-ID"); id == "" || entry["id"] != id {
			t.Fatal("missing dashed response correlation")
		}
		if response.Header().Get("transactionId") != "" {
			t.Fatal("emitted obsolete header")
		}
	}
}
