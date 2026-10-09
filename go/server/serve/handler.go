package serve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"trie-trunk/go/server/search"
	"trie-trunk/go/server/store"
)

type Handler struct {
	Root      string
	Token     string
	Headers   []string
	OnError   func(error)
	schemaKey [32]byte
	slots     chan struct{}
	mu        sync.Mutex
	closed    bool
	active    sync.WaitGroup
	engine    *search.Engine
	run       func(context.Context, search.Config, io.Writer) error
}

func New(root, token string, headers []string, maxQueries int) (*Handler, error) {
	if root == "" || maxQueries < 1 || maxQueries > 64 {
		return nil, fmt.Errorf("store root and query limit between 1 and 64 required")
	}
	handler := &Handler{Root: root, Token: token, Headers: headers, slots: make(chan struct{}, maxQueries), run: search.Run}
	handler.engine = search.NewEngine(root, maxQueries)
	handler.run = handler.engine.Run
	if _, err := rand.Read(handler.schemaKey[:]); err != nil {
		return nil, err
	}
	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, remote bool) {
	if remote && h.Token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if (r.URL.Path != "/collect" && r.URL.Path != "/events" && r.URL.Path != "/schemas" && r.URL.Path != "/taps") || r.URL.EscapedPath() != r.URL.Path || r.URL.RawQuery != "" && !(r.URL.Path == "/collect" && r.Method == http.MethodGet) {
		http.NotFound(w, r)
		return
	}
	method := http.MethodPost
	if r.URL.Path == "/taps" || r.URL.Path == "/collect" && r.Method == http.MethodGet {
		method = http.MethodGet
	}
	if r.Method != method {
		if r.URL.Path == "/collect" {
			w.Header().Set("Allow", "GET, POST")
		} else {
			w.Header().Set("Allow", method)
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if method == http.MethodPost {
		kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || kind != "application/json" {
			http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
			return
		}
	}
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	collect := r.URL.Path == "/collect"
	if collect && (r.Header.Get("PIL-Version") != "2" || accept != search.PIL) {
		w.Header().Set("PIL-Version", "2")
		http.Error(w, "collect requires application/pil and PIL-Version: 2", http.StatusUpgradeRequired)
		return
	}
	if (accept == search.PIL) && r.Header.Get("PIL-Version") != "" && r.Header.Get("PIL-Version") != "1" && r.Header.Get("PIL-Version") != "2" {
		w.Header().Set("PIL-Version", "2")
		http.Error(w, "unsupported PIL version", http.StatusUpgradeRequired)
		return
	}
	query := collect || r.URL.Path == "/events"
	compact := query && accept == search.DictionaryJSONL
	pil := query && accept == search.PIL
	pilSchemas := r.URL.Path == "/schemas" && accept == search.PILSchemas
	format := "application/json"
	if query {
		format = "application/x-ndjson"
	}
	if accept != "" && accept != "*/*" && accept != format && !compact && !pil && !pilSchemas {
		http.Error(w, "unsupported response format", http.StatusNotAcceptable)
		return
	}
	if !h.acquire(query) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "search busy", http.StatusServiceUnavailable)
		return
	}
	defer func() {
		if query {
			<-h.slots
		}
		h.active.Done()
	}()
	controller := http.NewResponseController(w)
	if r.URL.Path == "/taps" {
		_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
		h.listTaps(w, r)
		return
	}
	_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	defer r.Body.Close()
	if !query {
		_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
		h.negotiate(w, r)
		return
	}
	now := time.Now().UTC()
	limit := uint64(100)
	config := search.Config{Start: now.Truncate(24 * time.Hour), Limit: &limit, Tail: true}
	var selectionErr error
	if method == http.MethodGet {
		selectionErr = decodeSelection(r, &config)
	} else {
		selectionErr = decodeRequest(r.Body, &config)
	}
	if err := selectionErr; err != nil {
		http.Error(w, "invalid search selection: "+err.Error(), http.StatusBadRequest)
		return
	}
	_ = controller.SetReadDeadline(time.Time{})
	config.Root = h.Root
	config.Compact = compact
	config.PIL = pil
	config.PILV2 = pil && r.Header.Get("PIL-Version") == "2"
	if compact && config.Pretty {
		http.Error(w, "dictionary JSONL requires one record per line", http.StatusBadRequest)
		return
	}
	if config.Headers == nil {
		config.Headers = h.Headers
	}
	if config.End.IsZero() {
		config.End = now
		if config.Follow {
			config.End = time.UnixMilli(1 << 48).UTC()
		}
	}
	if err := config.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if config.Tap != "" && store.ValidateTap(config.Tap) != nil {
		http.Error(w, "invalid tap", http.StatusBadRequest)
		return
	}
	sendDefinitions := false
	var known []string
	if collect {
		config.Negotiation = r.Header.Get("Definition-Version")
		var err error
		known, err = knownDefinitions(r.Header.Values("Definition-Known"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if compact || pil {
		if !collect && config.Negotiation == "" {
			http.Error(w, "negotiate dictionaries at /schemas before pulling events", http.StatusPreconditionRequired)
			return
		}
		catalogs, err := store.ReadCatalogs(r.Context(), h.Root, config.Tap)
		if err != nil {
			http.Error(w, "schema lookup failed: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if pil {
			for _, catalog := range catalogs {
				config.PILKnown = append(config.PILKnown, fmt.Sprintf("%x", catalog.Revision.Digest))
			}
		}
		version := h.schemaToken(config.Tap, catalogs)
		w.Header().Set("Definition-Version", version)
		matches := subtle.ConstantTimeCompare([]byte(config.Negotiation), []byte(version)) == 1
		sendDefinitions = collect && !matches
		if !collect && !matches {
			http.Error(w, "schemas changed; negotiate again at /schemas", http.StatusConflict)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	if compact {
		w.Header().Set("Content-Type", search.DictionaryJSONL)
	}
	if pil {
		w.Header().Set("Content-Type", search.PIL)
		version := "1"
		if config.PILV2 {
			version = "2"
		}
		w.Header().Set("PIL-Version", version)
		w.Header().Set("Trunk-Entry-Headers", strings.Join(config.Headers, ","))
	}
	w.Header().Set("Vary", "Accept, PIL-Version, Definition-Version")
	if collect {
		w.Header().Add("Vary", "Definition-Known")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Trailer", "Trunk-Status")
	completion := collect || (compact || config.PILV2) && r.Header.Get("Trunk-Completion") == "body"
	if completion {
		w.Header().Set("Trunk-Completion", "body")
	}
	output := &stream{writer: w, controller: controller}
	var err error
	if sendDefinitions {
		err = search.PILDefinitionsVersion(r.Context(), h.Root, config.Tap, known, true, output)
	}
	if err == nil {
		err = h.run(r.Context(), config, output)
	}
	if err != nil {
		if r.Context().Err() == nil && h.OnError != nil {
			h.OnError(err)
		}
		if !output.started {
			w.Header().Del("Trailer")
			http.Error(w, "search failed: "+err.Error(), http.StatusUnprocessableEntity)
		} else {
			w.Header().Set("Trunk-Status", "error")
		}
		return
	}
	if completion {
		marker := "{\"complete\":true}\n"
		if config.PILV2 {
			marker = "\xfe"
		}
		if _, err := io.WriteString(output, marker); err != nil {
			w.Header().Set("Trunk-Status", "error")
			return
		}
	}
	if !output.started {
		w.WriteHeader(http.StatusOK)
	}
	w.Header().Set("Trunk-Status", "ok")
}

type stream struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
	started    bool
}

func (s *stream) Write(data []byte) (int, error) {
	if err := s.controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	s.started = true
	n, err := s.writer.Write(data)
	if err == nil {
		err = s.controller.Flush()
	}
	if err == nil {
		_ = s.controller.SetWriteDeadline(time.Time{})
	}
	return n, err
}

func (h *Handler) acquire(query bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	if !query {
		h.active.Add(1)
		return true
	}
	select {
	case h.slots <- struct{}{}:
		h.active.Add(1)
		return true
	default:
		return false
	}
}

func (h *Handler) Close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.active.Wait()
	h.engine.Close()
}
