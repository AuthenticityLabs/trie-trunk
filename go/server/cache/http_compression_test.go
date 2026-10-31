package cache

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func gzipMember(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestHTTPGzipIngressMembersRecovery(t *testing.T) {
	l := testLayout(t, false)
	now := time.Now()
	c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }}
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	h := &HTTP{Store: s}
	a, b := testFrame(t, l, now, 1, false), testFrame(t, l, now, 2, false)
	packed := append(gzipMember(t, a), gzipMember(t, b)...)
	r := httptest.NewRequest("POST", "/ingest?tap="+url.QueryEscape(h.tap(Write)), bytes.NewReader(packed))
	r.Header.Set("Content-Type", "application/pil")
	r.Header.Set("Content-Encoding", "gzip")
	r.Header.Set("PIL-Version", "2")
	r.Header.Set("Definition-Version", fmtDigest(l))
	w := httptest.NewRecorder()
	h.serveHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Trunk-Received") != "2" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for _, p := range s.branches[Write].pendingAll() {
		if !sameSeedData(t, l, p.frame, a) && !sameSeedData(t, l, p.frame, b) {
			t.Fatal("staging did not retain original plain PIL")
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := 0
	err = s.SearchLive(context.Background(), Query{Branch: Write, From: now.Add(-time.Hour), To: now.Add(time.Hour)}, func(raw []byte) error {
		if !sameSeedData(t, l, raw, a) && !sameSeedData(t, l, raw, b) {
			t.Fatal("recovery changed payload")
		}
		n++
		return nil
	})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
}

func TestHTTPGzipRejectsDamagedOrUnsupportedCoding(t *testing.T) {
	for _, kind := range []string{"truncated", "checksum", "trailing", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			l := testLayout(t, false)
			s, err := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			h := &HTTP{Store: s}
			packed := gzipMember(t, testFrame(t, l, time.Now(), 1, false))
			coding := "gzip"
			switch kind {
			case "truncated":
				packed = packed[:len(packed)-4]
			case "checksum":
				packed[len(packed)-8] ^= 0xff
			case "trailing":
				packed = append(packed, 1, 2, 3)
			case "unsupported":
				coding = "br"
			}
			r := httptest.NewRequest("POST", "/ingest?tap="+url.QueryEscape(h.tap(Write)), bytes.NewReader(packed))
			r.Header.Set("Content-Type", "application/pil")
			r.Header.Set("Content-Encoding", coding)
			r.Header.Set("PIL-Version", "2")
			r.Header.Set("Definition-Version", fmtDigest(l))
			w := httptest.NewRecorder()
			h.serveHTTP(w, r)
			if w.Code < 400 || w.Header().Get("Trunk-Acknowledgement") != "" {
				t.Fatalf("acknowledged invalid coding: %d", w.Code)
			}
		})
	}
}

func TestHTTPClientDecodesConsecutiveGzipMembers(t *testing.T) {
	// A response is one coded representation, even when it is assembled from
	// independent stored members. No bytes may sit outside those members.
	a, b := []byte("first PAC"), []byte("second PAC")
	wire := append(gzipMember(t, a), gzipMember(t, b)...)
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
				return
			}
			response := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Encoding": {"gzip"}}, ContentLength: int64(len(wire)), Body: io.NopCloser(bytes.NewReader(wire))}
			_ = response.Write(server)
		}()
		return client, nil
	}}
	defer transport.CloseIdleConnections()
	r, err := (&http.Client{Transport: transport}).Get("http://local.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	plain, err := io.ReadAll(r.Body)
	if err != nil || !r.Uncompressed || !bytes.Equal(plain, append(a, b...)) {
		t.Fatal("multi-member HTTP decoding", err)
	}
}
