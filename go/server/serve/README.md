# Event stream server

```sh
./trunk ingest 5410 serve 443
./trunk search now-15m --where 'service=="vault"' -f
```

`serve` listens on TCP and UDP on the same port: HTTP/2 over TLS, HTTP/3 over
QUIC, and HTTP/1.1 compatibility. The TCP endpoint advertises HTTP/3 with
`Alt-Svc`. TLS 1.3 is required. `/events` requires a bearer token or verified
client certificate; when both methods are configured, both are required.

The built Solid app is served at `/` from `./web`. Run `make web` to build it from
`client`. `TRUNK_WEB` or `serve --web` selects a different directory; an empty
value disables static serving. Missing files return 404. The app shell does not
require a bearer token, but configured mTLS applies to the entire listener,
including browser requests for the app. Content-hashed `/assets/` files (including
WASM and worker scripts) are cached for one year with `immutable`; the HTML shell
and other static files revalidate before reuse. Failed static requests are not
cached. New builds change asset URLs only when their contents change. See [client setup](../../../client/README.md).

Configuration uses the existing `.env` / `.env.<ENVIRONMENT>` loader. Server
certificates use `TRUNK_CERT`, `TRUNK_KEY`, and `TRUNK_CLIENT_CA`; bearer access uses
`TRUNK_TOKEN`. `TRUNK_HOST` defaults to loopback for `serve`, and
`TRUNK_SERVE_PORT` defaults to 443. Ingest retains its separate `TRUNK_PORT`.
`TRUNK_TRANSPORT` and `TRUNK_NETWORK` only apply to ingestion. Serving always binds
both TCP and UDP. Flags belong to the preceding command; shared settings normally
come from the environment file.

Port 443 may require elevated bind permission on the host. For an unprivileged
development account, use `./trunk ingest 5410 serve 8443`; local socket searches
remain unchanged. Both listeners keep TLS and authentication enabled.

## Tunnel origin

`serve --http-address 127.0.0.1:3020` (or `TRUNK_HTTP_ADDRESS`) adds a plain HTTP
listener for a trusted local reverse proxy such as Cloudflare Tunnel. It serves
Snoot and the streaming APIs with the same query limits and definition negotiation.
The address must use a literal loopback IP; the listener is disabled by default.

The tunnel route uses `http://localhost:3020`, while browsers use
`https://snoot.auth.enti.city`. The proxy owns public TLS and access control on
this path: the origin cannot require a TLS client certificate over HTTP. If
`TRUNK_TOKEN` is configured, APIs still require it. This listener does not advertise
the direct server's HTTP/3 port; public HTTP/2 and HTTP/3 belong to Cloudflare.
Direct HTTPS/mTLS on 8443 and the private Unix socket keep their existing behavior.

## Local access

The server also opens `<TRUNK_DIR>/.trunk/search.sock`. Its directory must be
private (0700) and the socket is 0600. The local socket relies on filesystem access
and does not require TLS, token, or client certificate. It shares the remote
endpoint's query concurrency limit. An existing socket is never overwritten;
normal shutdown removes the socket. After an unclean exit, remove the stale socket
only after verifying that no server owns it.

`trunk search` uses that socket unless `--endpoint` is supplied. `TRUNK_SOCKET`
or `--socket` selects another path inside a private directory. Paths must fit the
operating system's Unix socket path limit. `--dir` locates the default socket; it
cannot change the server's configured data root. There is no silent fallback to
file access if connecting fails. `search --local` explicitly reads files directly.

```sh
./trunk search --endpoint https://localhost:443 \
  --ca certs/client-ca.pem \
  --client-cert certs/client.pem --client-key certs/client-key.pem \
  now-15m --tap payments --pretty
```

The CLI uses HTTP/2 for HTTPS connections when negotiated. HTTP/3 clients can use
the same endpoint over UDP. `--ca`, `--client-cert`, and `--client-key` also accept
`TRUNK_CA`, `TRUNK_CLIENT_CERT`, and `TRUNK_CLIENT_KEY`. The endpoint can be configured
with `TRUNK_ENDPOINT`. Server certificates are always verified; no insecure option
is provided.

## Tap listing

`GET /taps` returns the available tap directory names in alphabetical order:

```json
{"taps":["billing","demo-week","sandbox"]}
```

The endpoint uses the same TLS, bearer authentication and local-socket rules as
`/events`. It accepts `Accept: application/json` and requires no request body or
Content-Type. Only valid, non-symlink tap directories are listed; hidden folders
and ordinary files are excluded. Empty or not-yet-created stores return
`{"taps":[]}`. Empty tap directories remain selectable.

Listing taps does not read dictionaries, indexes or events, and does not consume
a search slot. There is no pagination or per-tap authorization yet; authenticated
clients see the configured store's tap names. Responses use `Cache-Control: no-store`.

## Pull protocol

`POST /events` accepts one JSON selection, up to 64 KiB, with
`Content-Type: application/json`. The CLI requests `Accept: application/x-ndjson`;
snoot requests `Accept: application/vnd.trie-trunk.dictionary+jsonl`.
The root is fixed by the server; it is not part of the request. Unknown fields,
extra JSON documents, and invalid tap paths are rejected.
Compact pulls require schema negotiation before this request; expanded CLI pulls
do not require client dictionaries.

```json
{
  "tap": "payments",
  "from": "2026-09-25T14:00:00Z",
  "to": "2026-09-25T15:00:00Z",
  "where": ["service==\"vault\" http_status==401", "service==\"billing\""],
  "limit": 100,
  "tail": true,
  "follow": false,
  "pretty": false,
  "headers": ["time", "duration", "level", "service", "id"]
}
```

- `tap`: optional, all taps by default. Authenticated remote clients currently have
  access to all taps; per-tap authorization belongs to the upcoming identity work.
- `from` / `to`: RFC3339 timestamps, inclusive start and exclusive end. Defaults
  are midnight UTC through request-start now. With `follow: true`, an omitted
  `to` leaves the upper bound open. An explicit end remains a filter for late events.
- `where`: the existing groups; whitespace means conjunction inside each string,
  and separate strings are alternatives.
- `limit`: initial matching event count, default 100. `null` explicitly means
  unlimited; zero with follow skips existing events.
- `tail`: default true; selects the latest initial matches and emits them in
  ascending UUID order. False selects the earliest.
- `follow`: default false; keeps streaming matching appends. The initial count
  does not limit subsequent output. Close the request to stop.
- `pretty`: default false. Indented objects are available for human inspection;
  expanded JSONL contains one object per line. Dictionary streams reject `pretty`.
- `headers`: presentation order, default server `TRUNK_ENTRY_HEADERS`; an empty
  array selects `id`, `time`, shared trie fields from root to leaf, then the
  population tail. Explicit names move those fields to the front; remaining
  fields keep their default order.
- `negotiation`: token returned by `/schemas`, required for dictionary JSONL.
  It binds the selected tap scope and current catalog signatures. It is not an
  authentication credential; TLS and bearer requirements still apply.

Time expressions such as `now-15m` remain CLI parsing conveniences. The CLI sends
resolved timestamps. Expanded events include `time`, derived from UUIDv7 as UTC
RFC3339, and retain `duration` in microseconds. No end timestamp is generated.
The server returns 406 for unsupported requested formats.

### Schema negotiation

Before pulling compact events, send `POST /schemas` with `Content-Type` and
`Accept` both `application/json`. This endpoint uses the same authentication and
tap scope as `/events`. An omitted or empty tap selects all taps.

```json
{"tap":"payments","known":[]}
```

The response contains a `negotiation` token and a `schemas` array. Each binding has
`tap`, `version`, `digest`, and, when needed, `dictionary`. Digests are lowercase
SHA-256 hex of the stored `log.map` / `events.map` bytes, matching the existing codec revision.
The dictionary uses the existing `codec.Definition` JSON form: `version`, `fields`,
`domains`, `timezone`, and other declared metadata. Field keys and enum keys inside
it remain numeric strings. No second dictionary model is introduced.

On later negotiations, put cached digests in `known`. The server still lists each
catalog binding but omits dictionaries already known to the client. Identical
dictionaries shared by multiple taps are sent once in the response. Snoot keeps
up to 64 compiled dictionaries between searches; active searches retain their own
bindings. The negotiation covers all installed versions in the selected taps,
so it does not scan event indexes or population files.

Snoot installs all required dictionaries before the first pull and retains the
token and bindings for later searches in the same tap scope. Subsequent searches
send `/events` directly, without another `/schemas` request. Window, predicate and
limit changes do not invalidate negotiation. The client retains eight tap scopes
in memory and clears them when bearer credentials change or the page reloads. A missing token receives 428. A stale token, changed tap scope,
or server restart receives 409 before search execution. Snoot renegotiates and
retries the initial pull, at most three attempts. Tokens require no server-side
session allocation. The server reads catalog definitions during negotiation and
again to validate a compact pull; catalog caching is not implemented yet.

During follow, a previously unseen catalog triggers another `/schemas` exchange.
Snoot pauses stream consumption until the dictionary is installed, then resumes
the existing stream. Network buffers may already contain subsequent rows, but
they are not decoded with an unknown schema. Negotiations do not consume search
slots, so occupied follow slots cannot prevent this exchange.

### Dictionary JSONL

Each line is either a dictionary control object or an event array. A control
object selects a stream-local dictionary handle. The first selection names its
negotiated `tap` and `version`; `headers` carries presentation order once.
Dictionary contents never appear in the event stream.

A simplified stream using negotiated version 42 for the `payments` tap:

```jsonl
{"schema":0,"tap":"payments","version":42,"headers":[]}
[8,"AaDQAozhdL6jSSNdanJIww==",3,5,12,2234,15,"unauthorized"]
```

Event arrays alternate numeric field keys and values. The id comes first, followed
by shared fields in actual root-to-leaf order, then the population tail. Enum
references remain numbers, including references in registered nested objects and
arrays. Literal strings and opaque objects remain literal. Arbitrary object keys
are not interpreted through the dictionary unless the field declares a nested schema.

A later `{"schema":0}` switches back to a previously selected dictionary.
Handles identify catalogs within this response, not schema versions:
two taps may use version 42 with different definitions. A new request starts a new
handle scope. Follow responses announce new catalogs as they are encountered.

JSON byte-string values use base64, including the 16 UUIDv7 bytes. Whole CBOR
records are never wrapped in base64. No rendered timestamp or expanded enum label
is added to an event row. Snoot expands fields and values, derives `time` from
UUIDv7, and applies presentation headers locally. This is the existing JSON
projection of CBOR values; it does not add native JSON support for CBOR tags or
non-finite numbers. Binary CBOR egress remains a separate future format.

The response streams directly from Rust through bounded byte buffers. It never
collects the result set or decodes records in Go. Reads naturally pause when a
client stops consuming. Each blocked write has a 30-second deadline; idle follow
requests do not have a 30-second lifetime. HTTP/3 sends transport keepalives.

Browser clients can request `Trunk-Completion: body` on dictionary JSONL pulls.
The server acknowledges the same response header and appends `{"complete":true}`
as the final control line only after successful execution, including empty results.
A missing completion line means the stream is incomplete. This allows browser
Fetch clients, which cannot read response trailers, to verify collection coverage.
Clients that do not opt in and expanded JSONL responses retain the existing format.

Responses declare a `Trunk-Status` trailer. `ok` means normal completion; `error`
means execution failed after some output. A missing trailer or broken connection
must also be treated as an incomplete result. The CLI checks this and exits with
an error. Before any bytes are sent, invalid requests receive 400, authentication
failures 401, and execution failures 422. An occupied query limit returns 503 with
`Retry-After: 1`. Errors are never injected as fake log events. Headers on an empty
follow request may remain pending until the first event or execution error.

## Execution and lifetime

`TRUNK_QUERIES` / `--queries` defaults to four active searches, including followers.
Excess requests fail promptly rather than accumulating an unbounded queue. A
request owns one Rust process, which performs filtering, reading and JSON rendering.
Dictionary expansion runs in Rust for the CLI and in snoot for browser responses.
Cancellation terminates that process. This retains the current
search engine and its per-query caches; it does not yet provide cross-query LRU
caches or a persistent Rust worker pool.

A failure in either combined service stops the other. Ctrl-C stops both listeners,
cancels active queries, and lets ingestion finish its existing flush/close path.
TCP, UDP, and socket bindings are opened before reporting the serve address.
The app and event API share an origin. No cross-origin browser policy or
subscription resume protocol is implemented.

The listener follows the [Go TLS server API](https://pkg.go.dev/net/http#Server.ServeTLS)
and [quic-go HTTP/3 server API](https://quic-go.net/docs/http3/server/).

## PIL CLI transport

Snoot and the CLI collect through a single `GET /collect` with `Accept:
application/pil`, `PIL-Version: 2`, and an optional cached `Definition-Version`.
Selection parameters are URL encoded (`tap`, `from`, `to`, repeated `where`,
`limit=all` or a count, `tail`, `follow`, and repeated `headers`). There is no GET
body. Missing definitions/dictionaries are sent before event frames in the same
response; `Definition-Known` can advertise up to 128 immutable cached digests to
avoid resending them after a reload. The response reports the current versions.
An incompatible protocol gets HTTP 426 before any frames. `/schemas` is optional
metadata discovery for pre-collection selectors. Rust encodes on the server and
decodes at the CLI. The existing JSONL
and browser dictionary JSONL formats remain available. See the
[prototype binding](../../../rust/PIL-TRANSPORT.md) for media types, schema
identity, follow updates, and stream completion.

## Resident search reader

Search now uses a lazily started resident Rust reader through private local IPC.
It retains bounded index/catalog caches between requests and scans partitions in
parallel, with ordered output. `--queries` still limits concurrent requests;
`TRUNK_SEARCH_WORKERS=1..8` controls scan workers per request.
Both format-2 `log.*` and format-3 `events.*` are accepted. External HTTP/PIL
negotiation is unchanged; native browser package transfer is not yet enabled.
See [format 3](../store/FORMAT-3.md).
