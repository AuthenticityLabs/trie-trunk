# Trunk PIL search transport

This binding carries search results over the existing local Unix socket or remote
HTTP/3 and HTTP/2 listeners. It uses the Rust `pil-codec` crate for both server
encoding and CLI decoding, and the same codec compiled to WASM for the browser.
The on-disk store is unchanged.

## Exchange

1. Snoot and the CLI send a single `GET /collect?...`, requesting
   `application/pil` with `PIL-Version: 2`. The selection is in URL parameters;
   the request has no body.
2. The server responds with the selected `PIL-Version` and `Definition-Version`.
   A missing or stale catalog version causes the necessary Definition controls
   (including dictionaries) to precede events in this same response body.
3. With a matching cached version, the response starts directly with bindings
   and events. No schema preflight, redirect or second event request is needed.
4. The consumer installs each Definition before decoding its dependent frames.
   Rust expands events only at CLI JSONL/pretty output; Snoot retains PIL bytes
   through to its view components.

The worker caches definitions in memory and local storage. `Definition-Known`
can advertise immutable digests after a reload or server restart even when the
aggregate version is unavailable, avoiding retransmission of cached bodies.
A CLI invocation starts with a cold in-memory catalog; it requests definitions
inline and does not introduce a separate disk cache. `/schemas` remains an
optional discovery endpoint for the UI's pre-collection dropdown choices; a
collection does not call it or wait for it. Legacy POST endpoints remain supported.

Follow can discover a new schema after negotiation. A Definition control precedes
its first event, and that definition is retained for subsequent events. Multiple
taps and schema versions can interleave without changing field interpretation.

Finite responses require END followed by the body success marker (`254`) and
clean EOF. The CLI additionally checks `Trunk-Status: ok` in HTTP trailers. Partial frames, missing END records, unknown bindings, digest mismatches,
and failed HTTP trailers remain failures. Cancellation closes the network input
and terminates the local decoder and server search child.

## Transport choice

Local search defaults to `<dir>/.trunk/search.sock`; PIL is carried by the existing
HTTP-over-Unix-socket interface. No local TCP or QUIC connection is opened.

With `--endpoint https://host:port`, `--transport auto` prefers HTTP/3 over QUIC.
If the collection request fails before response headers arrive, auto mode retries
the read-only GET over HTTP/2.
No fallback or replay occurs after events start. Both transports retain the
existing TLS certificate validation, client identity, and bearer-token rules.

`--transport http3` requires QUIC and disables fallback. `--transport http2` uses
HTTP/2 directly. A remote HTTP/1 response is rejected. GET selections use URL parameters; result records use PIL. The binary layout is
independent of HTTP packet or stream boundaries.

## Legacy binary segment (version 1)

A version-1 response body begins with the five bytes `54 50 49 4c 01` (`TPIL`, version 1).
The following one-byte record tags are this Trunk binding's controls, not changes
to the PIL frame format:

| Tag      | Payload                                                       |
| -------- | ------------------------------------------------------------- |
| 1 DEFINE | PIL control frame: `digest BYTES`, `store BYTES`, `pil BYTES` |
| 2 BIND   | PIL control frame: `stream U32`, `digest BYTES`               |
| 3 EVENT  | Little-endian `stream U32`, followed by a PIL event frame     |
| 255 END  | No payload; ends the segment                                  |

Control Definitions are fixed by binding version 1. DEFINE supplies the original
store schema CBOR and the derived PIL Definition CBOR. The decoder verifies the
digest and checks that both definitions agree. The original schema retains nested
field/domain semantics which PIL's primitive type registry does not describe.

BIND associates a segment-local numeric handle with a previously supplied
Definition digest. A handle cannot be rebound inside its segment. A new segment
resets handles and retains known definitions. Frames have no redundant length
prefix: the mask and final cumulative End Offset delimit each frame.

The decoder limits any one frame to 64 MiB, including definition controls. This
is a per-frame safety bound,
not an event-count limit. Search collection limits and follow behavior are unchanged.

## Store-to-PIL mapping

The PIL Definition contains the original top-level fields in ascending store-key
order. The adapter maps:

- UUID-encoded fields to `UUID`, preserving all 16 bytes.
- Duration to `U64`, retaining the existing microsecond unit.
- Fields with unique enum labels to mapped `STRING`, using dense zero-based PIL
  dictionary IDs. The adapter translates the store's potentially sparse codes.
- Remaining fields to `CBOR`, preserving their original values and nested numeric
  dictionary references. Existing store schemas do not declare primitive types
  for every field, so this prototype does not guess from individual records.

Missing fields use ABSENT; explicit null uses NULL. Arbitrary strings in mapped
fields use OFFSET. Unknown enum codes, values incompatible with their preferred
PIL representation, and unknown store keys are preserved in a CBOR fallback map.
They are not coerced to strings or discarded.

Two transport fields follow the store fields: `trunk_lineage ARRAY<U16>` preserves
root-to-leaf field order, and `trunk_fallback CBOR` carries only fallback values.
Names gain trailing underscores if needed to avoid application-field collisions.
They are removed during CLI presentation; they are not attributes of the event.
The fallback is not PIL `@fields`, since it can supply values for known fields.
A field cannot appear both as a normal value and in the fallback map.

The CLI derives `time` from UUIDv7, applies the requested presentation headers,
and writes the same JSON field order as direct search. No UUID or CBOR payload is
base64-wrapped on the PIL wire. Binary values may still appear as base64 in final
JSON because that is the existing JSON representation.

## Direct store encoding

Search obtains the UUID from `log.idx`, borrows CBOR field values from `log.pop`,
reads numeric dictionary values from the cached `log.tri` lineage, and uses
`log.map` to bind these values to PIL Definition positions. Population fields are
validated and indexed as borrowed slices instead of decoded into object trees.
Trie-only and unfiltered searches build neither JSON nor owned CBOR population
objects. Population predicates still use the existing evaluator before encoding;
that evaluator may materialize values, but the output path does not reserialize
those objects into PIL.

A reusable `FrameEncoder` receives values in Definition order, retaining frame,
direct-value, offset, and payload buffer capacity between events. Stored CBOR is
copied into the frame after validation, without a CBOR decode/re-encode cycle.
This is direct encoding, not zero-copy I/O: population bytes are read into a
scratch buffer and copied into the output frame. Unusual/unknown field values
retain their encoded CBOR in the existing fallback map. No dictionary growth or
wire-format change is introduced. Canonical stored records produce the same PIL
bytes as the original adapter; other accepted CBOR representations can retain
original bytes while preserving decoded values.

Per-event lineage ordering and client-side JSON rendering remain in this binding.
The JSON decoder
still constructs intermediate CBOR/JSON values and is a separate optimization.

A local release comparison on 13,882 stored events (nine warm-cache runs, stdout
sent to `/dev/null`, including process startup) measured approximately 32 ms for
the original store-to-PIL path and 20 ms for direct encoding, versus 20 ms for
existing dictionary JSONL. Both PIL outputs were 794,641 bytes and matched
byte-for-byte; old and new decoders produced identical JSON. This is an encoding
measurement, not remote end-to-end latency or a large-production-data forecast.

An instrumented in-process pass on the same data reduced store-to-PIL allocations
from about 14.6 to 1.1 per event and reallocations from 5.7 to 0.02. These counts
include amortized catalog setup; one small borrowed-field index is still allocated
per record. Frame/payload buffers and cached lineage encodings are reused.

## Browser collection: binding version 2

Snoot and the CLI use `GET /collect`, on the same HTTPS listener with HTTP/3 (QUIC) and
HTTP/2 support. The browser chooses the network protocol; fetch cannot force
HTTP/3. The server advertises HTTP/3 with Alt-Svc. Both versions carry identical
PIL bytes and preserve the existing mTLS and bearer-token checks.

The collection request has no body. Example (URL parameters abbreviated):

```http
GET /collect?tap=demo-week&from=2026-09-19T00%3A00%3A00Z&limit=all
Accept: application/pil
PIL-Version: 2
Definition-Version: <cached catalog version, if available>
Definition-Known: <comma-separated cached SHA-256 digests, if available>
```

Supported URL parameters are `tap`, RFC 3339 `from`/`to`, repeated `where`
clauses, `limit` (unsigned count or `all`), `tail` and `follow` (`true`/`false`),
and repeated presentation `headers`. Omitting `limit` retains the API default
of 100; Snoot explicitly sends `limit=all`. An omitted `to` means now for finite
requests or an open end for follow. Unknown parameters and duplicate scalar
parameters are rejected. Legacy `POST /collect` JSON selections are still accepted
for already-open clients.

The response supplies `Content-Type: application/pil`, `PIL-Version: 2`, and
`Definition-Version: <current catalog version>`. HTTP header names and media
types are case insensitive. Here `PIL-Version` negotiates the Trunk stream binding;
the enclosed frames still use PIL 0.x. An unsupported version receives HTTP 426
with the supported version, before any event data is emitted.

`Definition-Version` is an opaque version of the selected catalog snapshot,
including all selected taps and their schema versions/digests. A single integer
would be ambiguous across taps. Individual immutable Definitions keep their own
numeric versions and SHA-256 identities. The aggregate token is server-bound;
restarting the server can require a fresh negotiation, without changing schemas.

When the header matches, the first segment contains bindings and event frames;
there is no preliminary schema request and no repeated definition payload. When
it is missing or stale, the response first supplies a Definition/binding
segment, omitting Definition bodies listed in `Definition-Known`, then the event segment, in the **same HTTP response**. `Definition-Known` is a cache hint of at most 128 immutable digests (not an access
token); omitted digests are safe to resend. Authentication and tap selection are
still checked normally. This lets locally persisted definitions survive aggregate
token changes. The worker installs and verifies definitions before reading the dependent frames. A new definition
discovered during follow is likewise introduced before its first event.

Version 2 uses `TPIL\x02`; DEFINE and EVENT retain their version-1 payloads. BIND
adds a third field, `tap STRING`, after `stream U32` and `digest BYTES`. This
preserves event identity for cross-tap deduplication, local search and notebooks.
Each segment ends in END (`255`); handles reset between segments. Definitions
remain cached across segments. A terminal byte `254` is written by the Go handler
only after the search process succeeds. The browser requires this success marker
and clean EOF, since browser fetch does not expose HTTP trailers. Canceled and
failed streams can retain complete received events but never certify coverage.

`/schemas` also supports `PIL-Version: 2`, returning version-2 bindings and the
`Definition-Version` header. The UI uses it for selector choices. `known` digests
omit dictionary bodies already held by the worker. Concurrent tabs share pending
lookups. `/schemas` and `/events` still support legacy binding version 1 when
`PIL-Version` is absent or `1`.

Definitions are cached in SharedWorker memory, keyed by immutable digest and
isolated by credentials. Negotiated versions are additionally scoped to the tap
selection. Definitions and collected events are also stored in OPFS by the local
file worker, so they survive reloads. Each tab owns its visible PIL buffers; the worker transfers ArrayBuffers with one
batch in flight and retains no second event collection. Closing/canceling one
port does not cancel another tab's collection. Browsers without SharedWorker use
the same binary worker pipeline in a dedicated worker (without cross-tab reuse).
