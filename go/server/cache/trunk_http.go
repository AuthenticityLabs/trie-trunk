package cache

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
)

type trunkFile struct {
	Path       string `pil:"path"`
	Kind       string `pil:"kind"`
	Definition string `pil:"definition,omitempty"`
	Digest     string `pil:"digest,omitempty"`
	Size       uint64 `pil:"size"`
	Bootstrap  bool   `pil:"bootstrap"`
}
type trunkCatalog struct {
	Scope       string      `pil:"scope"`
	Directories []string    `pil:"directories"`
	Files       []trunkFile `pil:"files"`
	Sources     []string    `pil:"sources"`
	Collections []string    `pil:"collections"`
}
type trunkSnapshot struct {
	catalog     trunkCatalog
	data        map[string][]byte
	definitions []*Layout
	bindings    []struct {
		layout *Layout
		source string
	}
}

func registeredPath(raw string) (string, bool) {
	if strings.HasPrefix(raw, "/") || strings.HasSuffix(raw, "/") {
		return "", false
	}
	if raw == "" {
		return "", true
	}
	for _, part := range strings.Split(raw, "/") {
		if !name(part) {
			return "", false
		}
	}
	return raw, true
}
func (h *HTTP) trunkSnapshot(scope string) (*trunkSnapshot, error) {
	apis := h.sources
	if len(apis) == 0 && h.Store != nil {
		apis = []*HTTP{h}
	}
	result := &trunkSnapshot{catalog: trunkCatalog{Scope: scope, Directories: []string{}, Files: []trunkFile{}, Sources: []string{}, Collections: []string{}}, data: map[string][]byte{}}
	files, dirs, sources, definitions := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[[32]byte]bool{}
	selected := []*HTTP{}
	within := func(p string) bool { return scope == "" || p == scope || strings.HasPrefix(p, scope+"/") }
	add := func(p, kind, definition string, raw []byte, size uint64, bootstrap bool) {
		if files[p] || !within(p) {
			return
		}
		files[p] = true
		digest := ""
		if bootstrap {
			sum := sha256.Sum256(raw)
			digest = fmt.Sprintf("%x", sum)
			result.data[p] = raw
			size = uint64(len(raw))
		}
		result.catalog.Files = append(result.catalog.Files, trunkFile{p, kind, definition, digest, size, bootstrap})
	}
	for _, api := range apis {
		s := api.Store
		folder := s.config.Cache + "/" + s.binding
		// A folder traversal includes descendants; a leaf traversal includes its
		// definition and bindings, without interpreting version folder names.
		if scope != "" && !within(folder) && !strings.HasPrefix(scope, folder+"/") {
			continue
		}
		selected = append(selected, api)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, fmt.Errorf("trunk source closed")
		}
		l := s.layout
		digest := fmtDigestNative(l.Digest)
		if !definitions[l.Digest] {
			result.definitions = append(result.definitions, l)
			definitions[l.Digest] = true
		}
		for b := Read; b <= Trace; b++ {
			source := api.tap(b)
			if !sources[source] {
				result.catalog.Sources = append(result.catalog.Sources, source)
				sources[source] = true
			}
			result.bindings = append(result.bindings, struct {
				layout *Layout
				source string
			}{l, source})
		}
		add(folder+"/resource.rx", "definition", digest, append([]byte(nil), l.Bytes...), uint64(len(l.Bytes)), true)
		stat, err := s.shared.Stat()
		var raw []byte
		if err == nil {
			raw = make([]byte, stat.Size())
			_, err = s.shared.ReadAt(raw, 0)
		}
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		add(folder+"/state.tri", "state", digest, raw, uint64(len(raw)), true)
		for b := Read; b <= Trace; b++ {
			for _, ext := range []string{"tri", "sum", "idx", "tvy", "pods"} {
				filename := b.Name() + "." + ext
				stat, err := os.Stat(s.dir + string(os.PathSeparator) + filename)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					s.mu.Unlock()
					return nil, err
				}
				add(folder+"/"+filename, ext, digest, nil, uint64(stat.Size()), false)
			}
		}
		// The hierarchy belongs to the cache, not to a particular definition minor.
		cachePath := s.config.Cache + "/resource.tri"
		if within(cachePath) && !files[cachePath] {
			raw, err := os.ReadFile(s.root + string(os.PathSeparator) + "resource.tri")
			if err != nil {
				s.mu.Unlock()
				return nil, err
			}
			add(cachePath, "resource", "", raw, uint64(len(raw)), true)
		}
		s.mu.Unlock()
	}
	result.catalog.Collections = collectionSources(selected)
	for _, f := range result.catalog.Files {
		for p := path.Dir(f.Path); p != "." && p != ""; p = path.Dir(p) {
			dirs[p] = true
		}
	}
	// Include empty registered folders even before their first branch seals.
	for _, api := range selected {
		p := api.Store.config.Cache + "/" + api.Store.binding
		for p != "." && p != "" {
			if within(p) || p == scope {
				dirs[p] = true
			}
			p = path.Dir(p)
		}
	}
	for p := range dirs {
		result.catalog.Directories = append(result.catalog.Directories, p)
	}
	sort.Strings(result.catalog.Directories)
	sort.Strings(result.catalog.Sources)
	sort.Strings(result.catalog.Collections)
	sort.Slice(result.catalog.Files, func(i, j int) bool { return result.catalog.Files[i].Path < result.catalog.Files[j].Path })
	sort.Slice(result.definitions, func(i, j int) bool {
		return fmtDigestNative(result.definitions[i].Digest) < fmtDigestNative(result.definitions[j].Digest)
	})
	sort.Slice(result.bindings, func(i, j int) bool {
		a, b := result.bindings[i], result.bindings[j]
		return a.source+fmtDigestNative(a.layout.Digest) < b.source+fmtDigestNative(b.layout.Digest)
	})
	if scope != "" && len(selected) == 0 {
		return nil, os.ErrNotExist
	}
	return result, nil
}
func trunkRequest(r *http.Request) bool {
	return r.URL.Path == "/trunk" || strings.HasPrefix(r.URL.Path, "/trunk/")
}
func (h *HTTP) trunkHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	if r.URL.EscapedPath() != r.URL.Path || r.URL.RawQuery != "" {
		http.Error(w, "invalid trunk path", 400)
		return
	}
	scope := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/trunk"), "/"), "/")
	if _, ok := registeredPath(scope); !ok {
		http.Error(w, "invalid trunk folder", 400)
		return
	}
	if accept := r.Header.Get("Accept"); accept != "application/pil" {
		http.Error(w, "application/pil required", 406)
		return
	}
	snapshot, err := h.trunkSnapshot(scope)
	if os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	catalog, err := encoder.Marshal(snapshot.catalog)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	version := sha256.Sum256(catalog)
	w.Header().Set("Content-Type", "application/pil")
	w.Header().Set("PIL-Version", "2")
	w.Header().Set("Trunk-Format", "trunk/0")
	w.Header().Set("Trunk-Completion", "body")
	w.Header().Set("Definition-Version", fmt.Sprintf("%x", version))
	w.Header().Set("Cache-Control", "no-store")
	// Bootstrap artifacts and definitions are sent uncompressed. Each control is
	// bounded independently, so a full catalog need not fit one giant PIL record.
	if _, err = w.Write([]byte{'T', 'P', 'I', 'L', 2}); err != nil {
		return
	}
	for _, l := range snapshot.definitions {
		if err = writeDefine(w, l); err != nil {
			return
		}
	}
	for i, b := range snapshot.bindings {
		if err = writeBind(w, uint32(i), b.layout.Digest, b.source); err != nil {
			return
		}
	}
	for _, f := range snapshot.catalog.Files {
		if !f.Bootstrap || f.Kind == "definition" {
			continue
		}
		raw := snapshot.data[f.Path]
		for at := 0; at < len(raw); {
			if r.Context().Err() != nil {
				return
			}
			end := min(at+(4<<20), len(raw))
			message, e := encoder.Marshal(map[string]any{"kind": "file", "path": f.Path, "offset": uint64(at), "bytes": raw[at:end]})
			if e != nil {
				return
			}
			if err = writeControl(w, 10, trunkControl, bytesValue(message)); err != nil {
				return
			}
			at = end
		}
	}
	message, err := encoder.Marshal(map[string]any{"kind": "catalog", "catalog": snapshot.catalog})
	if err != nil {
		return
	}
	if err = writeControl(w, 10, trunkControl, bytesValue(message)); err != nil {
		return
	}
	_, _ = w.Write([]byte{255, 254})
}

// Files are selected by their registered artifact paths. A URL never becomes
// an arbitrary filesystem lookup, even when the hierarchy gains more levels.
func (h *HTTP) trunkFileHTTP(w http.ResponseWriter, r *http.Request) bool {
	relative := strings.TrimPrefix(r.URL.Path, "/")
	if _, ok := registeredPath(relative); !ok {
		return false
	}
	if !strings.HasSuffix(relative, "/resource.rx") && !strings.HasSuffix(relative, "/resource.tri") && !strings.HasSuffix(relative, "/state.tri") {
		return false
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return true
	}
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		http.Error(w, "invalid artifact path", 400)
		return true
	}
	snapshot, err := h.trunkSnapshot(path.Dir(relative))
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	raw, ok := snapshot.data[relative]
	if !ok {
		http.NotFound(w, r)
		return true
	}
	sum := sha256.Sum256(raw)
	w.Header().Set("ETag", fmt.Sprintf("\"%x\"", sum))
	w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	w.Header().Set("Content-Type", "application/octet-stream")
	if strings.HasSuffix(relative, ".rx") {
		w.Header().Set("Content-Type", "application/pil")
	}
	_, _ = w.Write(raw)
	return true
}
