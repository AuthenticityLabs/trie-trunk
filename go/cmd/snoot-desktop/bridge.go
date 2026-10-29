package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const sessionCookie = "snoot_desktop_session"

var apiPaths = []string{"/trunk", "/collections", "/caches", "/schemas", "/events", "/collect", "/summaries", "/objects", "/configurations", "/storage", "/rejections", "/definitions", "/ingest"}

// One transport is selected for the session. A failed mutation is never replayed
// against a different server; changing connections requires an explicit restart.
func selectTransport(ctx context.Context, endpoint, socket string) (*url.URL, *http.Transport, string, error) {
	target, err := url.Parse(endpoint)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "" && target.Path != "/" || target.Scheme != "https" && target.Scheme != "http" {
		return nil, nil, "", fmt.Errorf("endpoint must be an HTTP(S) origin")
	}
	if target.Scheme == "http" && target.Hostname() != "localhost" && !net.ParseIP(target.Hostname()).IsLoopback() {
		return nil, nil, "", fmt.Errorf("remote endpoints require HTTPS")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true // PAC bytes and Content-Encoding pass unchanged.
	transport.MaxIdleConns = 64
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	if socket != "" {
		st, e := os.Stat(socket)
		if e == nil && st.Mode()&os.ModeSocket != 0 {
			dialer := &net.Dialer{Timeout: 2 * time.Second}
			connection, e := dialer.DialContext(ctx, "unix", socket)
			if e == nil {
				connection.Close()
				transport.Proxy = nil
				transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socket)
				}
				return &url.URL{Scheme: "http", Host: "localhost"}, transport, "Unix Socket", nil
			}
			log.Printf("Local socket unavailable; using configured HTTP endpoint")
		} else if e != nil && !os.IsNotExist(e) {
			log.Printf("Local socket unavailable; using configured HTTP endpoint")
		}
	}
	return target, transport, strings.ToUpper(target.Scheme), nil
}

type bridge struct {
	host, token   string
	files         fs.FS
	proxy         http.Handler
	selfTest      bool
	dashboardTest bool
	report        chan []byte
	transport     string
	connection    *desktopConnection
}

func newBridge(host, token string, files fs.FS, target *url.URL, transport http.RoundTripper, mode string) *bridge {
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Host = target.Host
		cookies := r.Cookies()
		r.Header.Del("Cookie")
		for _, c := range cookies {
			if c.Name != sessionCookie {
				r.AddCookie(c)
			}
		}
		r.Header.Del("Origin")
		r.Header.Del("Referer")
	}
	proxy.Transport = transport
	proxy.FlushInterval = -1 // Deliver each streaming write immediately, including SSE.
	proxy.ModifyResponse = func(r *http.Response) error { r.Header.Del("Alt-Svc"); return nil }
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		log.Printf("Upstream request failed: %v", e)
		http.Error(w, "Server Unavailable", http.StatusBadGateway)
	}
	return &bridge{host: host, token: token, files: files, proxy: proxy, transport: mode, report: make(chan []byte, 1)}
}
func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Host != b.host || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Forbidden", 403)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+b.host {
		http.Error(w, "Forbidden", 403)
		return
	}
	if r.URL.Path == "/" && r.Method == "GET" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("desktop-session")), []byte(b.token)) == 1 {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: b.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(b.token)) != 1 {
		http.Error(w, "Open Snoot To Continue", 403)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/__desktop/") {
		b.diagnostics(w, r)
		return
	}
	for _, p := range apiPaths {
		if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p+"/") {
			b.proxy.ServeHTTP(w, r)
			return
		}
	}
	// Native replication routes are catalog paths, rather than fixed API names.
	// Keep bundled frontend assets local; proxy the server's data-file routes.
	if !strings.HasPrefix(r.URL.Path, "/assets/") {
		switch path.Ext(r.URL.Path) {
		case ".rx", ".tri", ".sum", ".idx", ".tvy", ".pods":
			b.proxy.ServeHTTP(w, r)
			return
		}
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "Method Not Allowed", 405)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	if name == "index.html" {
		raw, e := fs.ReadFile(b.files, name)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		html := strings.Replace(string(raw), "<html", "<html data-snoot-desktop", 1)
		if b.selfTest {
			html = strings.Replace(html, "</head>", `<script defer src="/__desktop/self-test.js"></script></head>`, 1)
		}
		if r.Method != "HEAD" {
			w.Write([]byte(html))
		}
		return
	}
	http.FileServer(http.FS(b.files)).ServeHTTP(w, r)
}
