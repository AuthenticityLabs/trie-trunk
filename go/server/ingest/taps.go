package ingest

import (
	"errors"

	"trie-trunk/go/server/ingest/cbor"
	"trie-trunk/go/server/ingest/json"
	"trie-trunk/go/server/store"
)

type tap struct {
	store *store.Manager
	cbor  cbor.Handler
	json  json.Handler
}

func (h *Handler) openTap(name string) (*tap, error) {
	if err := store.ValidateTap(name); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, store.ErrClosed
	}
	if t, ok := h.taps[name]; ok {
		return t, nil
	}
	if len(h.taps) >= h.config.MaxTaps {
		return nil, store.ErrLimit
	}
	m, err := store.Open(store.Config{Root: h.config.Root, Tap: name})
	if err != nil {
		return nil, err
	}
	t := &tap{
		store: m,
		cbor:  cbor.Handler{Store: m, Timeout: h.config.StreamTimeout, OnError: h.config.OnError},
		json:  json.Handler{Store: m, Timeout: h.config.StreamTimeout, OnError: h.config.OnError},
	}
	h.taps[name] = t
	return t, nil
}

func (h *Handler) stores() []*store.Manager {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*store.Manager, 0, len(h.taps))
	for _, t := range h.taps {
		out = append(out, t.store)
	}
	return out
}

func (h *Handler) Flush() error {
	var errs []error
	for _, m := range h.stores() {
		if err := m.Flush(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *Handler) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	var errs []error
	for _, m := range h.stores() {
		if err := m.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
