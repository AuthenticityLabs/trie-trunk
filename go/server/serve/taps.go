package serve

import (
	"encoding/json"
	"net/http"

	"trie-trunk/go/server/store"
)

func (h *Handler) listCaches(w http.ResponseWriter, r *http.Request) {
	taps, err := store.ListTaps(r.Context(), h.Root)
	if err != nil {
		if h.OnError != nil {
			h.OnError(err)
		}
		http.Error(w, "cache lookup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	key := "caches"
	if r.URL.Path == "/taps" {
		key = "taps" // Compatibility response for older clients.
	}
	if err := json.NewEncoder(w).Encode(map[string][]string{key: taps}); err != nil && h.OnError != nil {
		h.OnError(err)
	}
}
