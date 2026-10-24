package cache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Inspection uses JSON text for the single-event display edge. Keep its numeric
// literals inside a string so browser JSON parsing cannot round U64/I64 values.
func (h *HTTP) rejectionsHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("format") == "availability" {
		if len(q) != 1 || len(q["format"]) != 1 {
			http.Error(w, "availability is an unfiltered presence check", 400)
			return
		}
		sources := h.sources
		if len(sources) == 0 && h.Store != nil {
			sources = []*HTTP{h}
		}
		available := false
		for _, source := range sources {
			found, err := source.Store.hasRejections()
			if err != nil {
				http.Error(w, "could not check alert availability", 500)
				return
			}
			if found {
				available = true
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"available": available})
		return
	}
	inspection := q.Get("format") == "inspection"
	if q.Get("format") != "" && !inspection {
		http.Error(w, "unknown rejection format", 400)
		return
	}
	for key := range q {
		if key != "format" && key != "tap" && key != "definition" {
			http.Error(w, "unsupported rejection parameter", 400)
			return
		}
	}
	sources := h.sources
	if len(sources) == 0 {
		sources = []*HTTP{h}
	}
	type target struct {
		api      *HTTP
		branches map[string]bool
	}
	var targets []target
	for _, source := range sources {
		if digest := q.Get("definition"); digest != "" && digest != fmtDigestNative(source.Store.layout.Digest) {
			continue
		}
		branches, err := source.selectedBranches(q.Get("tap"))
		if err != nil {
			continue
		}
		allowed := map[string]bool{}
		for _, branch := range branches {
			allowed[branch.Name()] = true
		}
		targets = append(targets, target{source, allowed})
	}
	if len(targets) == 0 && (q.Get("tap") != "" || q.Get("definition") != "") {
		http.Error(w, "unknown rejection source", 404)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Trailer", "Trunk-Status")
	if inspection {
		w.Header().Set("Trunk-Completion", "body")
	}
	encoder := json.NewEncoder(w)
	count := 0
	var err error
	for _, target := range targets {
		source := target.api
		err = source.Store.Rejections(func(record Rejection) error {
			if err := r.Context().Err(); err != nil {
				return err
			}
			if !target.branches[record.Branch] {
				return nil
			}
			fields, err := source.Store.inspectRejection(record.PIL)
			if err != nil {
				fields = map[string]any{"raw_hex": fmt.Sprintf("%x", record.PIL), "decode_error": err.Error()}
			}
			if inspection {
				raw, err := json.MarshalIndent(fields, "", "  ")
				if err != nil {
					return err
				}
				branch, err := ParseBranch(record.Branch)
				if err != nil {
					return err
				}
				// The audit file still retains original PIL. The inspection view
				// transmits its derived event once, without duplicating base64.
				err = encoder.Encode(struct {
					ReceivedAt      string `json:"received_at"`
					Source          string `json:"source"`
					EventID         string `json:"event_id"`
					Definition      string `json:"definition_version"`
					FinalizedBefore string `json:"finalized_before"`
					Reason          string `json:"reason"`
					EventJSON       string `json:"event_json"`
				}{record.ReceivedAt.Format(time.RFC3339Nano), source.tap(branch), record.EventID,
					record.DefinitionVersion, record.FinalizedBefore.Format(time.RFC3339Nano), record.Reason, string(raw)})
				if err != nil {
					return err
				}
			} else {
				if err = encoder.Encode(struct {
					Rejection
					Source string         `json:"source"`
					Event  map[string]any `json:"event"`
				}{record, source.Store.binding, fields}); err != nil {
					return err
				}
			}
			count++
			return nil
		})
		if err != nil {
			break
		}
	}
	if err != nil {
		w.Header().Set("Trunk-Status", "error")
		if inspection {
			_ = encoder.Encode(map[string]any{"complete": false, "error": "Rejected-event inspection failed"})
		}
		return
	}
	w.Header().Set("Trunk-Status", "ok")
	if inspection {
		if err = encoder.Encode(struct {
			Complete bool `json:"complete"`
			Count    int  `json:"count"`
		}{true, count}); err != nil {
			w.Header().Set("Trunk-Status", "error")
		}
	}
}

// Presence only: inspect the first record boundary without decoding payloads or
// scanning an arbitrarily large audit. Do not create a log just to check it.
func (s *Store) hasRejections() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return false, err
	}
	f, err := os.Open(filepath.Join(s.dir, "lost.log"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < headerSize+8 {
		return false, err
	}
	var header [8]byte
	if _, err = f.ReadAt(header[:], headerSize); err != nil {
		return false, err
	}
	size := int64(order.Uint32(header[:4]))
	return size > 0 && size <= rejectionLimit && info.Size() >= headerSize+8+size, nil
}
