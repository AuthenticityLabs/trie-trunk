# Search

`Run(ctx, Config, output)` launches `trunk-search`, which owns discovery, index
selection, merging, CBOR reconstruction, dictionary expansion and JSON output.
The Go adapter passes resolved options and the output descriptor to Rust; it
never decodes records. Timestamp and comma/whitespace range parsing live in
`window.go`. `ParseTime(value, now)` and `ParseWindow(value, now)` resolve relative
expressions against the same caller-supplied instant. `ParseWindow` returns a zero
end when omitted; the CLI supplies now for a finite query or an open upper bound
for follow. Time-only inputs and calendar words use UTC. Explicit offsets convert
to UTC before comparison.

Build with `make build` and keep `trunk` and `trunk-search` together. The adapter
finds the Rust binary beside the Go executable, then on `PATH`. There is no cgo
bridge. Ordinary queries start one Rust process; `Follow` keeps that process alive
until the context is cancelled or an error occurs.

`Config` supplies a start-inclusive, end-exclusive UTC range. `Limit == nil` means
unlimited initial results. A limit selects earliest matches, or latest matches if
`Tail` is true. Tail results still emit in ascending UUID order. `Follow` streams
subsequent matching appends without the initial count limit. Explicit timestamp
bounds remain filters; following tracks file offsets, not the last emitted UUID.

The CLI defaults to the latest 100 matching events from the current UTC day through
command-start now, and JSONL; `--pretty` enables indentation. `--all` selects the
entire archive explicitly. An explicit timestamp range overrides the day default.
`--head [N]` and `--tail [N]` default to 100 when N is omitted. `-f` aliases
`--follow`; count selection never implicitly enables follow. See
[command semantics](../../cmd/trunk/README.md#search) for combinations and examples.
Bare timestamps select the same window as `--win`; all other arguments require
flags. The CLI accepts flags before or after positional timestamps.

The existing `.env`/`ENVIRONMENT` selection and `TRUNK_ENTRY_HEADERS` presentation
order remain supported. `time` derives from UUIDv7; `duration` remains in microseconds.
Search no longer generates `time_begin` or `time_end`. Header-order names match
exactly. Without an override, output is `id`, `time`, shared trie fields from root
to leaf, then the population tail. Population fields sort alphabetically within
the tail. Explicit headers move those fields to the front; remaining fields keep
their default order. Schema definitions and stored formats are unchanged.

`Config.Where` contains OR groups, each containing whitespace-separated predicates
that must all match. Rust parses them once in `filter.rs`, then binds names and enum
labels separately for each schema. Only top-level fields are supported initially:
`==`, `!=`, `is null`, and `is !null`. Comparison values are JSON strings, numbers
or booleans. Strings are case-sensitive; strings and numbers are not coerced.
Missing and explicit-null fields satisfy `is null`; both fail `==` and `!=`.
Empty strings, empty objects, zero and false satisfy `is !null`.
The current ingestion writer rejects explicit null fields; the search reader
nevertheless handles them when present in a valid population record.

Trie predicates are evaluated before index selection. Each parent precedes its
children, so the reader copies its packed predicate states and evaluates only
predicates for the new node's field. The cache uses `ceil(predicate_count / 4) + 1`
bytes per node, excluding vector capacity: two bits per distinct predicate plus
one include/exclude/population result byte. Duplicate predicates share a slot.
There is no predicate-count bitmask limit. With no clauses, no filter table exists.

An excluded lineage skips population reads. An included lineage needs population
only for output. Unresolved predicates read population CBOR before counting a match;
absence from the trie alone never proves null. An OR group rejecting a lineage
cannot exclude a different matching group. Unknown fields are missing within
schemas that do not define them. Internal nodes and the implicit root can be
referenced by records, including complete population fallback.

The Rust reader discovers existing day directories, avoiding a day-by-day walk
from the epoch for explicit archive searches. Head stops at the requested count; tail walks
newest days first and retains only selected row cursors before rendering. Population
checksums are deferred until a run needs population evaluation or output, then
checked once per selected run. Events are decoded one at a time. Tail keeps cursors
instead of decoded objects; selected population-dependent matches are decoded
again when emitted in ascending order. This trades rereads for bounded result memory.
Loaded schema/trie/filter catalogs are reused across days within one search process.
Bulk queries build leaf-to-row posting lists from each validated run and combine
candidate positions for every included or unresolved lineage. Each row appears in
one leaf list, so overlapping OR groups cannot duplicate an event. The time window
limits positions within each run; the existing UUID merge preserves order across
overlapping runs. Unresolved population predicates still run before counting matches.
After selection the CLI releases the leaf lists, retaining only candidate ordinals
or a compact range if all rows qualify. Small count-limited queries skip posting-list
construction and stop after enough matches using buffered scanning.

Index cursors buffer up to 256 consecutive rows per active run. The first buffer is
copied from the block already read for validation; subsequent reads refill a buffer
instead of reading one row at a time. Expanded lineage fields are shared with the
JSON renderer without cloning the shared map for each event. The per-schema lineage
cache has an estimated 4 MiB admission budget; full caches fall back to uncached
expansion. These caches live within a search process, not between CLI invocations.
Index blocks are still read and validated in full before their binary bounds lookup;
population checksums still cover whole batches. Predicate filtering does not remove
those existing costs or add a field-to-node reverse index.

Follow polls every 250 ms, reads new complete index blocks, extends cached tries,
and extends filter states for new nodes before using new leaf references. It
discovers new directories and records initial offsets even for records excluded
by the initial count. It does not reread previously consumed blocks, infer progress
from UUIDs, or require a worker/channel per file. Directory discovery and file stats
still occur during polling. State is local to one process; no resume token is stored.

Complete corrupt blocks fail, partial appends wait, and consumed index truncation
or replacement is an error. Initial snapshots are per file, not globally atomic;
a readable index block is not proof that its fsync has completed. Search never
repairs files or holds the ingestion writer lock.

`make test` runs Rust tests and Go-writer-to-Rust-reader integration tests, including
counts, late arrivals, new taps/days/versions, trie growth, partial writes and
cancellation. Direct Go tests require the built Rust executable on `PATH`.

## Served searches

The CLI now defaults to the private server socket. `--endpoint` selects HTTPS;
`--local` retains direct file execution. `client.go` sends the resolved query and
copies the event stream; `search.go` remains the Rust process adapter used by both
the server and direct mode. Query fields serialize from the existing Config;
Root is never accepted from remote requests. The [serve package](../serve/README.md)
owns HTTP listeners, authentication, concurrency, streaming, and socket lifetime.
