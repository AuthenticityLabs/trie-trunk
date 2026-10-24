package cache

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeHTTPInterop(t *testing.T) {
	l := adaptiveLayout(t, testLayout(t, false).Definition, "16")
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := &HTTP{Store: s, Token: "test"}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	frame := testFrame(t, l, at, 1, false)
	request := func(method, path string, body []byte, known bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "adaptive-integers,native-ranges")
		r.Header.Set("Accept-Encoding", "gzip")
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("Content-Type", "application/pil")
		r.Header.Set("Definition-Version", fmtDigest(l))
		if known {
			r.Header.Set("Definition-Known", fmtDigest(l))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	tap := url.QueryEscape(h.tap(Write))
	w := request("POST", "/ingest?tap="+tap, frame, false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	path := "/collect?tap=" + tap + "&from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z"
	w = request("GET", path, nil, false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	raw := ungzipTest(t, w.Body.Bytes())
	if !bytes.HasPrefix(raw, []byte{'T', 'P', 'I', 'L', 2, 1}) || !bytes.HasSuffix(raw, []byte{255, 254}) {
		t.Fatal("framing")
	}
	define := &Layout{Widths: []int{0, 0, 0}}
	reader := bytes.NewReader(raw[6:])
	d, e := define.ReadFrame(reader, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(d, l.Bytes) {
		t.Fatal("missing unified map")
	}
	if tag, _ := reader.ReadByte(); tag != 2 {
		t.Fatal("binding")
	}
	bind := &Layout{Widths: []int{4, 0, 0}}
	if _, e = bind.ReadFrame(reader, nil); e != nil {
		t.Fatal(e)
	}
	_, chunks := readNativeTest(t, w.Body.Bytes())
	pacs := 0
	for _, chunk := range chunks {
		if chunk.Kind == "pac" {
			pacs++
		}
	}
	if pacs != 1 {
		t.Fatalf("expected whole PAC, got %d", pacs)
	}
	warm := request("GET", path, nil, true)
	if ungzipTest(t, warm.Body.Bytes())[5] != 2 {
		t.Fatal("definition downloaded again")
	}
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("PIL-Version", "2")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUpgradeRequired {
		t.Fatal("unnegotiated integer profile")
	}
	if dir := os.Getenv("PIL_NATIVE_FIXTURE_DIR"); dir != "" {
		// Standalone codec fixture, not a collection endpoint response.
		var fixture bytes.Buffer
		fixture.Write([]byte{'T', 'P', 'I', 'L', 2})
		if e = writeDefine(&fixture, l); e != nil {
			t.Fatal(e)
		}
		if e = writeBind(&fixture, 0, l.Digest, h.tap(Write)); e != nil {
			t.Fatal(e)
		}
		if e = writeEvent(&fixture, l, 0, frame, false); e != nil {
			t.Fatal(e)
		}
		fixture.Write([]byte{255, 254})
		raw = fixture.Bytes()
		if e = os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
		for n, b := range map[string][]byte{"definition.cbor": l.Bytes, "collect.pil": raw, "frame.pil": frame} {
			if e = os.WriteFile(filepath.Join(dir, n), b, 0644); e != nil {
				t.Fatal(e)
			}
		}
	}
}
func fmtDigest(l *Layout) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range l.Digest {
		out[i*2] = digits[b>>4]
		out[i*2+1] = digits[b&15]
	}
	return string(out)
}

func TestPILIngressStagingAndReceiverStubs(t *testing.T) {
	l := testLayout(t, false)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := &HTTP{Store: s, Token: "secret"}
	request := func(kind string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/ingest?tap="+url.QueryEscape(h.tap(Write)), bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("Definition-Version", fmtDigest(l))
		r.Header.Set("Content-Type", kind)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, kind := range []string{"application/jsonl", "application/x-ndjson", "text/plain", "application/syslog"} {
		if w := request(kind, []byte("not parsed")); w.Code != 501 {
			t.Fatal(kind, w.Code, w.Body.String())
		}
	}
	if w := request("application/cbor", []byte("not parsed")); w.Code != 415 {
		t.Fatal("typed destination accepted generic CBOR", w.Code)
	}
	raw := testFrame(t, l, now.Add(-time.Minute), 1, false)
	if w := request("Application/PIL", raw); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(s.branches[Write].rows) != 0 || s.branches[Write].live.count(0) != 1 {
		t.Fatal("HTTP bypassed holdback")
	}
	now = now.Add(time.Hour)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	raw = testFrame(t, l, now.Add(-2*time.Hour), 2, false)
	if w := request("application/pil", raw); w.Code != 409 || w.Header().Get("Trunk-Received") != "0" {
		t.Fatal(w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "/rejections", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"message":"request completed"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"pil_base64"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/rejections", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unauthenticated rejection log")
	}
}

// Ingress buffering must not limit frame size or change partial-read handling.
func TestPILIngressLargeFrameAndFragmentedBody(t *testing.T) {
	l := testLayout(t, false)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := &HTTP{Store: s}
	values := []Value{uuid(now.UnixMilli(), 1), testObject(42), Uint(1, 8), {}, {}, Text(string(bytes.Repeat([]byte("large field "), 24000))), {}}
	first, err := l.Encode(nil, values)
	if err != nil {
		t.Fatal(err)
	}
	second := testFrame(t, l, now.Add(time.Millisecond), 2, true)
	body := &fragmentedIngressBody{bytes.NewReader(append(bytes.Clone(first), second...))}
	r := httptest.NewRequest("POST", "/ingest?tap="+url.QueryEscape(h.tap(Write)), body)
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("Definition-Version", fmtDigest(l))
	r.Header.Set("Content-Type", "application/pil")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Trunk-Received") != "2" || w.Header().Get("Trunk-Acknowledgement") != "durable-staging" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	var rows [][]byte
	if err = s.SearchLive(context.Background(), Query{Branch: Write, From: now, To: now.Add(time.Second)}, func(raw []byte) error {
		rows = append(rows, bytes.Clone(raw))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !bytes.Equal(rows[0], first) || !bytes.Equal(rows[1], second) {
		t.Fatal("large or trailing sparse frame changed during ingress")
	}
}

type fragmentedIngressBody struct{ *bytes.Reader }

func (r *fragmentedIngressBody) Read(p []byte) (int, error) {
	return r.Reader.Read(p[:min(len(p), 37)])
}
