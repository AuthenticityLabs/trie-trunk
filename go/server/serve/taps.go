package serve

import (
	"encoding/json"
	"net/http"

	"trie-trunk/go/server/store"
)

func (h *Handler) listTaps(w http.ResponseWriter, r *http.Request) {
	taps, err := store.ListTaps(r.Context(), h.Root)
	if err != nil {
		if h.OnError != nil {
			h.OnError(err)
		}
		http.Error(w, "tap lookup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(struct {
		Taps []string `json:"taps"`
	}{taps}); err != nil && h.OnError != nil {
		h.OnError(err)
	}
}
