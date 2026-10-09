# trunk commands

CLI dispatch, flags, environment loading and output destinations live here.
Runtime behavior belongs to [ingest](../../server/ingest/README.md),
[search](../../server/search/README.md) and [store](../../server/store/FORMAT.md).

Build from the repository root:

```sh
make build
```

This builds the Solid app, Go CLI, and Rust `trunk-search` executable. It requires
Node 22.12+, Go 1.24+, and Rust 1.88+. Install both executables and the generated
`web` directory together. `make web` rebuilds only the app from `client`.
Server searches negotiate PIL definitions and stream binary PIL results to a Rust
decoder beside the CLI. JSON expansion happens there; Go forwards bytes and cgo is
not required. The browser retains its existing dictionary JSONL transport.
`search --local` passes its output descriptor directly to Rust.
On the development Mac, use
`DEVELOPER_DIR=/Library/Developer/CommandLineTools make build` when needed.

## Serve and search together

```sh
./trunk ingest 5410 serve 443
./trunk search now-15m -f
```

With the existing TLS/auth environment configured, this starts both services.
For Cloudflare Tunnel, add `serve --http-address 127.0.0.1:3020` or set
`TRUNK_HTTP_ADDRESS=127.0.0.1:3020`. This optional HTTP origin only binds loopback;
the trusted proxy owns public TLS and access control. Configured bearer tokens
still apply. See [tunnel origin](../../server/serve/README.md#tunnel-origin).

Serve uses HTTP/2 on TCP and HTTP/3 on UDP at port 443, plus a private Unix socket
at `<TRUNK_DIR>/.trunk/search.sock`. Search connects to that socket by default.
The web app is served at `/` from `./web`; `TRUNK_WEB` or `serve --web` selects
another directory. The app shell needs no bearer token; configured mTLS still
requires the browser to present a client certificate.
Use `--endpoint https://host:443` for remote search, or `--local` for direct file
access. Remote search prefers HTTP/3 and falls back to HTTP/2 during schema
negotiation. `--transport http3` requires QUIC; `--transport http2` selects HTTP/2.
Local searches keep using the Unix socket. Existing search flags and JSON output remain available. Each service's
flags follow its name and port; use the environment file for shared configuration.
See [event serving](../../server/serve/README.md) for the request format, credentials,
concurrency limits, and completion/error behavior.

Port 443 may require elevated bind permission on the host. For an unprivileged
development account, use `./trunk ingest 5410 serve 8443`; local socket searches
remain unchanged. Both listeners keep TLS and authentication enabled.

## Ingest

```sh
./trunk ingest 5140
```

With no TLS/auth settings, this starts local ingestion at
`ws://127.0.0.1:5140/<tap>` and writes to `./taps/<tap>/`. No token or
certificate is needed. Local mode accepts only loopback hosts; `--network tcp6`
selects `::1`. Use `--dir` to change the root. Clients select their tap in the URL,
for example `/json` for external JSONL and `/payments` for an application.
Only one path segment is accepted; `/`, trailing slashes and deeper paths are
rejected. There is no ingest tap argument or environment setting.

For a TLS listener, supply certificates and either a bearer token or client CA:

```sh
TRUNK_TOKEN=... ./trunk ingest 5140 --cert cert.pem --key key.pem
```

TLS listeners default to the platform wildcard; use `--host` to select an address.
HTTP/3 (`--transport http3`) always requires TLS and authentication. Partial TLS/auth
configuration returns an error instead of enabling local mode. File management
uses the supplied assigned schema; the semantic registration/planning protocol
is not implemented yet. See [file formats](../../server/store/FORMAT.md).

### Sandbox certificates

The local `certs/` directory contains development credentials for the existing
`.env.sand` mTLS settings. It is Git-ignored; private keys have owner-only access.

| File | Purpose |
| --- | --- |
| `certs/server.pem` | Server certificate for `localhost`, `127.0.0.1` and `::1` |
| `certs/server-key.pem` | Server private key (`TRUNK_KEY`) |
| `certs/client-ca.pem` | Sandbox CA; server uses it for `TRUNK_CLIENT_CA`, and clients trust it for server verification |
| `certs/client.pem` | Logger client certificate |
| `certs/client-key.pem` | Logger client private key |
| `certs/client-ca-key.pem` | CA signing key, used only when issuing replacement certificates |

With `ENVIRONMENT=sand`, run `./trunk ingest 5140`. Producers connect to
`wss://localhost:5140/<tap>` or `wss://127.0.0.1:5140/<tap>`, trust
`client-ca.pem`, and present `client.pem` with `client-key.pem`. Configure these
through the output connector's `HTTPClient` TLS settings. Server and client
certificates are valid for one year; the local CA is valid for ten years.
The CA is not installed into the operating system trust store.

## Environment configuration

All commands load `.env` from the current working directory when present. Put
the selector in that file to automatically overlay a profile:

```dotenv
ENVIRONMENT=sand
```

This loads `.env` first, then `.env.sand`. Base values are inherited unless the
profile overrides them; an empty profile value also overrides the base. Profile
selection happens once, so `ENVIRONMENT` inside the overlay does not chain to
another file. The selector may also come from the process environment, which
takes precedence over `.env`. An empty selector disables the overlay. Profile
names allow letters, digits, underscores and hyphens. A selected profile must exist.

Normal commands now pick up the sandbox profile automatically:

```sh
./trunk ingest
./trunk search --window 20260923T00:00,20260924T00:00
```

An explicit `--env /path/to/config.env` selects just that required file, bypassing
base/profile loading. `--env ''` disables all file loading while retaining process
environment variables. There is no parent-directory search. Relative file paths
resolve from the working directory, not the `.env` directory. `.env.sand` is covered
by the existing Git ignore rules.

Precedence is **explicit arguments > process environment > profile > .env > built-in defaults**.
An explicitly empty value overrides lower-precedence values. A missing default
`.env` is allowed; unreadable or malformed files, missing selected profiles, and
missing explicitly selected files fail startup. Configuration is read once,
before opening storage or listeners.

Create a private configuration from the repository root:

```sh
(umask 077; cp .env.example .env)
# Set the certificate/key paths and your client CA or bearer token in .env.
./trunk ingest
```

The example selects TLS with client certificate authentication; it needs your real
certificate files before it can start. For token authentication, clear
`TRUNK_CLIENT_CA` and set `TRUNK_TOKEN`. When both are set, both are required.
`.env` and `.env.*` are ignored by Git; `.env.example` contains no credentials.

| Variable | Argument | Default |
| --- | --- | --- |
| `TRUNK_DIR` | `--dir` | `./taps` |
| `TRUNK_WEB` | serve `--web` | `./web`; empty disables static files |
| `TRUNK_PORT` | positional port | `5140` |
| `TRUNK_HOST` | `--host` | loopback locally; wildcard with TLS |
| `TRUNK_TRANSPORT` | `--transport` | `websocket` |
| `TRUNK_NETWORK` | `--network` | `tcp` or `udp` according to transport |
| `TRUNK_FLUSH` | `--flush` | `1s` (Go duration syntax) |
| `TRUNK_CERT` | `--cert` | empty; PEM server certificate path |
| `TRUNK_KEY` | `--key` | empty; PEM private key path |
| `TRUNK_CLIENT_CA` | `--client-ca` | empty; PEM CA path for required client certificates |
| `TRUNK_TOKEN` | environment/file only | empty; bearer token |
| `TRUNK_ENTRY_HEADERS` | search `--entry-headers` | empty; id, time, root-to-leaf lineage, population tail |

Search uses `TRUNK_DIR` to find the local server socket, or the store with `--local`. It still searches all
taps unless search `--tap` is supplied. Ingestion selects the tap from the client URL.
The private Unix socket and `--local` do not require certificates or a token.
Remote HTTPS uses `TRUNK_TOKEN` and/or `--client-cert` / `--client-key`, with `--ca`
for a private server CA. Serving uses `TRUNK_SERVE_PORT` (443) and `TRUNK_QUERIES` (4).

Syntax: one `NAME=value` per line, optional `export`, single or double quotes, and
`#` comments. An unquoted inline comment needs whitespace before `#`; quoted hashes
are literal. Values are single-line and literal: no variable substitution, escape
processing or shell execution. Duplicate names, unmatched quotes, and lines longer
than 64 KiB fail with file/line diagnostics that omit the values. No process-wide
environment mutation or per-record configuration work is performed.

## Search

PIL carries server results; terminal output remains JSONL or `--pretty` JSON.
Both `trunk` and `trunk-search` must be rebuilt, and a running server restarted,
when upgrading to this prototype transport. See [the binding](../../../rust/PIL-TRANSPORT.md).

```sh
./trunk search now-1h --endpoint https://localhost:8443 --transport http3
```

```sh
./trunk search
./trunk search now-15m -f
./trunk search 14:00 15:30 --pretty
./trunk search yesterday,today --tap sandbox
./trunk search --head
./trunk search --tail 25 --pretty
./trunk search --from 20260923T20:40 -f
./trunk search --from 20260923T20:40 --tail 25 --follow
./trunk search --head 25 --to 20260924T00:00
./trunk search --window 20260923T00:00,20260925T00:00 > results.jsonl
./trunk search -w now-15m
./trunk search --win 14:00,15:30
./trunk search -w yesterday,today
./trunk search -w now-15m -f
./trunk search --tail 0 -f
./trunk search --all --head 100
./trunk search 14:00,15:00 --head 25 --where 'service=="vault" http_status==401'
./trunk search --where 'service=="vault"' --where 'service=="billing" error is !null'
```

Default output is JSONL: one complete object and newline per event. Embedded
newlines are escaped. `--pretty` enables indentation. `--jsonl` remains an explicit
alias for the default and takes precedence over `--pretty`.

| Selection | Initial output |
| --- | --- |
| No selection flags | Latest 100 matches from midnight UTC through command-start now |
| `--head [N]` | Earliest N matches in the selected window; today by default |
| `--tail [N]` | Latest N matches in the selected window, emitted in ascending UUID order |
| `--all --head [N]` | Earliest N matches from the entire archive |
| `--from date` | Latest 100 matches from date through now |
| `--to date` | Earliest 100 events before date |
| `--from date --tail [N]` | Latest N events at or after date |
| `--head [N] --to date` | Earliest N events before date |
| `--from date --to date` or `--window start,end` | All matches in the explicit time range, unless head/tail supplies a count |
| `-w start` or `--win start` | Latest 100 events from start to now |

Omitting N means 100; zero suppresses initial output. Counts apply across all
selected taps, schemas and days, not separately to each file. Both `--tail 25`
and `--tail=25` work. Head and tail are count selections; neither enables following.
Head and tail cannot combine. Either can supply a count within a positional window,
`--win`, or `--from`/`--to` range. `-w`, `--win` and `--window` are equivalent and
conflict with `--from` and `--to`. `--all` conflicts with timestamp arguments.

Only timestamps may be positional. Bare timestamps use the same window rules as
`--win`: one start, or a start and end separated by whitespace or a comma. Flags
may appear before, between or after the endpoints. For example,
`search 14:00 --pretty 15:00` and `search --pretty 14:00,15:00` are equivalent.
Negative relative timestamps such as `search -15m -f` also work.

A bare window cannot combine with `--win`, `--window`, `--from` or `--to`.
Other words, paths and counts require their flags; bare `pretty`,
`./taps` or `100` are errors. Values belonging to flags remain flag values, so
`--tap sandbox` never becomes part of the time window.

Time ranges have an inclusive start and exclusive end. Selection compares the
transaction start timestamp in UUIDv7. Timestamp parsing lives in
`go/server/search/window.go` and runs once before starting the Rust search.

| Timestamp input | Interpretation |
| --- | --- |
| `14:30`, `14:30:15.123` | Today in UTC; optional seconds and fractional seconds |
| `20260925T14:30`, `2026-09-25T14:30:15` | Compact or dashed year-first date and time |
| `2026-09-25T14:30:15.123-04:00` | Explicit offset, converted to UTC; `Z` also works |
| `2026-09-25`, `20260925` | Midnight UTC on that date |
| `now` | One instant captured at command start, shared by both bounds |
| `now-15m`, `-15m`, `now-1h30m` | Elapsed duration from that captured instant |
| `now-7d`, `-1w` | Whole days of 24 hours or weeks of seven days |
| `today`, `yesterday` | Midnight UTC today or on the previous date |

Relative offsets also accept `+`, seconds and subsecond duration units. Days and
weeks accept whole numbers and cannot be combined with other units. Calendar
months, Splunk snap syntax such as `@d`, and unrestricted natural-language phrases
are not supported. Ambiguous slash dates such as `03/04/2026` fail. An explicit
space between a dashed date and time works when quoted, for example
`--from '2026-09-25 14:30'`.

Comma and whitespace separate window endpoints. `-w start` omits the end and resolves it to
now; with `-f`, the end remains open so later events can arrive. An explicit end
such as `-w today,now` stays fixed even with `-f`. A trailing comma is an error.
Time-only endpoints both refer to today; `23:00,01:00` fails rather than assuming
tomorrow. Supply dates for a window crossing midnight.

A dashed date followed by a clock, such as `search 2026-09-25 14:00`, is one
timestamp. When both endpoints contain a space, separate them with a comma:
`search 2026-09-24 14:00, 2026-09-25 14:00`. With `--win`, keep the window in one
argument; quote it if it contains spaces.

Without explicit timestamps, selection starts at midnight of the current UTC day
and ends at command-start now. `--all` starts at the epoch; `--to` alone also keeps
its existing epoch-to-end behavior. `--from` without `--to` ends at command-start now.
An omitted end stays open with follow, including across midnight; an explicit end
remains fixed. The default window never expands to fill the requested event count.
Initial observations are per file, not an atomic store-wide snapshot.

### Field predicates

```sh
./trunk search 14:00,15:00 \
  --where 'service=="vault" http_status==401' \
  --where 'service=="billing" error is !null' \
  --tail 100
```

All predicates inside one `--where` must match. Repeated `--where` groups are
alternatives: an event matching any group is emitted once. There are no explicit
`AND`, `OR`, or parentheses. Quote the whole group for the shell. JSON strings
inside it can contain spaces and JSON escapes, such as `message=="access denied"`.
Whitespace around comparison operators is optional; whitespace between complete
predicates is required.

Fields are top-level names beginning with a letter or underscore, followed by
letters, digits, underscores or hyphens. Dotted nested paths are not supported yet.
Values use JSON string, number or boolean syntax. Strings are case-sensitive;
numeric strings do not equal numbers. Dictionary references compare using the
schema's labels, so use `level=="ERROR"`, not its numeric dictionary code.

| Predicate | Matches |
| --- | --- |
| `field==value` | Present, non-null field equal to the value |
| `field!=value` | Present, non-null field different from the value |
| `field is null` | Missing or explicit null |
| `field is !null` | Present and non-null, including empty strings, zero and false |

Use a separate null group to include missing values with inequality:
`--where 'service!="vault"' --where 'service is null'`.
Array/object equality and comparisons to the literal `null` are rejected.
Unknown fields count as missing in schemas that do not define them.

Shared lineage predicates run first. Index rows with excluded lineage addresses
skip population decoding. Clauses unresolved by the trie evaluate population CBOR;
only actual matches count toward head/tail limits. Follow applies the same clauses
to new records and does not replay rejected records on subsequent polls.

`-f` and `--follow` are equivalent. After the initial selection, the same Rust
process keeps reading newly appended matches until Ctrl-C. The initial count
limit does not limit future output. An explicit `--to` remains a filter while
following, so late events inside that range can appear and later timestamps are
excluded. `--tail 0 -f` emits only subsequent appends.

Ctrl-C stops search quietly with a successful exit. On macOS and Linux, follow
temporarily disables the foreground terminal's control-character echo and restores
its original settings on exit. Without a controlling terminal, it leaves terminal
settings alone. Other errors still report diagnostics on stderr.

Follow polls every 250 ms, remembers the last complete index-block position, and
loads only new index blocks and trie nodes. It discovers new tap/schema/day
directories and accepts late events in older matching days. Initial output is
UUID-sorted; subsequent batches can contain older UUIDs. Output flushes after each
poll's day batch, rather than waiting for the stdout buffer to fill. Ingestion's
own flush interval still affects visibility. Partial blocks wait for completion;
invalid complete blocks, truncated consumed indexes and replaced indexes fail.
No resume cursor is persisted after the process exits.

The default store is `./taps`, with all taps and schema versions. `--dir` and
`--tap name` restrict it. Each record uses its own schema's names and enum labels.
Search does not deduplicate matching UUIDs from different taps or versions.

Every result derives `time` from UUIDv7 as a UTC RFC3339 string with significant
fractional digits. `duration` remains an integer in microseconds; adding it to
`time` gives the transaction end. Search does not generate a separate `time_end`.
The derived `time` is a presentation field and is not stored separately.

```dotenv
TRUNK_ENTRY_HEADERS=
```

Default output starts with `id` and `time`, then shared trie fields from root to
leaf, followed by the population tail. Population fields sort alphabetically
within that tail. Trie order is preserved in the lineage cache.

Listed top-level fields render first; missing names are skipped, duplicates are
removed, and remaining fields keep their default order. Names match exactly.
`--entry-headers id,level` overrides configuration, and `--entry-headers=` restores
the default order. Ordering never changes storage or range selection.

Search opens files read-only, without writer locks or recovery. A visible complete
index block may still be awaiting its own fsync; follow does not assert durable
acknowledgement. Errors after earlier output leave a partial stream and a nonzero
exit. Diagnostics go to stderr. Following an empty or not-yet-created selected tap
waits for data without creating directories.

Selected index runs are validated before use. A population batch is checksummed
once before any selected row from it is decoded. Head can stop at its count; tail
retains at most N row cursors to reverse the selected results into ascending order.
Following establishes an initial offset for every matching index, including files
outside the initial count, to avoid replaying their existing data as new events.
That initial index scan and repeated directory/metadata polling remain costs.
Records are reconstructed individually, using a shared 64 KiB population buffer;
follow retains one offset per seen index and one incrementally loaded trie per
used schema version. The event server exposes the same selection and follow behavior over HTTP and the local socket.

## Partitioned events and resident search

New JSON registrations and the sample generator use hourly resource-owned
`events.*` storage. Existing immutable schemas continue using their assigned
layout. Search reads both layouts, and the server reuses one Rust process across
requests. `TRUNK_SEARCH_WORKERS=1..8` controls per-query partition scans (default 4).
See [format 3](../../server/store/FORMAT-3.md) for bounds, cache limits and migration.

To create a verified separate root while ingestion is stopped:

```sh
./trunk migrate-events --from ./taps --to ./events --timezone America/New_York
```

This does not restart the server, switch its root, or delete the original data.
Native package replication into the browser remains a subsequent stage.
