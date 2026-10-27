package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
)

func authorized(path string) *http.Request {
	r := httptest.NewRequest("GET", "http://127.0.0.1:34185"+path, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-secret"})
	return r
}
func TestBridgeAccessAndExactBinaryProxy(t *testing.T) {
	payload := []byte{0, 255, 80, 65, 67, 0, 128}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Cookie"), sessionCookie) {
			t.Error("bridge cookie leaked")
		}
		if r.Header.Get("Authorization") != "Bearer user-token" {
			t.Error("authorization changed")
		}
		w.Header().Set("Content-Type", "application/pil")
		w.Header().Set("Trailer", "Trunk-Status")
		w.Write(payload)
		w.Header().Set("Trunk-Status", "ok")
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	b := newBridge("127.0.0.1:34185", "test-secret", fstest.MapFS{"index.html": {Data: []byte("Snoot")}}, target, http.DefaultTransport, "HTTP")
	for _, kind := range []string{"missing-cookie", "wrong-host", "cross-site", "wrong-origin"} {
		r := authorized("/caches")
		switch kind {
		case "missing-cookie":
			r.Header.Del("Cookie")
		case "wrong-host":
			r.Host = "evil.test"
		case "cross-site":
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		case "wrong-origin":
			r.Header.Set("Origin", "https://evil.test")
		}
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(kind, w.Code)
		}
	}
	w := httptest.NewRecorder()
	b.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:34185/?desktop-session=test-secret", nil))
	if w.Code != 303 || w.Header().Get("Location") != "/" || len(w.Result().Cookies()) != 1 {
		t.Fatal("session bootstrap")
	}
	w = httptest.NewRecorder()
	b.ServeHTTP(w, authorized("/"))
	if w.Body.String() != "Snoot" {
		t.Fatal("bundled frontend not served")
	}
	r := authorized("/collect?from=one&to=two")
	r.Header.Set("Authorization", "Bearer user-token")
	w = httptest.NewRecorder()
	b.ServeHTTP(w, r)
	raw, _ := io.ReadAll(w.Result().Body)
	if string(raw) != string(payload) || w.Result().Trailer.Get("Trunk-Status") != "ok" {
		t.Fatal("binary/trailer changed")
	}
	w = httptest.NewRecorder()
	b.ServeHTTP(w, authorized("/__desktop/report"))
	if w.Code != 404 {
		t.Fatal("diagnostics exposed in production")
	}
}
func TestUnixPreferenceFallbackAndStreaming(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "snoot-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "server.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: ready\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	target, tr, mode, err := selectTransport(context.Background(), "https://snoot.trietrunk.com", socket)
	if err != nil || mode != "Unix Socket" {
		t.Fatal(mode, err)
	}
	defer tr.CloseIdleConnections()
	b := newBridge("", "test-secret", fstest.MapFS{}, target, tr, mode)
	front := httptest.NewServer(b)
	defer front.Close()
	u, _ := url.Parse(front.URL)
	b.host = u.Host
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", front.URL+"/events", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-secret"})
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 13)
	if _, err = io.ReadFull(res.Body, buf); err != nil || string(buf) != "data: ready\n\n" {
		t.Fatal("stream buffered", err)
	}
	_, fallback, mode, err := selectTransport(context.Background(), "https://snoot.trietrunk.com", socket+"-missing")
	if err != nil || mode != "HTTPS" {
		t.Fatal(mode, err)
	}
	fallback.CloseIdleConnections()
	if _, _, _, err = selectTransport(ctx, "http://example.com", ""); err == nil {
		t.Fatal("unencrypted remote allowed")
	}
}
func TestBridgeWebSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		kind, raw, err := c.Read(r.Context())
		if err == nil {
			c.Write(r.Context(), kind, raw)
		}
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	b := newBridge("", "test-secret", fstest.MapFS{}, target, http.DefaultTransport, "HTTP")
	front := httptest.NewServer(b)
	defer front.Close()
	u, _ := url.Parse(front.URL)
	b.host = u.Host
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+u.Host+"/events", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {sessionCookie + "=test-secret"}, "Origin": {front.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err = c.Write(ctx, websocket.MessageBinary, []byte{80, 73, 76, 255}); err != nil {
		t.Fatal(err)
	}
	kind, raw, err := c.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || string(raw) != string([]byte{80, 73, 76, 255}) {
		t.Fatal("websocket binary changed", err)
	}
}
