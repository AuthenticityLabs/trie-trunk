package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
)

func validTransport(mode string) bool { return mode == "auto" || mode == "socket" || mode == "http" }

func configuredTransport(ctx context.Context, cfg configuration) (*url.URL, *http.Transport, string, error) {
	if !validTransport(cfg.Transport) {
		return nil, nil, "", fmt.Errorf("invalid transport")
	}
	socket := cfg.Socket
	if cfg.Transport == "http" {
		socket = ""
	}
	target, transport, mode, err := selectTransport(ctx, cfg.Endpoint, socket)
	if err == nil && cfg.Transport == "socket" && mode != "Unix Socket" {
		transport.CloseIdleConnections()
		return nil, nil, "", fmt.Errorf("configured Unix socket is unavailable: %s", socket)
	}
	return target, transport, mode, err
}

func saveConfiguration(path string, cfg configuration) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".connection-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// Changes take effect only after restart: in-flight requests and workers keep
// their current server for the entire session.
type desktopConnection struct {
	mu                     sync.Mutex
	cfg                    configuration
	path, activePreference string
}

func (c *desktopConnection) serve(w http.ResponseWriter, r *http.Request, active string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Method == "POST" {
		var input struct {
			Transport string `json:"transport"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !validTransport(input.Transport) {
			http.Error(w, "Invalid Transport", 400)
			return
		}
		if input.Transport == "socket" {
			cfg := c.cfg
			cfg.Transport = input.Transport
			_, tr, _, err := configuredTransport(r.Context(), cfg)
			if err != nil {
				http.Error(w, "Unix Socket Unavailable", 409)
				return
			}
			tr.CloseIdleConnections()
		}
		next := c.cfg
		next.Transport = input.Transport
		if err := saveConfiguration(c.path, next); err != nil {
			http.Error(w, "Could Not Save Connection", 500)
			return
		}
		c.cfg = next
	} else if r.Method != "GET" {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method Not Allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Transport  string `json:"transport"`
		Preference string `json:"preference"`
		Endpoint   string `json:"endpoint"`
		Socket     string `json:"socket"`
		Pending    bool   `json:"pending"`
	}{active, c.cfg.Transport, c.cfg.Endpoint, c.cfg.Socket, c.cfg.Transport != c.activePreference})
}
