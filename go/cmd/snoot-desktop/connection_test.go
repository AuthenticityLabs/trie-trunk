package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionPreferencePersistsWithoutSwitchingActiveSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connection.json")
	c := &desktopConnection{cfg: configuration{Endpoint: "https://snoot.trietrunk.com", Socket: "/missing.sock", Port: 34185, Transport: "auto"}, path: path, activePreference: "auto"}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"transport":"http"}`))
	w := httptest.NewRecorder()
	c.serve(w, r, "Unix Socket")
	var got struct {
		Transport, Preference string
		Pending               bool
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Transport != "Unix Socket" || got.Preference != "http" || !got.Pending {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg configuration
	if json.Unmarshal(raw, &cfg) != nil || cfg.Transport != "http" || cfg.Socket != c.cfg.Socket || cfg.Port != 34185 {
		t.Fatal(string(raw))
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	for _, body := range []string{`{"transport":"unknown"}`, `{"transport":"socket"}`, `{"transport":"http","endpoint":"https://other.example"}`, `{"transport":"http"} {}`} {
		w = httptest.NewRecorder()
		c.serve(w, httptest.NewRequest("POST", "/", strings.NewReader(body)), "Unix Socket")
		if w.Code < 400 {
			t.Fatal("accepted", body)
		}
		unchanged, _ := os.ReadFile(path)
		if string(unchanged) != string(raw) {
			t.Fatal("failed change modified configuration")
		}
	}
}

func TestExplicitTransportsDoNotSilentlyFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "http-server") }))
	defer server.Close()
	cfg := configuration{Endpoint: server.URL, Socket: "/missing.sock", Transport: "socket"}
	if _, _, _, err := configuredTransport(context.Background(), cfg); err == nil {
		t.Fatal("socket-only fell back")
	}
	for _, preference := range []string{"http", "auto"} {
		cfg.Transport = preference
		target, tr, mode, err := configuredTransport(context.Background(), cfg)
		if err != nil || mode != "HTTP" {
			t.Fatal(mode, err)
		}
		client := &http.Client{Transport: tr}
		response, err := client.Get(target.String())
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		tr.CloseIdleConnections()
		if string(raw) != "http-server" {
			t.Fatal(string(raw))
		}
	}
}
