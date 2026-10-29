// Package out manages framed log output, schema negotiation and transport I/O.
package out

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"trie-trunk/go/codec"
)

var (
	ErrDisconnected = errors.New("log writer is disconnected; reconnect before writing")
	ErrClosed       = errors.New("log writer is closed")
	ErrSchemaAck    = errors.New("log writer acknowledged a different schema")
)

// Writer is an io.Writer for complete, already-encoded PIL records. It has no
// queue, background goroutine, reconnect loop or replay buffer. It serializes
// connection changes and record writes under one lock.
type Writer struct {
	mu         sync.Mutex
	connector  Connector
	connection Connection
	revision   codec.Revision
	definition []byte
	closed     bool
	frame      []byte
}

func Open(ctx context.Context, connector Connector, definition codec.Definition) (*Writer, error) {
	if connector == nil {
		return nil, errors.New("log writer connector is required")
	}
	revision, data, err := definition.Prepare()
	if err != nil {
		return nil, err
	}
	data, err = codec.AppendFrame(nil, data)
	if err != nil {
		return nil, err
	}
	w := &Writer{connector: connector, revision: revision, definition: data}
	if err := w.Reconnect(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) Revision() codec.Revision { return w.revision }

// Reconnect retires the old connection and renegotiates on EVERY new one. A
// failed handshake models the writer disconnected; no records are sent until a
// subsequent successful Reconnect. The schema is immutable for Writer's life;
// changing it requires a new Writer/Logger so in-flight entries cannot drift.
func (w *Writer) Reconnect(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrClosed
	}
	if w.connection != nil {
		old := w.connection
		w.connection = nil
		if err := old.Close(); err != nil {
			return fmt.Errorf("close previous log connection: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	connection, err := w.connector.Connect(ctx)
	if err != nil {
		if connection != nil {
			err = errors.Join(err, connection.Close())
		}
		return fmt.Errorf("connect log writer: %w", err)
	}
	if connection == nil {
		return errors.New("connector returned nil connection")
	}
	ready := false
	defer func() {
		if !ready {
			_ = connection.Close()
		}
	}()
	accepted, err := connection.Negotiate(ctx, w.revision)
	if err != nil {
		return fmt.Errorf("negotiate log schema: %w", err)
	}
	if accepted != w.revision {
		// Copy only on schema transfer. A transport cannot corrupt our cached
		// definition and thereby affect the next reconnect.
		accepted, err = connection.PushSchema(ctx, w.revision, append([]byte(nil), w.definition...))
		if err != nil {
			return fmt.Errorf("push log schema: %w", err)
		}
		if accepted != w.revision {
			return ErrSchemaAck
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.connection = connection
	ready = true
	return nil
}

// Write never retries. After an ambiguous delivery failure the connection is
// retired; retrying that record could duplicate a transaction remotely.
func (w *Writer) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, ErrClosed
	}
	if w.connection == nil {
		return 0, ErrDisconnected
	}
	frame, err := codec.AppendFrame(w.frame[:0], data)
	if err != nil {
		return 0, err
	}
	// Retain only small buffers. One unusually large entry must not pin its
	// full allocation for the lifetime of a persistent connection.
	if cap(frame) <= 64<<10 {
		w.frame = frame
	} else {
		w.frame = nil
	}
	if err := w.connection.WriteFrame(frame); err != nil {
		failed := w.connection
		w.connection = nil
		return 0, errors.Join(err, failed.Close())
	}
	return len(data), nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	w.frame = nil
	if w.connection == nil {
		return nil
	}
	connection := w.connection
	w.connection = nil
	return connection.Close()
}
