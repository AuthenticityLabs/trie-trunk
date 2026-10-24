package cache

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// The catalog lists cache/service/resource.vMajor/branch source bindings.
// Keep the old route and response key for clients opened before the rename.
func catalogRequest(r *http.Request) bool {
	return r.URL.Path == "/caches" || r.URL.Path == "/taps"
}

func catalogGET(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.EscapedPath() != r.URL.Path || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	if accept != "" && accept != "*/*" && accept != "application/json" {
		http.Error(w, "unsupported response format", http.StatusNotAcceptable)
		return false
	}
	return true
}

// Replay must be enabled for every retained definition of a resource major.
func collectionSources(apis []*HTTP) []string {
	enabled := map[string]bool{}
	for _, api := range apis {
		s := api.Store
		s.mu.Lock()
		key := api.tap(Write)
		ok := s.config.ObjectIndex && s.objects != nil && s.layout.Object >= 0
		s.mu.Unlock()
		prior, exists := enabled[key]
		enabled[key] = ok && (!exists || prior)
	}
	result := []string{}
	for key, ok := range enabled {
		if ok {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func writeCatalog(w http.ResponseWriter, r *http.Request, sources, collections []string) {
	key := "caches"
	if r.URL.Path == "/taps" {
		key = "taps"
	}
	if sources == nil {
		sources = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{key: sources, "collections": collections})
}
