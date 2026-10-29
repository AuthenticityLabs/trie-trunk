package cache

import (
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func definitionHeaders(h http.Header, l *Layout) {
	h.Set("PIL-Version", "2")
	h.Set("PIL-Features", "adaptive-integers,"+HTTPCompressionFeature)
	h.Set("Definition-Version", fmt.Sprintf("%x", l.Digest))
	h.Set("Definition-Identity", l.Definition.Identity)
	h.Set("Definition-Revision", fmt.Sprint(l.Definition.Version))
}
func (p *Endpoint) registration(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "HEAD", "GET", "PUT", "POST":
	default:
		w.Header().Set("Allow", "HEAD, GET, PUT, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Header.Get("PIL-Version") != "2" {
		http.Error(w, "PIL-Version: 2 required", 426)
		return
	}
	wanted, e := hex.DecodeString(r.Header.Get("Definition-Version"))
	if e != nil || len(wanted) != 32 {
		http.Error(w, "definition digest required", 400)
		return
	}
	digest := hex.EncodeToString(wanted)
	var proposed *Layout
	if r.Method == "PUT" {
		select {
		case p.registrationSlots <- struct{}{}:
			defer func() { <-p.registrationSlots }()
		default:
			http.Error(w, "registration busy", 503)
			return
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(5 * time.Minute))
		defer controller.SetReadDeadline(time.Time{})
		kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if kind != "application/pil" {
			http.Error(w, "definition map requires application/pil", 415)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxFrame))
		if e != nil {
			http.Error(w, "definition too large or unreadable", 413)
			return
		}
		proposed, e = ParseDefinition(raw)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if identity := r.Header.Get("Definition-Identity"); identity != "" && identity != proposed.Definition.Identity {
			http.Error(w, "definition identity mismatch", 400)
			return
		}
		if version := r.Header.Get("Definition-Revision"); version != "" && version != fmt.Sprint(proposed.Definition.Version) {
			http.Error(w, "definition revision mismatch", 400)
			return
		}
		if fmt.Sprintf("%x", proposed.Digest) != digest {
			http.Error(w, "definition digest mismatch", 400)
			return
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		http.Error(w, "endpoint closed", 503)
		return
	}
	api, err := p.registrationAPI(r, proposed)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if api == nil {
		if proposed == nil {
			w.WriteHeader(404)
			return
		}
		if proposed.Adaptive() && !hasAdaptive(r) {
			http.Error(w, "adaptive-integers feature required", 426)
			return
		}
		if e = p.install(proposed); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		if e = p.persistPreferences(); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		api = p.preferred[collectionKey(proposed)]
	}
	preferred := api.Store.layout
	definitionHeaders(w.Header(), preferred)
	if preferred.Adaptive() && !hasAdaptive(r) {
		http.Error(w, "adaptive-integers feature required", 426)
		return
	}
	if identity := r.Header.Get("Definition-Identity"); identity != "" && identity != preferred.Definition.Identity {
		http.Error(w, "different collection", 409)
		return
	}
	if revision := r.Header.Get("Definition-Revision"); revision != "" {
		n, e := strconv.ParseUint(revision, 10, 64)
		if e != nil || n>>20 != preferred.Definition.Version>>20 || n > preferred.Definition.Version {
			http.Error(w, "incompatible definition revision", 409)
			return
		}
	}
	if digest == fmt.Sprintf("%x", preferred.Digest) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/pil")
			_, _ = w.Write(preferred.Bytes)
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.Method == "POST" {
		http.Error(w, "acknowledgement must identify preferred definition", 409)
		return
	}
	path := filepath.Join(api.Store.collection, "producer-"+digest+".rx")
	if proposed == nil {
		raw, e := os.ReadFile(path)
		if os.IsNotExist(e) {
			w.WriteHeader(404)
			return
		}
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		proposed, e = ParseDefinition(raw)
		if e != nil || fmt.Sprintf("%x", proposed.Digest) != digest {
			http.Error(w, "invalid cached producer map", 500)
			return
		}
	}
	if _, e = CompileProjection(proposed, preferred); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	if r.Method == "PUT" {
		if e = ensureMap(path, proposed.Bytes); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
	}
	if r.Method == "HEAD" {
		w.WriteHeader(200)
		return
	}
	w.Header().Set("Content-Type", "application/pil")
	_, _ = w.Write(preferred.Bytes)
}
func hasAdaptive(r *http.Request) bool {
	return strings.Contains(","+r.Header.Get("PIL-Features")+",", ",adaptive-integers,")
}
