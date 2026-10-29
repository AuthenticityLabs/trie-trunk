package cache

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTP exposes canonical PAC collection, indexed views, and PIL ingress.
type HTTP struct {
	rollover     func() (*Layout, error)
	Store        *Store
	sources      []*HTTP
	storageRoots map[string]string
	Token        string
	ingressOnce  sync.Once
	ingressSlots chan struct{}
}

func (h *HTTP) tap(b Branch) string {
	n := h.Store.layout.Definition.Resource
	return h.Store.config.Cache + "/" + n.Service + "/" + fmt.Sprintf("%s.v%d", n.Resource, n.Major) + "/" + b.Name()
}
func (h *HTTP) branch(tap string) ([]Branch, error) {
	if tap == "" {
		return []Branch{Read, Write, Error, Debug, Trace}, nil
	}
	for b := Read; b <= Trace; b++ {
		if tap == h.tap(b) {
			return []Branch{b}, nil
		}
	}
	return nil, fmt.Errorf("unknown collection branch")
}

// Selection wildcards match complete catalog components only. They do not
// change stream identities or authorize ingestion to multiple branches.
func (h *HTTP) selectedBranches(selector string) ([]Branch, error) {
	if !strings.Contains(selector, "*") {
		return h.branch(selector)
	}
	parts := strings.Split(selector, "/")
	if len(parts) != 4 {
		return nil, fmt.Errorf("invalid collection selector")
	}
	var found []Branch
	for b := Read; b <= Trace; b++ {
		actual := strings.Split(h.tap(b), "/")
		match := true
		for i, part := range parts {
			if part != "*" && part != actual[i] {
				match = false
				break
			}
		}
		if match {
			found = append(found, b)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("unknown collection branch")
	}
	return found, nil
}
func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.Token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Trunk-Features", "native-ranges,pac-arrays")
	if trunkRequest(r) {
		h.trunkHTTP(w, r)
		return
	}
	if h.trunkFileHTTP(w, r) {
		return
	}
	if file, ok := branchFilePath(r.URL.Path); ok {
		h.branchFileHTTP(w, r, file)
		return
	}
	if r.URL.Path == "/storage" {
		h.storageHTTP(w, r)
		return
	}
	if r.URL.Path == "/summaries" {
		h.summariesHTTP(w, r)
		return
	}
	if r.URL.Path == "/events" {
		h.viewHTTP(w, r)
		return
	}
	if r.URL.Path == "/collect" {
		h.nativeCollect(w, r)
		return
	}
	if r.URL.Path == "/rejections" && r.Method == http.MethodGet {
		h.rejectionsHTTP(w, r)
		return
	}
	if r.URL.Path == "/ingest" && r.Method != http.MethodPost {
		h.genericIngress(w, r)
		return
	}
	if r.URL.Path == "/ingest" && r.Method == http.MethodPost {
		kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch strings.ToLower(kind) {
		case "application/cbor", "application/json", "application/jsonl", "application/x-ndjson", "text/plain", "application/syslog":
			h.genericIngress(w, r)
			return
		case "application/pil":
		default:
			http.Error(w, "Content-Type: application/pil required", 415)
			return
		}
	}
	if catalogRequest(r) {
		if !catalogGET(w, r) {
			return
		}
		var taps []string
		for b := Read; b <= Trace; b++ {
			taps = append(taps, h.tap(b))
		}
		w.Header().Set("Content-Type", "application/json")
		writeCatalog(w, r, taps, collectionSources([]*HTTP{h}))
		return
	}
	if r.URL.Path != "/schemas" && r.URL.Path != "/ingest" {
		http.NotFound(w, r)
		return
	}
	method := http.MethodPost
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", 405)
		return
	}
	l := h.Store.layout
	digest := fmt.Sprintf("%x", l.Digest)
	w.Header().Set("PIL-Version", "2")
	w.Header().Set("Definition-Version", digest)
	if r.Header.Get("PIL-Version") != "2" {
		http.Error(w, "PIL-Version: 2 required", 426)
		return
	}
	if len(h.sources) == 0 && l.Adaptive() && !strings.Contains(","+r.Header.Get("PIL-Features")+",", ",adaptive-integers,") {
		http.Error(w, "PIL-Features: adaptive-integers required", 426)
		return
	}
	q := r.URL.Query()
	tap := q.Get("tap")
	known := r.Header.Get("Definition-Known")
	if r.URL.Path == "/schemas" {
		var body struct {
			Tap   string   `json:"tap"`
			Known []string `json:"known"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&body); e != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		tap = body.Tap
		known = strings.Join(body.Known, ",")
	}
	type target struct {
		api    *HTTP
		branch Branch
	}
	sources := h.sources
	if len(sources) == 0 {
		sources = []*HTTP{h}
	}
	var targets []target
	for _, source := range sources {
		branches, err := source.selectedBranches(tap)
		if err != nil {
			continue
		}
		for _, branch := range branches {
			targets = append(targets, target{source, branch})
		}
	}
	if len(targets) == 0 {
		http.Error(w, "unknown collection branch", 400)
		return
	}
	branches, _ := h.branch(tap)
	var e error
	if r.URL.Path == "/ingest" {
		h.ingressOnce.Do(func() { h.ingressSlots = make(chan struct{}, 64) })
		select {
		case h.ingressSlots <- struct{}{}:
			defer func() { <-h.ingressSlots }()
		default:
			http.Error(w, "ingestion busy", 503)
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(5 * time.Minute))
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Minute))
		defer controller.SetReadDeadline(time.Time{})
		defer controller.SetWriteDeadline(time.Time{})
		if len(branches) != 1 || r.Header.Get("Definition-Version") != digest {
			http.Error(w, "ingest requires a branch, application/pil and matching Definition-Version", 412)
			return
		}
		encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
		if encoding != "" && encoding != "identity" && encoding != "gzip" {
			w.Header().Set("Accept-Encoding", "gzip, identity")
			http.Error(w, "unsupported Content-Encoding", http.StatusUnsupportedMediaType)
			return
		}
		body, closeBody, err := ingressReader(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer closeBody()
		// Read into Rust-owned buffers, then transfer ownership into resident
		// arenas. HTTP/TLS and gzip may retain bounded transport scratch.
		input := body
		count := 0
		stream, streamErr := h.Store.ingressStream()
		if streamErr != nil {
			http.Error(w, streamErr.Error(), 503)
			return
		}
		defer stream.close()
		var batch []nativeInput
		size := 0
		stage := func() error {
			n, e := h.Store.appendNativeBatch(branches[0], batch)
			stream.staged()
			clear(batch)
			count += n
			batch = batch[:0]
			size = 0
			return e
		}
		report := func(e error, status int) {
			if errors.Is(e, ErrIndexCapacity) && h.rollover != nil {
				next, err := h.rollover()
				if err != nil {
					e = err
					status = 503
				} else {
					definitionHeaders(w.Header(), next)
					w.Header().Set("PIL-Capacity-Rollover", "1")
					status = http.StatusPreconditionFailed
				}
			}
			if errors.Is(e, ErrStagingFull) {
				status = http.StatusServiceUnavailable
				w.Header().Set("Retry-After", "1")
			}
			var late *LateEventError
			if errors.As(e, &late) {
				status = http.StatusConflict
			}
			h.Store.mu.Lock()
			if h.Store.failed != nil {
				status = http.StatusServiceUnavailable
			}
			h.Store.mu.Unlock()
			w.Header().Set("Trunk-Received", fmt.Sprint(count))
			http.Error(w, e.Error(), status)
		}
		for {
			if e = r.Context().Err(); e != nil {
				return
			}
			frame, readErr := stream.read(l, input)
			if readErr != nil {
				// Even a malformed final frame does not discard the valid prefix.
				if e = stage(); e != nil {
					report(e, 422)
					return
				}
				if readErr != io.EOF {
					report(readErr, 400)
					return
				}
				break
			}
			batch = append(batch, frame)
			size += len(frame.raw)
			if len(batch) >= 1024 || size >= 4<<20 {
				if e = stage(); e != nil {
					report(e, 422)
					return
				}
			}
		}
		if e = h.Store.Flush(); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Trunk-Acknowledgement", "durable-staging")
		w.Header().Set("Trunk-Received", fmt.Sprint(count))
		_ = json.NewEncoder(w).Encode(map[string]int{"received": count})
		return
	}
	if !strings.EqualFold(r.Header.Get("Accept"), "application/pil") {
		http.Error(w, "Accept: application/pil required", 406)
		return
	}
	for _, target := range targets {
		if target.api.Store.layout.Adaptive() && !hasAdaptive(r) {
			http.Error(w, "adaptive-integers feature required", 426)
			return
		}
	}
	if len(sources) > 1 {
		// The response token identifies the selected catalog; individual DEFINE/BIND
		// messages still carry real map digests. Browser cache planning compares this
		// token to detect a changed set of source definitions.
		hash := sha256.New()
		for _, target := range targets {
			hash.Write(target.api.Store.layout.Digest[:])
			hash.Write([]byte{byte(target.branch)})
		}
		w.Header().Set("Definition-Version", fmt.Sprintf("%x", hash.Sum(nil)))
	}
	w.Header().Set("Content-Type", "application/pil")
	w.Header().Set("Trunk-Completion", "body")
	w.Header().Set("Trailer", "Trunk-Status")
	w.Header().Set("Vary", "Accept, PIL-Version, PIL-Features, Definition-Version, Definition-Known")
	out := bufio.NewWriterSize(w, 128<<10)
	_, e = out.Write([]byte{'T', 'P', 'I', 'L', 2})
	// Each binding carries its immutable definition digest, including historical
	// minors. No source frame is relabeled with the preferred current definition.
	emitted := map[[32]byte]bool{}
	for i, target := range targets {
		if e != nil {
			break
		}
		source, b := target.api, target.branch
		layout := source.Store.layout
		digest := fmt.Sprintf("%x", layout.Digest)
		if !emitted[layout.Digest] && !strings.Contains(","+known+",", ","+digest+",") {
			e = writeDefine(out, layout)
		}
		emitted[layout.Digest] = true
		if e == nil {
			e = writeBind(out, uint32(i), layout.Digest, source.tap(b))
		}
	}
	if e == nil {
		_, e = out.Write([]byte{255, 254})
	}
	if e == nil {
		e = out.Flush()
	}
	if e != nil {
		w.Header().Set("Trunk-Status", "error")
	} else {
		w.Header().Set("Trunk-Status", "ok")
	}
}
func writeDefine(out io.Writer, l *Layout) error {
	return writeControl(out, 1, defineControl, bytesValue(l.Digest[:]), bytesValue(l.Bytes), bytesValue(l.Bytes))
}
func writeBind(out io.Writer, id uint32, digest [32]byte, tap string) error {
	return writeControl(out, 2, bindControl, Uint(uint64(id), 4), bytesValue(digest[:]), Text(tap))
}
