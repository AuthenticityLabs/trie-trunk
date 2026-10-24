package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fxamacker/cbor/v2"
)

func (h *HTTP) genericIngress(w http.ResponseWriter, r *http.Request) {
	if !h.Store.generic() {
		http.Error(w, "CBOR intake requires the built-in generic definition; this destination uses an explicit typed definition", 415)
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
		http.Error(w, "CBOR body too large or unreadable", 413)
		return
	}
	if e = cbor.Valid(raw); e != nil {
		http.Error(w, "invalid CBOR item", 400)
		return
	}
	var fields map[string]cbor.RawMessage
	var identity []byte
	if decoder.Unmarshal(raw, &fields) == nil {
		identity = fields["event_id"]
	}
	frame, e := h.Store.wrapCBOR(raw, identity, nil)
	if e != nil {
		h.Store.mu.Lock()
		e = h.Store.rejectInput(branch, raw, "invalid_event_id", e)
		h.Store.mu.Unlock()
		http.Error(w, e.Error(), 400)
		return
	}
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
	_ = json.NewEncoder(w).Encode(map[string]any{"event_id": text, "duplicate": duplicate, "received": 1})
}
