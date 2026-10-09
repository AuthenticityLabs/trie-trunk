# Rust search

Rust owns file discovery, index lookup, merge ordering, record reconstruction,
dictionary expansion and JSON output. `trunk search` keeps the existing Go flag
and environment handling, then launches `trunk-search` with the resolved options.
Both executables are built by `make build` and installed together. Stdout is passed
directly to Rust for terminal/file output; records are never marshalled back to Go.

- `lib.rs`: allocation-free, paired UUIDv7 boundary search.
- `store.rs`: read-only format-2 files, CRC32C checks, sorted runs, trie and population reads.
- `schema.rs`: CBOR definitions and projection into named JSON fields.
- `filter.rs`: grouped predicates, per-schema binding and packed lineage decisions.
- `postings.rs`: leaf-to-row posting lists and combined candidate positions within a run.
- `search.rs`: day traversal, run merging, derived timestamps and JSON presentation.
- `main.rs`: process arguments and buffered stdout.

The library exposes `search::run(&Config, &mut output)`. It is synchronous and has
no channels, background workers or async runtime. A long-running Rust search server
can call it without spawning a process. The current Go CLI starts one Rust process
per query; the launch is part of end-to-end query latency.

The child executable accepts `--dir`, `--tap`, `--from-ms`, `--to-ms`,
`--entry-headers`, `--pretty`, `--limit`, `--tail`, `--follow` and repeated `--where`. Millisecond bounds are unsigned and end-exclusive;
the Go adapter rounds sub-millisecond boundaries upward. Use `trunk search` for
human-readable dates and `.env` loading. Diagnostics go to stderr and failures
return a nonzero exit code. Cancelling the Go context terminates the child.

There is no file-format change. Schema versions are inherited from their directory.
Index snapshots ignore later appends and incomplete trailing blocks, but reject
corruption. Read operations never acquire writer locks or repair files. Each run
is validated before its two bounds are selected; its population batch is checked
before selected records are read. See [FORMAT.md](../go/server/store/FORMAT.md).

CBOR decoding uses Ciborium; CRC32C uses the Castagnoli implementation in `crc32c`.
Chrono renders UTC timestamps, and Serde JSON emits exact integer values and
configurable top-level ordering. Nested objects remain hierarchical. Byte strings
without UUID encoding render as base64, matching the former Go output.

Default presentation is `id`, `time`, trie fields from root to leaf, then the
population tail. The lineage cache preserves trie order as a sequence; rendering
does not walk the trie again. Population fields remain alphabetically ordered
within the tail. `--entry-headers` moves exact field names to the front and leaves
the remaining fields in their default order.

## Following and count selection

The CLI defaults to JSONL and the latest 100 matching events in the current UTC day
through command-start now. `--all` explicitly selects the archive. `--pretty` enables indentation.
`--head [N]` selects earliest events; `--tail [N]` selects latest events. Both default
to 100 and are independent of `-f` / `--follow`. Explicit date ranges remain
unlimited unless a count selection replaces an endpoint.

`search::Config` supplies `limit: Option<usize>`, `tail: bool`, `follow: bool` and
`clauses: Vec<String>`. Clauses use implicit AND within a group and OR across groups.
`==`, `!=`, `is null`, and `is !null` operate on top-level semantic field names.
The trie filter caches include/exclude/population decisions before index selection.
Population-dependent predicates must pass before an event counts toward the limit.
Bulk filtered queries group row ordinals by leaf while their index run is in memory,
combine all nonexcluded leaf lists within the time window, and visit only those
candidates. Once combined, the CLI releases the leaf lists and retains the selected
ordinals. If every row is a candidate, it keeps a range instead of an ordinal array.
Small count-limited queries scan buffered rows and stop early without building
posting lists. These are per-query structures; there is no cross-request LRU yet.

Each active run buffers up to 256 index rows, seeded from the already validated
block. Refills read consecutive rows rather than issuing a read per event. Shared
population-checksum state prevents repeated validation when tail cursors are cloned.
Expanded lineage fields are cached per schema and borrowed during JSON serialization;
only the event's population fields and derived timestamps are constructed per record.
The lineage cache stops admitting entries at an estimated 4 MiB per schema. Storage
formats, JSON field names, precision and pretty-print behavior are unchanged.
See [search behavior and costs](../go/server/search/README.md).
A missing limit preserves the full-range library behavior. Tail selection keeps
only selected cursors and renders them in ascending order. The six-byte paired
boundary lookup is unchanged. Population checksums are checked once when a run
actually contributes output, and schema/trie data is shared by its readers.

Following polls every 250 ms within the same process. Per-index offsets advance
only through complete validated blocks; cached tries load appended nodes. New
directories and late arrivals are discovered without using a UUID as the cursor.
Each emitted day batch flushes stdout. The initial count does not cap future output.
See [CLI semantics](../go/cmd/trunk/README.md#search) for bounds and defaults.

## Boundary lookup

`window(rows, from, to)` returns the matching range for `[from, to)` in a sorted
slice of 36-byte rows. Each probe loads eight bytes and discards the last two, so
only the six UUIDv7 timestamp bytes participate. Both boundaries advance in one
loop, without threads, allocations or measurement calls. `lower_bound` remains
available for single-boundary lookups. The paired algorithm is unchanged.

Empty or reversed windows return an empty range at the starting boundary.
Fractional milliseconds round up; before-epoch and beyond-48-bit times clamp to
the beginning and end. Apply the lookup separately within each sorted disk run.

```sh
make build
make test
cargo bench --manifest-path rust/Cargo.toml --bench search
```

On the development Mac, prefix commands with
`DEVELOPER_DIR=/Library/Developer/CommandLineTools` when Xcode's linker is unavailable.
The file reader currently targets Unix platforms (macOS/Linux); macOS ARM64 is tested.

The boundary benchmark uses the existing 10,000-row fixture plus a million-row
in-memory array. Both algorithms receive the same fixed 4,096 query pairs per width,
generated with seed 56. Reported ns/window exclude disk I/O, validation, reconstruction,
JSON and process startup. They must not be interpreted as whole-query latency.

## Posting selection and lineage-cache measurements

Measured on 2026-09-25, Apple M1 Max, macOS ARM64. The same existing million-event
Go-written dataset was used for both binaries: one UTC day, 48 complete lineages,
72 trie nodes and 4,096-event runs. Every query uses
`20260924T00:00,20260925T00:00`. Complete JSONL output was checked for identical
SHA-256 hashes and event counts, including the four-service inclusive filter.

These are medians of seven alternating baseline/updated runs with warmed filesystem
caches and fresh CLI/Rust processes. Stdout goes to `/dev/null`. Times include
process startup, validation, posting construction, reconstruction and JSON output;
they exclude terminal display, networking and concurrent users. There is no cache
shared between timed invocations and no timing instrumentation inside search loops.

| Query | Returned | Previous | Updated |
| --- | ---: | ---: | ---: |
| All events | 1,000,000 | 3,645.29 ms | 3,038.82 ms |
| One service, trie filter | 125,000 | 817.80 ms | 416.57 ms |
| Four services, inclusive filter | 500,000 | 2,029.37 ms | 1,550.00 ms |
| Trie filter, no matches | 0 | 400.19 ms | 27.96 ms |
| Same service as literal population data | 125,000 | 1,794.24 ms | 1,387.83 ms |
| Latest 100, no filter | 100 | 16.95 ms | 17.67 ms |
| Latest 100, trie filter | 100 | 17.97 ms | 17.41 ms |

Direct Rust peak resident memory measured 6.05 MiB for the 125,000-result filter,
5.41 MiB for all events and 2.56 MiB for no matches. These exclude the Go process
and filesystem cache. The additional memory holds bounded per-run row buffers and
candidate positions; it does not retain the complete 36 MB index. Memory grows with
the number of simultaneously active runs and selected positions. The tiny trie is
not representative of high-cardinality schema workloads.

The remaining full index validation, population decoding, population batch CRCs,
per-event population reads and JSON/timestamp rendering still cost time. This is
an improvement to the CLI implementation, not a persistent-server or UI benchmark.

## End-to-end check before count and follow support

Apple M1 Max, macOS ARM64, Go 1.26.4 and Rust 1.97.1. One fixed Go-written store
held 10,000 synthetic events across ten minutes, in 512-record batches. Both
executables searched identical files and UTC windows; their parsed JSON outputs
were compared for equality before timing. Medians below cover 11 warm-cache runs,
alternating execution order, with stdout redirected to `/dev/null`. Timings include
process startup, file validation, reconstruction and JSON serialization.

| Returned records | Previous Go pipeline with Rust bounds | Full Rust pipeline launched by Go |
| ---: | ---: | ---: |
| 10,000 | 58.27 ms | 51.51 ms |
| 1,000 | 15.41 ms | 15.99 ms |
| 0 | 7.32 ms | 9.64 ms |

These local synthetic results show a modest gain for the larger result set and a
startup penalty for small queries. They are not measurements of a persistent
server, concurrent users, cold storage or arbitrary event payloads.
