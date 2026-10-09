package out

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"trie-trunk/go/codec"
)

type control struct {
	Op       string         `json:"op"`
	Revision codec.Revision `json:"revision,omitempty"`
	Error    string         `json:"error,omitempty"`
}
type connection struct {
	socket  *websocket.Conn
	timeout time.Duration
	cancel  context.CancelFunc
	rx      chan control
	done    chan struct{}
	mu      sync.Mutex
	err     error
	ready   bool
}

func (c *connection) read(ctx context.Context) {
	defer close(c.done)
	for {
		typ, b, e := c.socket.Read(ctx)
		var v control
		if e == nil && (typ != websocket.MessageText || json.Unmarshal(b, &v) != nil) {
			e = fmt.Errorf("invalid ingestion control message")
		}
		if e == nil && v.Op == "error" {
			e = fmt.Errorf("ingestion rejected stream: %s", v.Error)
		}
		if e != nil {
			c.mu.Lock()
			c.err = e
			c.mu.Unlock()
			return
		}
		select {
		case c.rx <- v:
		case <-ctx.Done():
			return
		}
	}
}
func (c *connection) receive(ctx context.Context) (control, error) {
	select {
	case v := <-c.rx:
		return v, nil
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.err != nil {
			return control{}, c.err
		}
		return control{}, ErrClosed
	case <-ctx.Done():
		return control{}, ctx.Err()
	}
}
func (c *connection) send(ctx context.Context, v control) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return c.socket.Write(ctx, websocket.MessageText, b)
}
func (c *connection) Negotiate(ctx context.Context, wanted codec.Revision) (codec.Revision, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if e := c.send(ctx, control{Op: "negotiate", Revision: wanted}); e != nil {
		return codec.Revision{}, e
	}
	v, e := c.receive(ctx)
	if e != nil {
		return codec.Revision{}, e
	}
	switch v.Op {
	case "ready":
		if v.Revision != wanted {
			return codec.Revision{}, ErrSchemaAck
		}
		c.ready = true
		return v.Revision, nil
	case "schema":
		if v.Revision != wanted {
			return codec.Revision{}, ErrSchemaAck
		}
		return codec.Revision{}, nil
	default:
		return codec.Revision{}, fmt.Errorf("unexpected negotiation response")
	}
}
func (c *connection) PushSchema(ctx context.Context, wanted codec.Revision, frame []byte) (codec.Revision, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if e := c.socket.Write(ctx, websocket.MessageBinary, frame); e != nil {
		return codec.Revision{}, e
	}
	v, e := c.receive(ctx)
	if e != nil {
		return codec.Revision{}, e
	}
	if v.Op != "ready" || v.Revision != wanted {
		return codec.Revision{}, ErrSchemaAck
	}
	c.ready = true
	return v.Revision, nil
}

// WriteFrame means the transport accepted the bytes, not durable server store.
// A later rejection surfaces on a following write or Close's flush barrier.
func (c *connection) WriteFrame(b []byte) error {
	if !c.ready {
		return ErrDisconnected
	}
	c.mu.Lock()
	e := c.err
	c.mu.Unlock()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.socket.Write(ctx, websocket.MessageBinary, b)
}
func (c *connection) Close() error {
	defer c.cancel()
	defer c.socket.CloseNow()
	if !c.ready {
		return nil
	}
	c.ready = false
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	if e := c.send(ctx, control{Op: "flush"}); e != nil {
		return e
	}
	v, e := c.receive(ctx)
	if e != nil {
		return e
	}
	if v.Op != "flushed" {
		return errors.New("missing durable flush acknowledgement")
	}
	return nil
}
