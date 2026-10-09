# Partitioned event storage — formats 3 and 4

Format 3 was introduced 2026-09-29; format 4 adds U16 row references on 2026-10-02.
[Format 2](FORMAT.md) and format 3 remain readable.
PIL transport versions and schema versions are independent of this disk version.

Object identity, the tap registry, and the 30-day trie statistics snapshot are
described in [OBJECTS.md](OBJECTS.md). Legacy anonymous schema directories remain readable.

## Policy and paths

The immutable map adds CBOR key `7`:

```text
{0: 4, 1: "resource", 2: "hour"}
   format  boundary field  bucket (hour | day)
```

Map key `4` declares an IANA timezone or a fixed `±HH:MM` offset. Nil policy
continues to mean format 2/UTC. Registering a definition without policy now assigns
format 4/hour/resource before computing its version and digest. Installing an
already assigned definition retains its original policy. The CBOR logger can opt
in by assigning this policy and a new schema version; existing definitions must
not be mutated under their old version.

```text
<root>/<tap>/<object>.v<major>/<minor>/
  events.map                       deterministic CBOR definition
  events.tri                       shared ordinal-addressed trie
  statistics                       rolling trie frequencies/cardinality/entropy
  events.y26                       optional yearly rollup
  2026/
    events.365                     optional daily rollups
    events.m09                     optional monthly rollup
    events.w40                     optional weekly rollup
    272/
      130000-0400/                  HHMMSS±HHMM local bucket start
        1933080307/                 resource ancestor's node ordinal
          events.idx
          events.pop
          events.sum
        0/                         missing resource / population fallback
          events.idx
          events.pop
          events.sum
```

Day policy omits the hour directory. An owner is the ancestor whose field is the
boundary in the declared trie path; it is not a physical depth or a global ID.
Missing boundary and leaf-0 fallback use owner 0. Index and population share one
owner/bucket, so event references need no file ID. New indexes use no collection ID.

Bucket boundaries are resolved from the schema timezone. Hour buckets advance
in elapsed hours from the civil day's first instant; the final bucket can be
shorter at a non-hour offset transition. Offset-qualified names distinguish
repeated local hours. The index pins absolute bounds. Existing bounds remain
authoritative; a timezone-rule change that collides with an existing label requires
a new schema rather than reinterpreting that file. Missing local midnight, skipped
dates, half-hour transitions, repeated hours, and fixed offsets have regression tests.

## Framing

All integers below are big endian. The common eight-byte file header is:

```text
magic:4 | storage_version:u32 = 3 or 4
```

Magics: `TTRI` trie, `TPOP` population, `TSUM` summaries, `TAGG` rollups;
`TIDA` active index and `TIDC` mature index. Trie, population, and rollup payloads
retain format-2 encodings. There is no header on the CBOR map. All disk blocks use:

```text
payload_length:u32 | CRC32C(payload):u32 | payload
```

An index starts with exactly one 20-byte scope payload:

```text
start_unix_ms:u64 | end_unix_ms:u64 | owner:u32
```

Bounds are start-inclusive/end-exclusive. The first run starts at byte 36.
Scope/header checks and explicit magics determine row width; names or lengths do
not substitute for format validation.

Format-3 active run payload:

```text
count:u32 | [uuid:16 | leaf:u32 | population_start:u64 | population_end:u64]...
```

Format-3 mature run payload:

```text
count:u32 | population_start:u64 | [uuid:16 | leaf:u32 | population_end:u64]...
```

Format 4 changes only the per-event leaf width in these runs:

```text
active: count:u32 | [uuid:16 | leaf:u16 | population_start:u64 | population_end:u64]...
mature: count:u32 | population_start:u64 | [uuid:16 | leaf:u16 | population_end:u64]...
```

Active rows shrink from 36 to 34 bytes; mature rows shrink from 28 to 26 bytes.
Population addresses remain U64. Trie parents, scope owner IDs, summary IDs, and
in-memory traversal IDs remain U32. Format-4 tries allow at most 65,536 nodes
including root zero. Once a new path would exceed capacity, its fields stay in
population and the event uses leaf zero; existing paths remain reusable.

The file header and immutable definition explicitly select the format. Existing
format-3 definitions and files are not silently rewritten. `migrate-events` now
produces format 4 in a separate root/new definition minor, preserving node ordinals
and exact population values; it rejects source tries over capacity.

Rows are strictly UUID-ordered within each run. Separate runs can overlap in time.
For a mature row, its start is the prior **physical row's** end; row zero uses the
run base. A matching-row predecessor or a merged chronological predecessor is
never valid. Population is contiguous within a run, one CRC-framed block per run.
An empty run is invalid. The first population value normally starts at byte 16.

For N rows, mature versus active run savings are `8*(N-1)` bytes. File metadata,
additional files and tiny runs are separate costs. The limit remains 16 MiB per
block and 65,535 bytes per population value.

## Commit, maturity and recovery

The writer syncs trie, then population, then index. A whole bucket becomes mature
when `bucket.end <= writer_now - 15 minutes`. A current hour can consequently
remain active for roughly 75 minutes. Its complete runs remain searchable.

Maturity writes a complete checksum-verified compact replacement in the same
directory, syncs it, renames it atomically, and syncs directory metadata. It never
moves population bytes. Existing readers keep the old descriptor; new readers use
the new inode. Temporary files are excluded from discovery. Late events append
new runs even in mature files. Maturity is not a promise of permanent completeness.

Follow tracks committed run positions, population ends and a canonical prefix CRC
(UUID, leaf, end). On active-to-mature replacement it verifies the consumed prefix
and resumes after that prefix, including newly appended older UUIDs. Other index
replacement or truncation is an error requiring a fresh search.

`events.sum` is deliberately **per owner/bucket**, beside its index. Its five-minute
bucket numbers are elapsed intervals from the local day's first instant. Its
progress watermark is the population end, which does not change during index
compaction. This avoids inventing a day-wide watermark spanning unrelated files.
Day/month/week/year rollups combine these contributions separately. Legacy summary
watermarks remain index offsets. Migration rebuilds summaries; previously published
rollups are derived artifacts and are not copied under changed schema IDs.

## Resident search

The Go server starts one Rust reader lazily and reuses it over a private Unix
socket. External clients still use the existing local socket or HTTP/2/HTTP/3.
The internal IPC carries length-framed encoded output and a terminal success/error;
Go does not expand event values. A private parent pipe terminates the reader when
the server exits. Request cancellation closes IPC and interrupts scans/following.

The reader caches checksum- and structure-validated packed index blocks with a
24-hour event-time target, a 256 MiB payload budget, at most 8,192 blocks and 128
file incarnations. Eviction can occur before 24 hours. Retained descriptors prevent
inode reuse from aliasing a cached block. Catalog/lineage caches are bounded per
request worker (eight catalogs / estimated 32 MiB). No persistent predicate-result
LRU is introduced. Population reads and rendering still incur their own costs.

`TRUNK_SEARCH_WORKERS=1..8` controls partition scan concurrency, default 4. This is
per request; `serve --queries` separately bounds simultaneous requests. The parent
captures index descriptors before refreshing shared catalogs. Workers apply time
bounds and trie decisions; an ordered merge applies global head/tail limits after
population-dependent predicates. `--local` remains the offline process reader.

Directory discovery and file metadata still run on refresh. This is not yet the
proposal's commit-notification inventory. Output retains the existing run cursor
model, whose descriptors and open files scale with selected overlapping runs/files;
it is not a claim of constant total query memory or open handles for arbitrary
partition counts. The cache budget does not include that query state.

## Migration and browser boundary

```sh
trunk migrate-events --from ./logs --to ./events --timezone America/New_York
```

Stop ingestion first. The command holds source writer locks, recovers incomplete
tails, stages a new sibling root, assigns new schema versions, preserves dictionary
codes and every trie ordinal, repartitions population with its rows, and rebuilds
summaries. It verifies counts and an order-independent SHA-256-based fingerprint
of UUID + leaf + exact population bytes before atomic publication. `migration.json`
records revision mappings and counts. Source files are retained, and the command
does not change service configuration or delete either root. Point the service at
one root after review and update CBOR producers to the report’s new revisions and
policy. Reinstalling old producer definitions would resume their legacy layout;
replaying the same UUID across schema versions is not server-side deduplication. Rollback after additional writes requires reconciliation.

The browser can decode PIL from either disk format and from timezone-aware maps.
Its existing IDB/OPFS event cache remains format 2. Native package negotiation,
source-preserving browser replication, revision-aware package coverage and lazy
population downloads are the next stage of the proposal; this server prototype
does **not** label an ordinary PIL event stream as a native index package.
