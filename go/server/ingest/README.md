# Ingestion

Implemented: local and TLS WebSocket listeners, HTTP/3, immutable schema negotiation,
bounded CBOR validation, a shared file manager/writer, uuid replay checks, and
five-minute aggregate accumulators. See [storage format](../store/FORMAT.md).

External producers can also send unregistered JSONL through the separate
[`json` handler](json/README.md), on the same listener and with the same command
arguments. HTTP selects it with `Content-Type: application/x-ndjson`; WebSocket
selects subprotocol `trie-trunk.jsonl.v1`. It stores arbitrary fields as literal
CBOR population data for future classification experiments. The logger client
continues to use the CBOR protocol described below.

From the repository root (Go 1.24+ and Rust 1.88+):

```sh
make build
# Local sandbox: ws://127.0.0.1:5140/sandbox, no credentials.
./trunk ingest 5140 -dir ./taps

# Set TRUNK_TOKEN in the environment, or provide -client-ca for mTLS.
./trunk ingest 5140 -dir ./data -cert cert.pem -key key.pem
./trunk ingest 5140 -transport http3 -dir ./data -cert cert.pem -key key.pem
```

WebSocket is the default; HTTP/3 uses UDP on the same port in the last command.
Run only one writer per tap. TLS certificates must match the client hostname.
Both transports support an explicit `-host` and `-network` (tcp4/tcp6 or udp4/udp6).
With no TLS/auth settings, WebSocket runs without encryption or authentication on
loopback only: `127.0.0.1` by default, or `::1` with `-network tcp6`. An explicit
`-host localhost` uses that same address selection; other local hosts must be
literal loopback addresses. Wildcard and remote hosts are rejected in local mode.

With TLS configured, an empty host uses the platform wildcard; use explicit
listeners if dual-stack behavior must be guaranteed on a particular platform.
Both certificate and key plus a bearer token or verified client certs are required;
both authentication methods are enforced when configured together. Partial TLS/auth
settings fail instead of falling back to local mode. HTTP/3 always requires TLS.
TLS minimum is 1.3; HTTP/3 disables 0-RTT. Compression is disabled.

The command loads `.env` from the working directory and overlays `.env.sand` when
`ENVIRONMENT=sand` is selected there or in the process environment. Explicit
arguments take precedence over process environment variables, profile settings
and base file defaults. All ingest options
have `TRUNK_*` equivalents, including certificate/key paths and token/client CA.
See [environment configuration](../../cmd/trunk/README.md#environment-configuration) and the root
`.env.example`. Use `--env path` to select one file without base/profile loading.
Configuration is only read at startup.

`Handler.Run` schedules flushes every configured interval and on cancellation;
the CLI defaults to one second. Storage owns the actual commits, accumulators and
recovery. A shutdown cancels listeners and flushes accepted records.
No automatic reconnect or replay is performed by the
producer. A failed/ambiguous stream may have persisted a prefix; replay must retain
the original ids. Deduplication is scoped to a schema/day, not global across versions.

## Tap routing

Every request uses exactly `/<tap>`, for example `/json`, `/payments` or `/sandbox`.
The name is 1–64 ASCII characters: a lowercase letter or digit first, followed by
lowercase letters, digits, `.`, `_` or `-`. Empty names, deeper paths, trailing
slashes, percent escapes, backslashes and query strings are rejected with 400;
the server does not clean or redirect them. Existing symlink tap directories
are rejected. The old `/v1/stream`, `/v1/schema` and `/v1/entries` routes are removed.

After authentication and route validation, the server lazily opens or creates
`<root>/<tap>/` and binds the connection/request to that store. The method selects
the operation: GET for WebSocket, HEAD to check a CBOR schema, PUT to install it,
POST to ingest a batch. Content type/subprotocol selects JSONL or CBOR independently
of the tap name. Use `/json` to keep classification experiments in their own tap.

TLS/mTLS and token checks run before a request can select a store. Authenticated
producers may select any valid tap; there is no certificate-to-tap authorization
policy. mTLS verifies a client certificate, not ownership of the requested name.
Local plaintext mode retains the existing loopback restriction.

`Config.MaxTaps` defaults to 64 open stores per handler; excess names receive 503
without creating a directory. Open stores remain resident until `Handler.Close`.
The routing lock is used once per connection/HTTP batch, never per event. Opening
a new tap includes recovery and can delay other connections establishing taps;
existing streams continue to write to their selected stores. `Handler.Run` flushes
all open taps, and `Handler.Close` flushes and releases every writer lock.

Only the root is configured by `--dir` / `TRUNK_DIR`; producers choose the tap.
Local search uses `--tap name`, and searches all taps when omitted. The on-disk
files and existing directory names are unchanged by this terminology change.

## WebSocket protocol

GET `/<tap>`, subprotocol `trie-trunk.v1`, bearer Authorization header if used.
The Origin check retains the library's same-origin default. Native producers omit
Origin. Text messages are initialization/barrier controls; binary messages carry
one `[length:u16 big-endian][CBOR]` frame, with no extra per-entry envelope.

1. Client sends `{"op":"negotiate","revision":{"version":3145770,"digest":[...32 bytes...]}}`.
2. Server replies `ready` for the exact revision, or `schema` if not installed.
3. If requested, client sends a binary framed Definition; server installs log.map
   durably and replies `ready` with the same revision. Conflicting versions fail.
4. Each subsequent binary message carries one framed log record.
5. Client sends `{"op":"flush"}`; server syncs storage and replies `flushed`.

Errors return a text `error` control and terminate the stream. Socket write success
is not a persistence acknowledgement. The adapter exposes rejection on a later
write or Close; Close sends the flush barrier. Idle reads have a five-minute
limit by default. Connect/Reconnect renegotiates; no catalog changes midstream.

## HTTP/3 batch/stream protocol

All endpoints use `Trunk-Version: 3.42` and `Trunk-Digest: <64 hex characters>`.
Authorization is the same as WebSocket.

- HEAD `/<tap>`: 204 exact match, 404 absent, 409 version conflict.
- PUT `/<tap>`: body is exactly one framed definition; 204 means installed.
- POST `/<tap>`: body is a sequence of framed CBOR records. The schema is
  bound once in request headers. 204 after EOF means accepted records are durable;
  `Trunk-Accepted` counts frames, including replays. Stream lifetime defaults to
  five minutes. Periodic store flushes can commit records before EOF.

Earlier valid records can be accepted even if a later frame is rejected. Error
responses do not imply rollback. Batch uploads avoid per-record HTTP requests
and disk syncs. This protocol is tested over real HTTP/3, but a production HTTP/3
Connector for the logger is not included; the ready-to-use Connector is WebSocket.

## Costs and bounds

Defaults: 64 open taps and 64 active request/stream slots across the listener.
Each tap has its own bounds: 4 resident day partitions, 128 installed
schemas, 65,536 trie nodes per partition, 16,384 active aggregate groups, 100,000
index runs, and batches bounded by 1,024 records or roughly 4 MiB. Disk blocks cap
at 16 MiB, transport payloads at 65,535 bytes. Unknown fields/references, duplicate
CBOR map keys, indefinite items, tags, excessive nesting and malformed types fail.

The initial manager serializes storage mutations with one mutex. A sync can block
other writers. Replay lookup cost grows with overlapping run count; reaching its
cap returns backpressure, not unbounded memory. Run compaction, asynchronous
commit queues, one-byte path aliases, compression, schema training, automatic
higher-period rollups and remote query serving are not implemented. Explicit
rollup publication is in `store`; local read-only queries are in `search`.
Benchmark before adding the remaining features.

Storage uses maps when parsing and projecting dynamic CBOR, and a flat edge map
for shared paths. This allocates more than a schema-generated fixed decoder.
The WebSocket adapter uses one reader goroutine per connection and a bounded
control channel; there is no unbounded per-record queue. Writes have deadlines.

Official transport references: [quic-go server](https://quic-go.net/docs/http3/server/)
and [coder/websocket](https://github.com/coder/websocket).
