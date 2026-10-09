# Logger output

One package owns the complete output path:

- `writer.go`: CBOR length framing, immutable schema preparation, negotiation,
  reconnect and serialized writer lifecycle.
- `connector.go`: Connection/Connector interfaces, ConnectFunc adapter, and
  WebSocketConnector configuration and dialing.
- `stream.go`: the private WebSocket stream, control-message reader, schema
  exchange, record writes and durable close/flush acknowledgement.

```go
connector := out.WebSocketConnector{
    URL: "wss://logs.example.com:5140/v1/stream",
    Token: os.Getenv("TRUNK_TOKEN"),
}
logger, err := logging.NewStream(ctx, connector, logging.Config{
    SchemaVersion: 3<<20 | 42,
    Dictionary: dictionary,
})
// Handle err, retain the logger for the process, and check logger.Close().
```

Imports: `trie-trunk/go/logger/out`, logging `trie-trunk/go/logger/client`.
Set HTTPClient for custom root CAs or client certificates; normal TLS verification
is enabled by default. Writes send only the two-byte-prefixed CBOR frame. Close
performs a server flush barrier; socket write success alone is not a durability
acknowledgement. No automatic reconnect, retries or unbounded replay queue is added.

Logger integration remains in `logger/client/logger.go`, schema construction in
`logger/client/dictionary.go`. Output does not import logger; there is no import cycle.
