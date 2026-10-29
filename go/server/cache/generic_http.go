package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"bytes"
	pil "github.com/AuthenticityLabs/pill-packer/go/codec"
	"mime"
	"trie-trunk/go/codec"
	"unicode/utf8"
)

func (h *HTTP) genericIngress(w http.ResponseWriter, r *http.Request) {
	if !h.Store.generic() {
		http.Error(w, "Blind intake requires the built-in generic definition; this destination uses an explicit typed definition", 415)
		return
	}
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
	branch := Write
	if tap := r.URL.Query().Get("tap"); tap != "" {
		bs, e := h.branch(tap)
		if e != nil || len(bs) != 1 {
			http.Error(w, "unknown branch", 400)
			return
		}
		branch = bs[0]
	}
	w.Header().Set("Trunk-Acknowledgement", "durable-staging")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Header.Get("Trunk-Version") != "" || r.Header.Get("Trunk-Digest") != "" || r.Header.Get("Upgrade") != "" {
		http.Error(w, "custom CBOR protocol is unsupported; send standard CBOR or use PIL", 400)
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxFrame-128))
	if e != nil {
		http.Error(w, "Input body too large or unreadable", 413)
		return
	}
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var items []any
	switch kind {
	case "application/cbor":
		var value any
		if e = ingressDecoder.Unmarshal(raw, &value); e != nil {
			http.Error(w, "invalid CBOR item", 400)
			return
		}
		items = append(items, value)
	case "application/json", "application/jsonl", "application/x-ndjson":
		if !utf8.Valid(raw) {
			http.Error(w, "invalid JSON UTF-8", 400)
			return
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		budget := 65536
		for {
			var value any
			value, e = blindJSON(d, 0, &budget)
			if e == io.EOF {
				break
			}
			if e != nil {
				http.Error(w, "invalid JSON input", 400)
				return
			}
			items = append(items, value)
			if kind == "application/json" {
				var extra any
				if d.Decode(&extra) != io.EOF {
					http.Error(w, "single JSON item required", 400)
					return
				}
				break
			}
		}
	case "text/plain", "application/syslog":
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				continue
			}
			if len(items) >= 65536 {
				http.Error(w, "too many log lines", 400)
				return
			}
			items = append(items, map[string]any{"message": string(line)})
		}
	default:
		http.Error(w, "unsupported blind input", 415)
		return
	}
	if len(items) == 0 {
		http.Error(w, "empty input", 400)
		return
	}
	frames := make([][]byte, len(items))
	for i, value := range items {
		frames[i], e = h.Store.wrapBlind(value)
		if e != nil {
			payload, err := pil.MarshalObject(value)
			if err == nil {
				id, err := codec.NewUUIDv7(h.Store.config.Now())
				if err == nil {
					audit, err := h.Store.wrapObject(payload, id, nil)
					if err == nil {
						h.Store.mu.Lock()
						e = h.Store.rejectInput(branch, audit, "invalid_event_id", e)
						h.Store.mu.Unlock()
					}
				}
			}
			http.Error(w, e.Error(), 400)
			return
		}
	}
	var frame []byte
	received := 0
	anyDuplicate := false
	for _, frame = range frames {
		duplicate, e := h.Store.Append(branch, frame)
		if e != nil {
			status := 422
			if errors.Is(e, ErrStagingFull) {
				status = 503
				w.Header().Set("Retry-After", "1")
			}
			var late *LateEventError
			if errors.As(e, &late) {
				status = 409
			}
			h.Store.mu.Lock()
			if h.Store.failed != nil {
				status = 503
			}
			h.Store.mu.Unlock()
			http.Error(w, e.Error(), status)
			return
		}
		received++
		anyDuplicate = anyDuplicate || duplicate
	}
	if e = h.Store.Flush(); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	var f Frame
	if e = h.Store.layout.Scan(frame, &f); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	id := f.Values[0].Bytes
	text := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"event_id": text, "duplicate": anyDuplicate, "received": received})
}
