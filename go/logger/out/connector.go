package out

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"trie-trunk/go/codec"
)

// Connection is the transport interface. Each instance is one ordered logical
// stream, not an HTTP/3 connection pooled across unrelated log streams.
//
// Negotiate compares the peer's revision with wanted. A matching reply MUST bind
// that exact schema to this stream. A mismatch triggers PushSchema, whose reply
// must acknowledge installation AND binding before any records can be written.
// Control exchanges must be distinguished from record data by the adapter.
// PushSchema receives a length-prefixed CBOR definition; hash/parse its payload.
//
// WriteFrame sends one complete [uint16 big-endian length][CBOR payload] frame.
// The prefix counts only payload bytes. Adapters must send the entire frame
// verbatim, without adding another prefix, even when using WebSocket messages.
// Implementations must neither mutate nor retain data after returning and must
// treat short/incomplete sends as errors. They must bound I/O with transport deadlines
// and report all failures. The writer intentionally adds no per-record timers.
type Connection interface {
	Negotiate(ctx context.Context, wanted codec.Revision) (codec.Revision, error)
	PushSchema(ctx context.Context, wanted codec.Revision, frame []byte) (codec.Revision, error)
	WriteFrame(frame []byte) error
	Close() error
}

type Connector interface {
	Connect(context.Context) (Connection, error)
}
type ConnectFunc func(context.Context) (Connection, error)

func (f ConnectFunc) Connect(ctx context.Context) (Connection, error) { return f(ctx) }

type WebSocketConnector struct {
	URL        string
	Token      string
	HTTPClient *http.Client
	Timeout    time.Duration
}

func (c WebSocketConnector) Connect(ctx context.Context) (Connection, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	if timeout < 0 {
		return nil, fmt.Errorf("invalid timeout")
	}
	headers := http.Header{}
	if c.Token != "" {
		headers.Set("Authorization", "Bearer "+c.Token)
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	socket, response, e := websocket.Dial(dialCtx, c.URL, &websocket.DialOptions{HTTPClient: c.HTTPClient, HTTPHeader: headers, Subprotocols: []string{"trie-trunk.v1"}, CompressionMode: websocket.CompressionDisabled})
	if e != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, e
	}
	socket.SetReadLimit(1024)
	life, stop := context.WithCancel(context.Background())
	out := &connection{socket: socket, timeout: timeout, cancel: stop, rx: make(chan control, 2), done: make(chan struct{})}
	go out.read(life)
	return out, nil
}
