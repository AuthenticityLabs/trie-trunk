package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/search"
	"trie-trunk/go/server/store"
)

type schemaRequest struct {
	Tap   string   `json:"tap"`
	Known []string `json:"known"`
}

type schemaBinding struct {
	Tap        string            `json:"tap"`
	Version    uint32            `json:"version"`
	Digest     string            `json:"digest"`
	Dictionary *codec.Definition `json:"dictionary,omitempty"`
}

type schemaResponse struct {
	Negotiation string          `json:"negotiation"`
	Schemas     []schemaBinding `json:"schemas"`
}

func (h *Handler) schemaToken(tap string, catalogs []store.Catalog) string {
	mac := hmac.New(sha256.New, h.schemaKey[:])
	fmt.Fprintf(mac, "%s\x00", tap)
	for _, catalog := range catalogs {
		fmt.Fprintf(mac, "%s\x00%d\x00", catalog.Tap, catalog.Revision.Version)
		mac.Write(catalog.Revision.Digest[:])
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *Handler) negotiate(w http.ResponseWriter, r *http.Request) {
	var request schemaRequest
	if err := decodeRequest(r.Body, &request); err != nil {
		http.Error(w, "invalid schema negotiation: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.Tap != "" && store.ValidateTap(request.Tap) != nil {
		http.Error(w, "invalid tap", http.StatusBadRequest)
		return
	}
	known := make(map[string]bool, len(request.Known))
	for _, digest := range request.Known {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			http.Error(w, "invalid dictionary digest", http.StatusBadRequest)
			return
		}
		known[hex.EncodeToString(decoded)] = true
	}
	catalogs, err := store.ReadCatalogs(r.Context(), h.Root, request.Tap)
	if err != nil {
		http.Error(w, "schema negotiation failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	browser := r.Header.Get("PIL-Version") == "2"
	if strings.EqualFold(r.Header.Get("Accept"), search.PILSchemas) {
		w.Header().Set("Content-Type", search.PIL)
		version := "1"
		if browser {
			version = "2"
		}
		w.Header().Set("PIL-Version", version)
		if browser {
			w.Header().Set("Trunk-Completion", "body")
		}
		w.Header().Set("Vary", "Accept, PIL-Version")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Trunk-Negotiation", h.schemaToken(request.Tap, catalogs))
		w.Header().Set("Definition-Version", h.schemaToken(request.Tap, catalogs))
		w.Header().Set("Trailer", "Trunk-Status")
		output := &stream{writer: w, controller: http.NewResponseController(w)}
		if err := search.PILDefinitionsVersion(r.Context(), h.Root, request.Tap, request.Known, browser, output); err != nil {
			if h.OnError != nil && r.Context().Err() == nil {
				h.OnError(err)
			}
			if !output.started {
				w.Header().Del("Trailer")
				http.Error(w, "PIL schema negotiation failed: "+err.Error(), 422)
			} else {
				w.Header().Set("Trunk-Status", "error")
			}
			return
		}
		if browser {
			if _, err := output.Write([]byte{0xfe}); err != nil {
				w.Header().Set("Trunk-Status", "error")
				return
			}
		}
		w.Header().Set("Trunk-Status", "ok")
		return
	}
	response := schemaResponse{Negotiation: h.schemaToken(request.Tap, catalogs), Schemas: make([]schemaBinding, 0, len(catalogs))}
	for i := range catalogs {
		catalog := &catalogs[i]
		digest := hex.EncodeToString(catalog.Revision.Digest[:])
		binding := schemaBinding{Tap: catalog.Tap, Version: catalog.Revision.Version, Digest: digest}
		if !known[digest] {
			binding.Dictionary = &catalog.Definition
			known[digest] = true
		}
		response.Schemas = append(response.Schemas, binding)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(response); err != nil && h.OnError != nil {
		h.OnError(err)
	}
}

func decodeRequest(reader io.Reader, value any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("expected one JSON request")
	}
	return nil
}
