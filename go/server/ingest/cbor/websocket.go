package cbor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
	"trie-trunk/go/codec"
)

func (h *Handler) websocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Protocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(codec.MaxFramePayload + 2)
	ctx := r.Context()
	read := func() (websocket.MessageType, []byte, error) {
		readCtx, cancel := context.WithTimeout(ctx, h.Timeout)
		defer cancel()
		return c.Read(readCtx)
	}
	send := func(v control) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		writeCtx, cancel := context.WithTimeout(ctx, h.Timeout)
		defer cancel()
		return c.Write(writeCtx, websocket.MessageText, data)
	}
	fail := func(err error) {
		if h.OnError != nil {
			h.OnError(err)
		}
		_ = send(control{Op: "error", Error: cleanError(err)})
	}
	typ, data, err := read()
	if err != nil {
		return
	}
	var hello control
	if typ != websocket.MessageText || len(data) > 512 || json.Unmarshal(data, &hello) != nil || hello.Op != "negotiate" || hello.Revision.Version == 0 {
		fail(fmt.Errorf("invalid negotiation"))
		return
	}
	wanted := hello.Revision
	actual, ok := h.Store.FindRevision(wanted)
	if ok && actual != wanted {
		fail(fmt.Errorf("schema conflict"))
		return
	}
	if !ok {
		if err = send(control{Op: "schema", Revision: wanted}); err != nil {
			return
		}
		typ, data, err = read()
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			fail(fmt.Errorf("expected binary schema"))
			return
		}
		payload, err := singleFrame(data)
		if err == nil {
			err = h.Store.Install(wanted, payload)
		}
		if err != nil {
			fail(err)
			return
		}
	}
	if err = send(control{Op: "ready", Revision: wanted}); err != nil {
		return
	}
	for {
		typ, data, err = read()
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var command control
			if len(data) > 512 || json.Unmarshal(data, &command) != nil || command.Op != "flush" {
				fail(fmt.Errorf("expected flush"))
				return
			}
			if err = h.Store.Flush(); err != nil {
				fail(err)
				return
			}
			if err = send(control{Op: "flushed", Revision: wanted}); err != nil {
				return
			}
			continue
		}
		payload, err := singleFrame(data)
		if err == nil {
			_, err = h.Store.Append(wanted, payload)
		}
		if err != nil {
			fail(err)
			return
		}
	}
}
