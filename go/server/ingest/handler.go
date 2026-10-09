package ingest

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"trie-trunk/go/server/ingest/cbor"
	"trie-trunk/go/server/ingest/json"
	"trie-trunk/go/server/store"
)

type Config struct {
	Root          string
	Token         string
	MaxTaps       int
	MaxStreams    int
	StreamTimeout time.Duration
	OnError       func(error)
}

type Handler struct {
	config Config
	slots  chan struct{}
	mu     sync.Mutex
	taps   map[string]*tap
	closed bool
}

func New(c Config) (*Handler, error) {
	if c.Root == "" {
		return nil, fmt.Errorf("store root required")
	}
	if c.MaxTaps == 0 {
		c.MaxTaps = 64
	}
	if c.MaxStreams == 0 {
		c.MaxStreams = 64
	}
	if c.StreamTimeout == 0 {
		c.StreamTimeout = 5 * time.Minute
	}
	if c.MaxTaps < 1 || c.MaxStreams < 1 || c.StreamTimeout <= 0 {
		return nil, fmt.Errorf("invalid ingress limits")
	}
	return &Handler{
		config: c,
		slots:  make(chan struct{}, c.MaxStreams),
		taps:   make(map[string]*tap),
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.config.Token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.config.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if !strings.HasPrefix(r.URL.Path, "/") || r.URL.EscapedPath() != r.URL.Path || r.URL.RawQuery != "" || r.URL.ForceQuery || store.ValidateTap(name) != nil {
		http.Error(w, "expected /<tap>, one unescaped directory name", http.StatusBadRequest)
		return
	}
	useJSON := json.Matches(r)
	switch r.Method {
	case http.MethodGet:
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "WebSocket upgrade required", http.StatusUpgradeRequired)
			return
		}
	case http.MethodHead, http.MethodPut, http.MethodPost:
		if !useJSON {
			if err := cbor.ValidateRequest(r); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "ingestion busy", http.StatusServiceUnavailable)
		return
	}
	t, err := h.openTap(name)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrLimit) {
			status = http.StatusServiceUnavailable
		}
		if h.config.OnError != nil {
			h.config.OnError(err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	if r.Method != http.MethodGet {
		controller := http.NewResponseController(w)
		deadline := time.Now().Add(h.config.StreamTimeout)
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
	}
	if useJSON {
		t.json.ServeHTTP(w, r)
	} else {
		t.cbor.ServeHTTP(w, r)
	}
}
