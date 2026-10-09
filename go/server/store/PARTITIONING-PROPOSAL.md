# Partitioned event storage and collection

Status: server prototype implemented; native browser package collection remains a subsequent stage.
See [FORMAT-3.md](FORMAT-3.md) for the implemented layout, tests and current limits.
The sections below retain the full target design, including work not yet implemented.
Date: 2026-09-29.
Scope: server storage, resident search, and subsequent browser collection.
Proposed binary storage version: 3. This is separate from PIL protocol versions
and assigned schema/definition versions.

## 1. Proposed direction

Partition indexes and population by a deterministic trie branch and time bucket.
Keep the original schema dictionary and shared trie authoritative. Use complete,
committed index runs for collection and retrieve population only when needed.

The proposed baseline is:

- Rename application data files from `log.*` to `events.*`.
- Start with a fixed resource boundary per schema, including all operations
  beneath that resource. A search can still select an individual operation.
- Introduce hourly collection buckets, with the tap's timezone governing calendar
  placement. Support day buckets during transition.
- Keep the latest 15 minutes outside mature package publication. Continue to
  persist and serve live events.
- Use 28-byte, end-offset-only rows in mature indexes, plus one population start
  per run. Retain explicit starts in active indexes.
- Keep a rolling 24-hour index cache in a resident reader, with a byte budget.
- Search independent partitions concurrently with bounded workers and ordered
  result merging.
- Preserve server dictionary codes, trie node IDs, population bytes, and source
  offsets when replicating complete packages into the browser.
- Do not make collection requests or collection IDs owners of stored events.

This proposal does not replace population with Parquet, add compression, reorder
existing trie paths, or introduce automatic statistical schema changes.

## 2. Current behavior and the change in responsibility

[Format 2](FORMAT.md) writes one shared `log.map` and `log.tri` per schema,
and one `log.idx` and `log.pop` per UTC day. A 36-byte index row contains:

```text
UUIDv7:16 | trie leaf:4 | population start:8 | population end:8
```

Index runs append sequentially. UUIDs are ordered within each run; late arrivals
can make different runs overlap in time. Global ordering requires a merge.
Population maps are contiguous in the corresponding run's row order.

The [Go search adapter](../search/search.go) currently launches a new
Rust process for each ordinary search. The [Rust reader](../../../rust/src/store.rs)
reads and validates index blocks before applying binary time bounds. Normal
searches do not share an application index cache across requests.

The browser currently uses IndexedDB UUID entries and locally reconstructed
population/trie files. Its `collections` and `members` stores are existing
prototype behavior, not the ownership model proposed here. See the
[browser store description](../../../rust/browser/README.md).

## 3. Names, ownership, and paths

Use consistent `events.*` basenames and put the owning trie node in a directory.
This is equivalent to the suggested `<year>/<day>/<node-id>.idx` routing while
making index/population pairing and the prefix transition explicit.

Example hourly layout; bucket labels below include the local UTC offset:

```text
<root>/<tap>/<schema-version>/
    events.map
    events.tri
    events.y26
    2026/
        events.365
        events.m09
        events.w40
        272/
            events.sum
            1300-0400/
                1933080307/
                    events.idx
                    events.pop
                1933080421/
                    events.idx
                    events.pop
                0/
                    events.idx
                    events.pop
```

Node IDs are scoped to the tap/schema, never globally meaningful. Bucket labels
are derived from the tap timezone; the authoritative bucket identity is its
absolute, start-inclusive/end-exclusive instant range. Use offset-qualified
labels for repeated local hours. Resolve missing hours and unusual offset
transitions through timezone rules, not arithmetic on an hour label. Day buckets
omit the hour directory. Bucket duration is explicit policy, never inferred from
the set of files found.

Prefer an IANA timezone for new tap configuration; an explicit fixed-offset zone
is supported with fixed-offset semantics. Existing taps remain UTC unless an
explicit migration changes their policy. Changing timezone or partition policy
must not reinterpret paths already written. Pin the resolved bucket start/end in
published metadata; timezone-database updates must not silently recalculate old
package identities. Exact path escaping and the bucket boundary algorithm are
part of the format-3 specification, with round-trip tests for unusual transitions.

The filename transition covers all existing data roles:

| Existing name | Proposed name |
| --- | --- |
| `log.map` | `events.map` |
| `log.tri` | `events.tri` |
| Daily `log.idx` | Bucket/node `events.idx` |
| Daily `log.pop` | Bucket/node `events.pop` |
| `log.sum` | `events.sum`, initially still at day scope |
| `log.365`, `log.mMM`, `log.wWW`, `log.yYY` | Same suffixes under `events.*` |
| Browser `log.pil` | `events.pil` for the cached PIL definition |

The writer lock remains `.writer.lock`. This is not a blind rename of diagnostic
logs, documentation examples of the old format, or unrelated filenames.

Day-level aggregates can combine branch/hour contributions, but their progress
watermarks must identify the source bucket, owner, and run as well as position.
A byte offset from one index cannot stand for progress across multiple indexes.
Maturity conversion must translate existing progress to corresponding compact-run
boundaries without counting the same events again. Check rebuilt aggregates
against incremental totals during conversion and late-arrival tests.

## 4. Routing from query parameters to indexes

The map should declare the storage policy alongside its existing trie path:
partition boundary field, time-bucket policy, and timezone. Exact assigned CBOR
keys belong in the subsequent binary format specification. The map remains
immutable for its assigned version; changing these declarations requires a new
version/digest.

Start with `resource` as the boundary when the schema supports it. This identifies
a field in the declared path, not a fixed physical depth: missing fields are
currently skipped. Schemas without this boundary use owner 0 initially.

For each leaf, its index owner is the ancestor carrying the boundary field. If
there is no such ancestor, route to owner 0. This includes missing-boundary events,
internal-node leaves, and the current full-population fallback at leaf 0. Every
event belongs to exactly one index owner and one time bucket.

A resource may occur under several earlier prefixes such as INFO and ERROR.
Collecting that resource across levels therefore selects several owners.
Operation-specific search retains the operation predicate but collection widens
to all operations in the selected resource scope. Any other broadening must be
visible in the collection scope; do not silently broaden service or level.

The trie needs an in-memory child lookup for parameter-based downward traversal.
The current parent references alone do not provide efficient child lookup.
Build child adjacency and a leaf-to-owner table once, extend them as committed
nodes arrive, and reuse them. Predicates that omit leading fields fan out through
the relevant branches; arbitrary field searches are not all single-path walks.

The trie indicates possible owners, not which hourly files actually exist.
Maintain the existing-file and run inventory in the resident reader:

- Discover bucket/node directories during cold startup.
- Record committed run locations, counts, first/last UUIDs, and file revisions.
- Extend the inventory from writer commit notifications and recovery scans.
- Filter by bucket and owner before scheduling run work.

This is the initial index-of-indexes. No new collection identity or separate
persistent routing database is required. Cold discovery and validation remain
real costs; optional persisted run directories can be evaluated later.

Only prune a partition when the predicate proves it cannot match. Missing trie
fields, negative/null predicates, and population-only fallback retain the existing
include/exclude/population semantics. An absent node is not proof of an absent
event field.

## 5. Mature indexes: 28-byte rows

A mature index uses fixed-width rows:

```text
UUIDv7:16 | trie leaf:4 | population end:8
```

Each run has a count and one absolute population start:

```text
[length:4][CRC32C:4]
[count:4][population_start:8]
[row:28][row:28]...
```

For zero-based row i:

```text
start(0) = run.population_start
start(i) = row[i - 1].population_end, for i > 0
end(i)   = row[i].population_end
```

All offsets address the uncompressed bytes of one population file. Population
block headers are outside event ranges. The first value in the current population
framing starts at byte 16, not zero; the run base removes any dependency on a
preceding run.

The previous row starts 28 bytes earlier within a run. Only its final eight bytes
are needed to reconstruct the start. Sequential iteration already has that value.
Binary time bounds use the UUID alone and do not need the predecessor.

Compared with the current run, excluding identical file headers:

```text
old bytes = 8 + 4 + 36N
new bytes = 8 + 4 + 8 + 28N
saving    = 8(N - 1)
```

Large runs approach a 22.2% index reduction. A one-row run has no saving.
Additional fixed file metadata is separate and must be included in benchmarks.

Required invariants:

1. A run references exactly one population file and one contiguous population
   block. Population order matches physical index row order.
2. UUID order is strict within a run. Different runs may overlap in time.
3. Each end exceeds its derived start, remains within the committed population
   block, and meets the existing per-event size limit.
4. The first base and final end identify the population block covered by its CRC.
5. Never derive a start from the previous matching row, a merged-query predecessor,
   or an IndexedDB chronological predecessor.
6. A copied subset of rows is not a complete native run. Replicate the full run,
   retain original source positions, or explicitly repack its population.
7. Publishing an index reference requires its trie and population bytes to be
   durable first. Recovery must reject or repair incomplete appends before exposure.

The storage-version header must distinguish active and mature index layouts;
readers never guess row width from filenames or file length. Detailed common
header fields and numeric tags are an implementation prerequisite. PIL framing
and its protocol-version negotiation remain separate.

## 6. Fifteen-minute live tail and late arrivals

At the start of a request, capture one server time T:

```text
maturity_cutoff = T - 15 minutes
```

The last 15 minutes are not eligible for mature package publication or permanent
coverage claims. They remain durably ingested and queryable through the live PIL
path. The delay is not permission to hold acknowledged data only in RAM.

A committed block already has stable predecessor offsets even when its events
are recent. The 15-minute policy addresses collection freshness and pending
arrival/reordering risk; elapsed time alone does not establish a committed block.

For a simple first implementation:

- Keep active bucket indexes in the existing explicit-start, 36-byte row form.
  Their complete runs remain searchable. Do not distribute them as mature native
  index packages.
- Mature an entire bucket only when its end is at or before the cutoff.
  An hourly bucket can therefore remain active for up to roughly 75 minutes
  from its start. This conservatively retains some older events on the live path.
- Under the bucket writer lock, checkpoint the committed index, write a temporary
  28-byte replacement, verify it, sync it, and atomically publish it at the same
  `events.idx` path with a new file incarnation. Sync directory metadata.
- Preserve population order and offsets during this conversion; `events.pop`
  does not need a rewrite. Never mix 36-byte and 28-byte rows in one index file.
- Existing readers finish against their pinned old descriptor. New readers and
  caches use the new incarnation. Temporary conversion files are not searchable.
- A crash leaves either the old committed index or the verified replacement
  authoritative. Recovery cleans incomplete staging only after that choice.

A bucket that is already mature when first created can be written directly in
28-byte committed runs. This conversion strategy is a proposal choice to test,
not an existing capability or an assertion that recent end-only rows are unsafe.

Late arrivals older than 15 minutes are still possible. Append their population
and a new compact index run; preserve existing offsets and rows. Increment the
published revision/committed ends and merge the new run during search. Never
discard a late event or insert it into a previously published run.

Thus mature means eligible for compact packages, not permanently closed to new
events. Follow/refresh progresses by committed file positions and incarnations,
not maximum UUID. Clients revalidate mature bucket revisions when revisiting
them; unchanged revisions cost metadata rather than duplicate event bytes.

## 7. Population granularity without larger event references

Initially pair index and population by bucket and owner:

```text
<bucket>/<owner>/events.idx
<bucket>/<owner>/events.pop
```

The directory supplies file identity, so rows need no extra file ID or path.
One population block per index run also preserves simple checksum validation.

Keep finer population partitioning as a compatible later experiment:

- A deeper trie owner is derivable from the existing leaf.
- A finer time bucket is derivable from UUIDv7 plus the declared bucket policy.
- Cache these routing results instead of traversing parents for every event.
- Group runs by the resulting population file. Do not interleave unrelated files
  and treat the previous physical row's end as the current start.
- Record any shared run-level routing selector needed for validation once per run,
  not as a larger reference in every row.

Arbitrary size-triggered population rollover is deferred because its file choice
is not necessarily derivable from leaf and time. It needs explicit additional
routing information.

Smaller files improve whole-file lazy loading, but incur more file headers,
directory entries, opens, requests, and potentially weaker cross-event compression.
The fallback lazy unit is a complete population block: the existing CRC covers a
batch, not one event. Do not treat an individual byte slice as CRC-verified without
verifying its containing block.

## 8. Resident rolling index cache

A long-lived reader owns an approximately 24-hour event-time working set:

- Shared schema and trie catalogs, child lookups, and leaf-to-owner tables.
- Packed active or mature index rows and their run descriptors.
- Committed file ends, incarnations, and validation state.
- Optionally reusable leaf-to-row postings after measurement.

The 24-hour period is a retention target with a separate byte budget. It is not
a guarantee that arbitrary tap volume fits memory. Use bounded allocations and
retain pinned snapshots until readers finish. Expire runs only when outside the
window and unreferenced; clamp partially overlapping runs logically.

| Cached event count | Mature row bytes | Active row bytes |
| --- | ---: | ---: |
| 1 million | 28 MB | 36 MB |
| 10 million | 280 MB | 360 MB |
| 100 million | 2.8 GB | 3.6 GB |

These are decimal raw row sizes; headers, catalogs, postings, and capacity are extra.

Extend from durable append notifications, validate once on admission, and recover
missed notifications using committed offsets. Check replacement/truncation by
incarnation. Keep late arrivals within the cached time window even when their
UUID precedes those already loaded.

Time/trie-only searches, counts, and histograms can use this cache without index
disk reads once warm and complete. Population output, dynamic predicates, and
duration analytics still need population or appropriate aggregates. Older or
evicted indexes require disk access. The operating system's file cache may
already avoid physical reads; measure the additional win from retained parsing,
validation, and routing state.

## 9. Parallel search and collection

Interpret `tap.*` here as all eligible branch partitions of a selected tap. This
does not silently introduce new CLI wildcard grammar or cross-tap permissions.

Plan queries as:

1. Capture query time, schemas, and committed per-file snapshot boundaries.
2. Route predicates to eligible owners and time buckets.
3. Skip runs whose UUID bounds cannot overlap the window.
4. Schedule independent partition/run tasks with bounded concurrency.
5. Apply binary bounds and cached lineage decisions; evaluate unresolved
   population predicates before counting or emitting matches.
6. Merge accepted cursors in UUID order for CLI/event streams, then produce PIL.
7. For native package collection, preserve complete run bytes and source identity;
   packages can arrive out of order and be searched/merged locally.

A multi-file snapshot initially means one captured committed end per file, not
a cross-tap transactional instant. Response metadata describes that snapshot;
writes after those boundaries are obtained on refresh.

Use Go goroutines/channels for request admission, transport, cancellation, and
backpressure. Retain one resident Rust engine as the owner of packed caches and
parallel scan/merge work. A bounded Rust worker pool can execute partition jobs
without cloning the catalog or reimplementing the decoder in Go. A persistent
local IPC connection is the initial integration direction; keep `--local` as
an uncached/offline reader. Do not spawn a Rust process per file.

Queues have byte and task bounds. Slow clients must stop upstream scheduling,
and cancellation/errors must release workers, buffers, and file handles. The
official [Go pipeline guidance](https://go.dev/blog/pipelines) describes the
fan-out/fan-in and cancellation patterns relevant to the Go orchestration layer.

Ordered output requires more than forwarding whichever worker finishes first.
Use run lower bounds and bounded merge lookahead; do not buffer an entire large
query waiting for a slow shard. Apply head/tail limits globally after predicate
resolution, never independently as the final limit for each partition. Stable
source ordering breaks ties across taps if UUIDs compare equal; deduplication
retains the established identity scope.

Parallelism may shorten elapsed time by overlapping reads and using more cores.
It does not reduce the total work of a broad scan. Across K similarly sized runs,
binary bounds still require roughly K log2(N/K) comparisons, followed by scanning,
merging, validation, and output. Tiny jobs should be batched to avoid scheduling
cost exceeding lookup cost. Disk bandwidth, memory bandwidth, and transport can
dominate. There is no promised speedup until measured against the sequential path.

## 10. Collection, revisions, and the browser

Collection ownership is the stable source tuple:

```text
authorized source + tap + schema + time bucket + trie owner
```

Query/session IDs may identify requests, but never directories owning event bytes.
An authorization namespace may isolate browser storage without changing that
logical tuple.

The collection response identifies map/PIL-definition digests, required trie
prefix, index layout, file incarnation, and committed byte ends. Reuse the
existing single-GET negotiation approach: send missing definitions before
dependent bytes on the same response. Do not prepend another mandatory schema
download when the cached versions match.

Native index/trie/population replication needs its own negotiated PIL Definition.
Keep `Content-Type: application/pil`; define package/control frames that carry
source metadata and native byte spans as BYTES fields. An existing PIL event-frame
stream is not automatically a complete index package. Specify the package frames
before implementation; never send bare TIDX/TPOP bytes under an event definition
or expand them through JSON. Definitions and dependent spans still share one GET.

Fetch only missing complete runs or file prefixes when incarnation and existing
prefix identity match. Require a new snapshot after replacement/compaction;
offset alone cannot establish prefix equality. Append revision checks avoid
rehashing and retransmitting unchanged files.

Recent active buckets use the live event path, remain outside mature coverage,
and are revalidated on later collection. UUID identity reconciles live events
with their later mature packages; it must not create duplicate durable event
ownership. A previously empty bucket also needs revision revalidation because
late arrivals can create it.

The browser keeps the server's dictionary and trie ordinal assignments. It does
not recreate an unrelated trie from returned events. Preserve server population
offsets by caching full files or blocks at their original offsets, with explicit
availability metadata. A missing local block is not an absent event field.

Prefer complete native index runs as the browser's analytical inventory for this
prototype. If IndexedDB retains UUID uniqueness/lookups, its entries must point
to source run/row locations, not derive population starts from the preceding IDB
record. Definitions, preferences, notebooks, and cached-byte metadata can remain
in IndexedDB. Do not add a second expanded event database.

Retire `collections`/`members` as persistent ownership. Coverage and available
bytes describe canonical source partitions and revisions, independent of which
search acquired them. Preserve notebooks by event identity across migration.

For example, previously collecting ERROR resource partitions for two separated
weeks does not certify INFO partitions or the intervening week. A broader
collection reuses the matching packages and requests the missing owners/buckets.
Within the same resource scope, a newly selected operation needs no new index
package when all operations were already collected. Population is fetched only
for requested display/evaluation or deliberate prefetch.

## 11. Selecting granularity

Begin with an explicit resource boundary and hourly buckets. For schemas lacking
a resource boundary, owner 0 is a valid simple starting point. Do not pick a
numeric median of field cardinalities and assume it balances bytes.

Measure past files to explain alternative choices:

- Event counts and index/population bytes per candidate prefix and time bucket.
- Typical, p95, and maximum package sizes; skew and tiny-file frequency.
- Extra package count versus reduction in the largest packages after another
  field split.
- Whole-package overfetch, cache reuse, open handles, and read/request overhead.

A useful initial gradient is the marginal reduction in package size per added
split. Time derivatives of volume may signal that a policy deserves review, but
do not automatically change routing. Statistical/adaptive cuts and query-history
optimization are deferred. Any adopted change is explicit and versioned; old
files retain their original interpretation.

## 12. Transition and implementation sequence

1. **Specify and test format 3.** Finalize header tags, active/mature row widths,
   map policy keys, bucket naming, predecessor rules, and committed publication.
   Keep `FORMAT.md` as the description of the currently implemented format until
   the new readers/writers exist.
2. **Add dual readers.** Recognize legacy `log.*` format 2 and proposed `events.*`
   format 3 explicitly. Reuse predicate semantics and PIL output. Never infer
   compatibility from an extension alone.
3. **Prototype partitioned writing and maturity conversion.** Preserve writer
   lock, population/trie-before-index durability, validation, and recovery. Add
   late-run publication and source revision tracking before cache completeness.
4. **Make the Rust reader resident.** Add bounded cache ownership and incremental
   publication before parallel query work, so workers reuse prepared state.
5. **Add bounded parallel scheduling.** Measure sequential and parallel modes
   against the same pinned snapshot and outputs; retain concurrency 1.
6. **Add native collection and browser replication.** Negotiate the new package
   kind, preserve source addresses, remove request-owned storage, and verify
   partial-download and live-to-mature reconciliation.
7. **Migrate existing data explicitly.** Stop the writer for the first migration,
   capture committed ends, and write a separate staging root. Preserve field/value
   assignments, trie ordinals, UUIDs, and event contents. Assign new schema
   versions/digests where the immutable map changes. Repartition rows and
   population together; this changes population offsets and requires rebuilding
   references. Rebuild aggregates or convert their source watermarks coherently.
8. **Verify and cut over.** Compare counts, UUID sets, reconstructed event values,
   predicate results, and aggregates. Select one authoritative root, then restart
   the writer/search service. Retain the old root for rollback; never search both
   copies as independent events. Rollback after new writes needs explicit replay
   or reconciliation.
9. **Transition clients and retire legacy naming.** Namespace the native browser
   cache by negotiated source/layout identity, retain preferences/notebooks, and
   do not reinterpret old pointers as new offsets. Update loaders, fixtures,
   tools, docs, and generated WASM assets together. Delete old data only after a
   separate verified retention decision.

A rename alone cannot implement the row-width, partition, or bucket changes.
No destructive in-place conversion is proposed.

## 13. Validation and performance gates

Correctness and recovery coverage must include:

- Empty files; one-row and multi-row runs; row 0; run boundaries; integer overflow;
  malformed ends; CRC failures; maximum population value size.
- Sparse predicates, missing resource, root fallback, null/negative clauses,
  shared resource names under different prefixes, and global head/tail semantics.
- Overlapping runs and late arrivals, including arrivals older than 15 minutes,
  new owner files, and previously empty mature buckets.
- Restart after each population/index sync point, during maturity conversion,
  and during cache publication. No dangling references or duplicate promotion.
- Cutoff boundaries, clock changes, midnight/year rollover, fixed offsets, DST
  repeated/missing hours, and non-hour offset transitions.
- Source-preserving partial replication, canceled downloads, changed incarnation,
  live/mature overlap, multi-tab access, authorization isolation, and notebook
  identity preservation.
- Parallel versus sequential output equality, cancellation, slow consumers, and
  bounded buffers/handles under concurrent requests.

Benchmark actual distributions at one, ten, and larger feasible millions of
events. Include skewed resources, many tiny branches, broad tap queries, selective
resource queries, recent cache hits, expanded historical windows, and cold starts.

Compare 36 versus 28 bytes, day versus hour buckets, candidate branch boundaries,
and concurrency 1/2/4/8 with a byte budget. Record throughput, time to first
results, completion p50/p95, CPU, RSS, disk bytes, open files, package overfetch,
transport bytes, late-refresh cost, and index validation time. Measure live,
mature, population-free, and population-expanded paths separately.

Earlier lookup-only and small-fixture PIL measurements in
[PERFORMANCE.md](PERFORMANCE.md) and
[the PIL transport notes](../../../rust/PIL-TRANSPORT.md) are context, not evidence
of a speedup for this design. Advance the rollout only after format correctness
and measured resource bounds pass.
