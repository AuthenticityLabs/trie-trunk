package json

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

const Protocol = "trie-trunk.jsonl.v1"

type Handler struct {
	Store   *store.Manager
	Timeout time.Duration
	OnError func(error)
	once    sync.Once
	rev     codec.Revision
	err     error
}

func Matches(r *http.Request) bool {
	if r.Method == http.MethodGet {
		for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
			for _, protocol := range strings.Split(header, ",") {
				if strings.TrimSpace(protocol) == Protocol {
					return true
				}
			}
		}
		return false
	}
	if r.Method != http.MethodPost {
		return false
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && (kind == "application/x-ndjson" || kind == "application/jsonl")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.websocket(w, r)
		return
	}
	defer r.Body.Close()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		http.Error(w, "JSONL requires UTF-8", http.StatusUnsupportedMediaType)
		return
	}
	rev, err := h.revision()
	if err != nil {
		h.writeError(w, err)
		return
	}
	count, err := h.append(rev, r.Body)
	w.Header().Set("Trunk-Accepted", strconv.FormatUint(count, 10))
	if err == nil {
		err = h.Store.Flush()
	}
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) append(rev codec.Revision, r io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), codec.MaxFramePayload+2)
	var count uint64
	for scanner.Scan() {
		data, err := encodeJSON(scanner.Bytes())
		if err == nil {
			_, err = h.Store.Append(rev, data)
		}
		if err != nil {
			return count, err
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("%w: unreadable or oversized JSONL line", store.ErrInvalid)
	}
	return count, nil
}

func (h *Handler) websocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Protocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(codec.MaxFramePayload + 2)
	send := func(op string) error {
		ctx, cancel := context.WithTimeout(r.Context(), h.Timeout)
		defer cancel()

		return c.Write(ctx, websocket.MessageText, []byte(`{"op":"`+op+`"}`))
	}
	fail := func(err error) {
		if h.OnError != nil {
			h.OnError(err)
		}
		_ = send("error")
	}
	rev, err := h.revision()
	if err != nil {
		fail(err)
		return
	}
	if err = send("ready"); err != nil {
		return
	}
	for {
		ctx, cancel := context.WithTimeout(r.Context(), h.Timeout)
		typ, data, err := c.Read(ctx)
		cancel()
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			fail(store.ErrInvalid)
			return
		}

		if len(data) == 0 {
			if err = h.Store.Flush(); err != nil {
				fail(err)
				return
			}
			if err = send("flushed"); err != nil {
				return
			}
			continue
		}
		if _, err = h.append(rev, bytes.NewReader(data)); err != nil {
			fail(err)
			return
		}
	}
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrInvalid), errors.Is(err, codec.ErrFrameSize):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrLimit):
		status = http.StatusServiceUnavailable
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
	}
	if h.OnError != nil {
		h.OnError(err)
	}
	http.Error(w, http.StatusText(status), status)
}
