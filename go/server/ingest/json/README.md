# External JSONL ingestion

This adapter accepts JSONL from systems that do not use the logger library. It
is an input path for future realtime classification experiments. It currently
preserves arbitrary data; it does not infer business events, train dictionaries,
or assign semantic meaning to unrecognized fields.

Start the same listener with the same environment, TLS and authentication:

```sh
./trunk ingest 5140
```

No new ingest arguments, client registrations, schema uploads or revision headers
are needed. The parent ingest handler enforces authentication, concurrency limits
and deadlines for both JSONL and CBOR. The logger library continues to use CBOR.

The producer selects its tap in the URL: `https://host:5140/json` or
`wss://host:5140/json` writes under `<root>/json/`. A different single-level name
selects a different tap. The server creates the directory on first use; no tap
is selected by the environment or ingest command. Nested paths and trailing
slashes are rejected. See [tap routing](../README.md#tap-routing).

## HTTP upload

POST `/<tap>` with `Content-Type: application/x-ndjson` (also accepts
`application/jsonl`). UTF-8 is required; `charset=utf-8` is optional. This works on
the existing HTTPS listener and the HTTP/3 listener when that transport is selected.

With the sandbox mTLS certificates, from the project root:

```sh
curl --cacert certs/client-ca.pem \
  --cert certs/client.pem --key certs/client-key.pem \
  -H 'Content-Type: application/x-ndjson' \
  --data-binary @events.jsonl \
  https://localhost:5140/json
```

Token-authenticated deployments also send `Authorization: Bearer <token>`.
Local plaintext mode uses `http://127.0.0.1:5140` with no certificates.

Example `events.jsonl`:

```jsonl
{"level":"WARN","service":"legacy-payments","invoice":9007199254740993,"detail":{"retry":true}}
{"operation":"checkout","result":"declined","duration":"12ms"}
```

A successful response is `204`, with `Trunk-Accepted` giving the number of accepted
lines. Success follows a durable flush. An error may follow an accepted prefix;
its count identifies that prefix, which may still be pending a periodic flush.
An error does not roll back earlier records.

## WebSocket

Connect to `/<tap>` (for example `/json`) with this handshake header:

```http
Sec-WebSocket-Protocol: trie-trunk.jsonl.v1
```

WebSocket uses a negotiated subprotocol, not per-message HTTP Content-Type.
Both formats share the listener and URL; existing CBOR uses `trie-trunk.v1`.

1. Server sends the text response `{"op":"ready"}`. There is no schema negotiation.
2. Producer sends text messages containing one or more complete JSONL records.
   A record must not span separate WebSocket messages; protocol fragmentation
   within a message is handled by the WebSocket library.
3. Optionally send an **empty text message** to request a durable flush. The
   server replies `{"op":"flushed"}` after sync. All nonempty JSON objects are
   event data, including a record such as `{"op":"flush"}`.
4. Invalid record data receives `{"op":"error"}` and the connection ends. Earlier
   accepted records are not rolled back. Binary messages are rejected.

Socket write success and connection close are not durability acknowledgements.
The CLI periodically flushes accepted records; use the empty-message barrier
when a producer needs explicit confirmation.

## Storage and record identity

The server assigns one immutable fallback schema and reuses it across requests,
connections and restarts. Its version is the next uint32 after the highest
installed version within that tap. Concurrent registrations reuse the same definition. This is
registration of an input format, not a statistics-driven schema revision.

The envelope has `id`, `duration` and `fields`. Arbitrary source fields remain in
`fields`, encoded directly as CBOR in `log.pop`. The schema has no trie path, so
names, messages, PII and unique business values do not enter shared lineages or
value dictionaries. The existing index and aggregate formats are unchanged.

- A top-level canonical UUIDv7 string in `id` becomes the envelope id. Otherwise
  a UUIDv7 is generated at ingestion time and any source `id` stays in `fields`.
- A top-level nonnegative integer `duration` becomes the envelope duration in
  microseconds. If absent or another JSON type, envelope duration is zero and
  any original value stays in `fields`. No other timestamp or duration units
  are inferred. The store still enforces its duration and age bounds.
- Extracted metadata is removed from `fields`, avoiding duplicate storage.
  Other keys, nested objects, arrays, booleans and nulls retain their structure.
- Search displays the envelope and derives `time` from UUIDv7 as usual.
  With a generated id, this timestamp describes ingestion, not an inferred source
  transaction. The original document's timestamp fields remain available.
- Replay deduplication applies when the sender retains a valid UUIDv7. Replaying
  a document without one creates a new event, because each receive gets a new id.

For example, the second input above renders approximately as:

```json
{
  "id": "<generated UUIDv7>",
  "duration": 0,
  "fields": {
    "operation": "checkout",
    "result": "declined",
    "duration": "12ms"
  },
  "time": "<ingest time>"
}
```

## Costs and bounds

Uploads stream through one reusable line buffer. Each record temporarily needs
its decoded JSON object and its CBOR representation. JSON token parsing costs
more CPU and allocations than the existing CBOR path; this adapter does not run
on that path. Definitions are registered once on first use, not once per record.
Raw JSONL receives no dictionary/trie compression beyond CBOR encoding.

Each line and the resulting CBOR record must fit 65,535 bytes. A WebSocket
message is limited to 65,537 bytes, including newlines. HTTP batches can exceed
that size because their lines are processed individually. Bounds also apply to
nesting, map sizes and array sizes, as in storage. Blank lines, scalar/array root
values, duplicate keys, invalid UTF-8 and malformed JSON are rejected. LF, CRLF
and a final line without a newline are accepted.

Integer tokens retain full uint64/int64 precision. Fraction/exponent tokens use
float64; integers outside the supported ranges and floating-point overflow fail.
The adapter preserves JSON values, not original whitespace or number spelling.
