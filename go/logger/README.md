# trie-trunk producer

A request-scoped fluent builder with one completion record per transaction.
This package belongs to the standalone `trie-trunk/go` module. It imports
`models` and `codec`; neither depends on logging.

```go
dictionary, err := codec.NewDictionary("customers/v1", map[string]map[uint16]string{
    "service":   {1: "customers"},
    "resource":  {1: "customer"},
    "operation": {1: "lookup"},
})
// Handle err.
logger, err := logging.NewStream(ctx, connector, logging.Config{
    Dictionary: dictionary,
    Service: codec.Ref(1),
    Version: "1.0.0",
    OnError: reportDeliveryFailure,
})
// Handle err. Initialization returns only after schema acknowledgement.
defer logger.Close()

handler := logger.Handle(func(w http.ResponseWriter, r *http.Request) error {
    logging.FromContext(r.Context()).
        Resource(1).
        Operation(1).
        Field("account_id", codec.String(accountId))

    if invalid {
        return models.New(models.INVALID_INPUT)
    }
    _, err := w.Write(response)
    return err
})
```

Imports use `trie-trunk/go/logger/client` as `logging`,
`trie-trunk/go/models` as `models`, and `trie-trunk/go/codec`. Prefer named constants of `ServiceId`, `ResourceId`, `OperationId`,
`CheckpointId`, and `MessageId` in application code. Their catalogs are local to
the named application dictionary. See `client/examples/http/main.go` for a runnable
local-output example (`go run ./logger/client/examples/http` from the module root).

## Package boundaries

`logger/client` contains Logger, its builder, and optional HTTP instrumentation;
`logger/out` owns outbound framing and transport. Import the client as `logging`.
`logger/client` owns dictionary types, constants, BuildSchema and BuildDefinition directly.
Applications use one logging package; server code imports it for the same wire
contract. There are no vocabulary aliases or forwarding functions.
Other language runtimes consume the negotiated serialized definition; importing
Go models does not itself share executable code across languages.

## Connection initialization and transports


`NewStream(ctx, connector, config)` uses `out.Connector`. The connector
creates a `out.Connection`, the adapter seam for WebSocket or HTTP/3:

```go
type Connection interface {
    Negotiate(context.Context, codec.Revision) (codec.Revision, error)
    PushSchema(context.Context, codec.Revision, []byte) (codec.Revision, error)
    WriteFrame([]byte) error
    Close() error
}
```

Initialization prepares the full definition and digest once, opens a connection,
and asks the peer to compare revisions. A matching revision binds that definition
to this stream without sending it again. A mismatch sends the definition and
waits for acknowledgement of the exact revision. Only then can records flow.
Version plus content digest catches changed application values even if the
numeric schema version was not bumped. `logging.BuildDefinition(dictionary)` includes
the field schema, time units, built-in enums/messages, nested error/checkpoint
schemas, and all application dictionary domains.

`logger.Reconnect(ctx)` repeats negotiation on every newly opened connection.
Each write is **a two-byte big-endian payload length followed by one CBOR entry**.
The length excludes the prefix and permits 1–65,535 payload bytes. The CBOR
payload contains numeric field keys,
dictionary value references, and remaining literal data. There is no repeated
schema version, dictionary name, digest, or control envelope in those entries.
Schema uploads use the same length prefix. Oversized entries or definitions
return `codec.ErrFrameSize`; no truncation or automatic chunking occurs.
`WriteFrame` adapters send the frame verbatim and report incomplete writes; they
must not prefix it a second time, even when using WebSocket message boundaries.
Schema control exchanges are separate transport messages; a transport adapter
must preserve their ordering ahead of data. With HTTP/3 this binding is per
ordered logical log stream, not an entire multiplexed connection.

The connection state machine has local stub tests, and `out.WebSocketConnector`
provides a real binary WebSocket transport. See [connector](out/README.md)
and [ingestion](../server/ingest/README.md). HTTP/3 ingestion accepts framed request
streams; a producer HTTP/3 Connector is not yet implemented. The adapter must honor initialization contexts and bound writes
and closes with transport deadlines. Close/reconnect wait for any active write.

There is no automatic reconnect loop or replay. A write error retires the
connection; subsequent writes report `ErrDisconnected` until explicit reconnect.
An ambiguous failed write is not retried. Initial/schema-sync failures return an
error without exposing a usable logger. Catalogs remain immutable for a logger's
lifetime; a changed catalog gets a new logger/session to isolate in-flight work.

`logging.New(io.Writer, config)` remains the local file/stdout/testing path and
does not perform negotiation. Its compact records also omit schema metadata;
save the definition separately if those records will be decoded later. The
receiver must retain the stream-to-definition association when storing records.

## Builder lifecycle

For non-HTTP work:

```go
ctx, entry := logger.Begin(ctx)
// Pass ctx to request-owned work and enrich through FromContext(ctx).
entry.Service(1).Resource(1).Operation(1).HttpMethod(logging.GET)
entry.HttpStatus(200).ResponseBytes(0)
err := entry.Finish()
```

- Middleware owns `Finish` for HTTP; handlers must not finish early.
- Every setter is synchronized. Chains are not atomic groups. Join request-owned
  goroutines before returning. Competing setters for the same field use the last
  completed setter; use distinct fields for concurrent branch results.
- `Finish` seals the entry and performs at most one writer call, including on
  failure. Concurrent/repeated calls return the original result. Late setters
  do nothing; `Check` reports `ErrFinalized`. This prevents an escaped builder
  from modifying memory handed to the encoder.
- `Snapshot` copies mutable containers. It is an inspection API, not a hot-path
  requirement. Error objects and extension values are immutable.
- No attached context means a no-op builder, replacing the role of `VoidLogger`.
- `IfDebug().FieldLazy(...)` skips evaluation when disabled. Ordinary argument
  expressions are evaluated by Go even when the receiving builder is disabled.
- Conditional detail settings never suppress the final completion record.
- Background work needs a new transaction linked explicitly to its parent.
  No goroutine-local or thread-local storage is used.

For lean checkpoints:

```go
start := time.Now()
// Work.
entry.Checkpoint(DatabaseLookup, time.Since(start))
```

`StartCheckpoint` also returns an idempotent stop function for convenience. That
function allocates; the duration-taking method avoids the closure. Repeated
checkpoint names aggregate duration and count in a bounded slice.

## Wire contract

Schema version 5 moves `message` out of the trie path and into the per-event
population (`log.pop`). Built-in and application message dictionary references
remain supported. Custom `SchemaVersion` values must advance for this layout
change; existing stored definitions remain immutable.

Schema version 4 names built-in JSON fields and error dictionary domains in
snake_case. HTTP headers remain dash-separated and numeric field keys are
unchanged. Custom SchemaVersion values must advance when adopting this changed
definition; existing versions are immutable. Application extension keys are
preserved exactly as supplied; use snake_case when defining them.

Schema version 3 stores `id` as a 16-byte UUIDv7 at key 8 and removes the separate
start time. Version 2 moved the version/dictionary metadata to initialization.
Per-record keys 0, 1, and 11 are retired; other assignments retain their numbers.
`client/keys` contains immutable entry field numbers; import it as `Keys` to
write `Keys.Category`, `Keys.Duration`, etc. `client/dictionary.go` defines
categories, levels, methods and standard messages. Never renumber or reuse
existing assignments. Retired slots 0, 1 and 11 are reserved only in a comment;
there are no schema, dictionary or start-time attributes on an entry.
Known log field keys are currently all in the one-byte CBOR range 2–23.

`Entry` is the only transaction representation. Compact JSON and CBOR append
directly from its fields into one output buffer; errors and checkpoints are
written in place without intermediate structs or checkpoint slices. Duration
conversion to microseconds happens during writing. Only dynamic extension
values use the general serializer. Readable JSON and explicit `Record()`
inspection build expanded maps outside the compact path.
Application extension fields allocate their map only on first use.

- `ReadableJSON`: expanded names, newline-delimited; intended for inspection.
- `CompactJSON`: decimal string keys and numeric references, newline-delimited.
- `CBOR`: integer keys and values. Remote streams use the two-byte length prefix;
  `codec.ReadFrame(reader, maxPayload)` reads one payload even across fragmented
  network reads, checking size before allocation. Pass that payload to
  `codec.Decode`. Local `New(io.Writer, ...)` and `Entry.Encode` retain raw CBOR
  output without transport framing. No newline or base64 wrapping is added.
- The model has `entry.id` and `entry.duration`, with no start/end-time fields or
  derived-time methods. The log-view client interprets the UUIDv7 timestamp and
  duration. UUIDv7 carries milliseconds; duration encodes integer microseconds.
  The producer uses a private monotonic stopwatch solely to measure duration.
- The Id is generated once at `Begin`. CBOR carries 16 binary bytes; JSON and
  the HTTP correlation header use canonical UUID text. The Id is immutable
  through the builder and survives snapshots, finalization, and re-encoding.
- Future replay must reuse the original Id/record; creating another transaction
  generates a different Id. The remote writer must deduplicate by Id, for example
  with a unique storage key. An Id supplies that key, not exactly-once delivery
  by itself. Automatic replay remains disabled.
- UUIDv7 uses 74 random bits per entry, without a global counter or generator
  lock. Ordering within the same millisecond is not guaranteed. Entropy or
  timestamp failures surface through `Check`/`Finish` and prevent record emission.
- Response status/byte-count presence is explicit, so a measured zero-byte
  response survives encoding. Unset string fields are omitted; dictionary-backed
  explicit empty text remains present.
- Unknown enum numbers remain numbers in readable output. Unregistered names
  use `ServiceText`, `ResourceText`, `OperationText`, `HttpMethodText`, or
  `MessageText`. `Field` accepts unregistered **field names**; variable values in
  known fields do not require maps.
- Core messages reserve 1–127; application messages start at 128. Other domains
  such as service/resource/operation may begin at 1. The source and destination
  agree on the exact catalog through connection initialization. Catalogs do not
  change or grow while records are flowing. Typed constants select references;
  explicit text setters supply literal fallbacks, without scanning or interning
  arbitrary request values on the producer path.

## HTTP behavior and limits

`client/http.go` is optional instrumentation for an existing application server; it
never opens a listener or starts a server. `out` owns outbound log transport.
`Handle` accepts error-returning handlers; `Middleware` wraps `http.Handler`.
Both attach the builder, capture final response status/bytes, and finish once.
Known errors use their definitions, unknown errors become SYSTEM/500, and bare
4xx/5xx responses get summary errors. Intended error status and actual committed
response status remain distinct. Panics before commit produce a safe 500;
panics after commit record the failure then abort the stream. `ErrAbortHandler`
continues to abort. Context cancellation is recorded even if the handler returns
nil. Process exit or fatal runtime failures cannot guarantee a completion record.

`Flusher`, `Hijacker`, and `Pusher` availability is preserved, with `Unwrap` for
`http.ResponseController`. After hijacking, arbitrary connection traffic is not
counted; completion means the handler returned. Streaming requests emit their
summary when the handler exits. ResponseWriter remains single-owner, as in
`net/http`. The wrapper does not preserve `io.ReaderFrom`/sendfile optimization:
copying through `Write` keeps byte accounting correct. Revisit that adapter only
if large-response benchmarks justify its complexity.

The `transaction-id` HTTP header carries the same UUIDv7 as `entry.id`.
Inbound correlation uses `client-transaction-id` and `client-id`. Header case is
insensitive; the old undashed names are no longer read or emitted. CBOR keys are unchanged.
Request bodies, response bodies, and query strings are
not retained. Client IP comes from the network peer; forwarded headers require a
separate trusted-proxy policy.

Default extension limits are 64 top-level fields, 32 checkpoints, 4,096 UTF-8
bytes per text value/key, eight nesting levels, and 64 items per nested container.
Oversized nested objects become a truncation marker; lists are truncated. These
are per-container limits, not a total entry-byte budget. Keep extensions small.
Top-level rejected fields/checkpoints increment `dropped`. Password, pin, ssn,
and authorization keys are masked case-insensitively, including nested objects.
`SensitiveFields` adds names. This is key-based masking, not free-text scrubbing.

Internal error/panic text is off by default. `Diagnostics` explicitly enables
bounded text; application messages and diagnostic text must be chosen with care.
No automatic stack capture occurs. Sink and response-write errors go to
`Config.OnError`; configure it once to observe HTTP-path delivery failures.
`Handle(handler)` and `Middleware(handler)` inherit it; optional `HTTPOptions`
override it when registering a handler. An empty override disables reporting.

## Performance policy

See `PERFORMANCE.md` for measured costs, omissions, and follow-up criteria.
The writer is synchronous and serialized per Logger/session. This keeps memory bounded
without an internal queue or goroutine, but a slow sink delays completion.
Multiple Logger instances sharing one non-thread-safe writer require external
coordination. Exactly-once storage delivery is not promised; failed writes are
reported and not automatically retried.

## Validation

```sh
cd go
go test -race ./...
go vet ./...
go test ./logger/client -run TestEncodingSizes -v -bench . -benchmem
```

The module requires Go 1.24 or later. The module defaults to readable JSON when
Format is omitted in `New`; `NewStream` always uses CBOR.
