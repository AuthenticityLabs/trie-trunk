package pil

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/store"
)

type ReceiverStore interface {
	FindRevision(codec.Revision) (codec.Revision, bool)
	Install(codec.Revision, []byte) error
	Append(codec.Revision, []byte) (bool, error)
	Flush() error
}

type Handler struct {
	Store   ReceiverStore
	Timeout time.Duration
	OnError func(error)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.websocket(w, r)
		return
	}
	rev, err := requestRevision(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodHead:
		h.checkSchema(w, rev)
	case http.MethodPut:
		h.installSchema(w, r, rev)
	case http.MethodPost:
		h.append(w, r, rev)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) checkSchema(w http.ResponseWriter, rev codec.Revision) {
	actual, ok := h.Store.FindRevision(rev)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	revisionHeaders(w.Header(), actual)
	if actual != rev {
		w.WriteHeader(http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) installSchema(w http.ResponseWriter, r *http.Request, rev codec.Revision) {
	r.Body = http.MaxBytesReader(w, r.Body, codec.MaxFramePayload+codec.FrameHeaderSize)
	defer r.Body.Close()
	data, err := codec.ReadFrame(r.Body, codec.MaxFramePayload)
	if err == nil {
		var extra [1]byte
		n, tailErr := r.Body.Read(extra[:])
		if n != 0 || tailErr != io.EOF {
			err = fmt.Errorf("schema must contain exactly one frame")
		}
	}
	if err != nil {
		http.Error(w, "invalid schema frame", http.StatusBadRequest)
		return
	}
	if err = h.Store.Install(rev, data); err != nil {
		h.writeError(w, err)
		return
	}
	revisionHeaders(w.Header(), rev)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) append(w http.ResponseWriter, r *http.Request, rev codec.Revision) {
	defer r.Body.Close()
	actual, ok := h.Store.FindRevision(rev)
	if !ok || actual != rev {
		http.Error(w, "negotiate schema first", http.StatusConflict)
		return
	}
	var count uint64
	for {
		data, err := codec.ReadFrame(r.Body, codec.MaxFramePayload)
		if err == io.EOF {
			break
		}
		if err != nil {
			w.Header().Set("Trunk-Accepted", strconv.FormatUint(count, 10))
			http.Error(w, "invalid/truncated frame; earlier records may have been accepted", http.StatusBadRequest)
			return
		}
		if _, err = h.Store.Append(rev, data); err != nil {
			w.Header().Set("Trunk-Accepted", strconv.FormatUint(count, 10))
			h.writeError(w, err)
			return
		}
		count++
	}
	w.Header().Set("Trunk-Accepted", strconv.FormatUint(count, 10))
	if err := h.Store.Flush(); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, store.ErrLimit):
		status = http.StatusServiceUnavailable
	case errors.Is(err, store.ErrInvalid), errors.Is(err, codec.ErrFrameSize):
		status = http.StatusBadRequest
	}
	if h.OnError != nil {
		h.OnError(err)
	}
	http.Error(w, http.StatusText(status), status)
}
