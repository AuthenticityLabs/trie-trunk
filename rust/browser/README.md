# Browser store kernel

Small Rust/WASM adapter between negotiated PIL frames and trie-trunk format 2.
The host provides synchronous I/O for **two files**, `log.tri` and `log.pop`.
The browser no longer writes or reads `log.idx` during normal operation.

`put` borrows PIL/CBOR fields, translates dictionary references to assigned store
codes, and stages lineage and population bytes. `commit` flushes both files and
returns packed 42-byte records: UUID (16), leaf (2), population start (8), end (8),
and CRC block address (8), all big endian. The block address replaces information
previously derived by scanning index runs. `get` accepts one such record and emits
PIL, preserving ABSENT, NULL, fallback values and nested CBOR. There is no JSON
projection or expanded event model in this path.

## Index and files

The dedicated browser worker owns a credential-scoped IndexedDB database,
`snoot-events-<SHA256 credential>`:

- `events`: primary key `[tap, binary UUIDv7]`; value `{catalog, pointer}`. UUIDs
  remain 16 bytes. `pointer` contains the remaining 26 bytes of the packed record.
  The primary key already supplies uniqueness and chronological ordering; no
  duplicate UUID or timestamp index is created. Tap/UUID identity spans schemas.
- `files`: committed end offsets for each catalog trie and day population file.
- `catalogs`: immutable catalog locations and tap/schema identities.
- `collections`: original selections and completed predicate-specific time ranges.
- `members`: unique `[collection, tap, UUID]` references preserving exact membership,
  including partial, canceled, limited and follow collections. These replace the
  old per-collection OPFS journals; repeated deliveries within a collection do
  not add references. Separate collections still have their own memberships.
- `state`: migration completion marker.

Population and dictionary data remain in OPFS:

```
trie-trunk-v2/<SHA256 credential>/
  taps/<SHA256 [tap, schema digest]>/
    tap, ready
    log.map                 original immutable tap schema CBOR
    log.pil                 cached PIL definition CBOR
    log.tri                 shared native trie
    <UTC year>/<ordinal>/
      log.pop               native CBOR population blocks
```

The `.tri`, `.map` and `.pop` layouts remain compatible with their server formats.
A complete server datastore would additionally require rebuilding its native
`.idx` from the browser index. Server format 4 also uses U16 leaf references in index rows; browser payload
files retain their existing format-2 framing.

## Publication and recovery

An origin-wide `trie-trunk-files-v1` Web Lock serializes operations across tabs and
also excludes old workers during migration. Sync OPFS handles close before the
lock is released. IndexedDB transactions contain only database requests, never
file operations, network calls or unrelated awaits.

1. Check UUID existence in a batched read transaction; discard existing events.
2. Recover file ends to their IndexedDB commit boundaries and open the WASM store.
3. Append and flush new trie nodes and population blocks.
4. In one strict-durability IndexedDB transaction, publish event pointers, file
   end offsets and collection membership. Deliver the batch only after commit.

A failed transaction leaves no published pointers. The next open truncates any
uncommitted file tails. A missing or shortened committed file is an error, not a
new empty cache. Trie CRCs are checked on load; population CRCs are checked when a
block is read and cached for subsequent rows. No full event UUID map or index-run
copy is retained in WASM. Up to eight day contexts retain trie state and one
population block each.

Reads use 512-record IndexedDB pages and group population reads by file/block.
A bounded per-query leaf cache avoids repeating shared-field comparisons. Sparse
or literal fields are checked in PIL only when necessary. Frames remain PIL until
the view reads individual fields.

## Cache planning

For finite, unlimited Collect requests, the worker reads matching local UUID
ranges first, then requests only gaps in completed coverage for the **same tap
selection and exact predicates**. Current UI equality controls are supported;
unsupported expressions, limited requests and follow streams use the server path.
A broader cached predicate is not yet used to certify a narrower one.

Only a successfully completed unlimited finite response certifies coverage.
The final 15 minutes relative to request start remain untrusted and are fetched
again. UUID deduplication absorbs overlap. A failed later gap cannot certify the
whole selection. An empty successful range can establish coverage. Cache-only
collections save their own completion metadata, and reload restores membership
without an event request.

## Existing caches

New pointers use U16 leaves. Existing 44-byte packed records / 28-byte IDB
pointers remain readable, so pointer rows may coexist without a cache rewrite.
Trie parents stay U32. New paths which would exceed 65,536 nodes (including root)
fall back to population fields at leaf zero; no field is dropped or reference wrapped.


On first use, v1 catalogs, committed index rows, collection memberships and coverage
are imported into v2. Index and population CRCs/bounds are checked; incomplete
index tails and uncommitted population appends are ignored. Import is restartable
and normal reads/writes are gated until it finishes. Active v2 storage contains no
`.idx` or collection journal files.

The original v1 directory is retained untouched as a migration backup. This also
isolates v2 from older open bundles that still use index files. Writes made by an
old bundle **after** migration are not automatically imported; reload those tabs
to use v2. Cleanup/retention is deferred in this prototype.

## Validation

`npm run wasm --prefix client` builds the codec and browser store. `npm test
--prefix client` exercises the actual WASM adapter and worker protocol, including
pointer bounds, block reuse, round-trip fidelity, cache-only delivery and failed
gap completion.

Real IndexedDB/OPFS tests run in an isolated Chromium profile with Playwright Core:

```
PLAYWRIGHT_MODULE=/path/to/playwright-core/index.mjs \
CHROME_PATH='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' \
node client/scripts/test-store-browser.mjs
```

The script bundles its test-only worker in a temporary directory. It covers
out-of-order inserts, bounded ordered reads, overlapping and cross-schema UUIDs,
tap/credential isolation, concurrent workers, interrupted publication and migration,
exact PIL restoration, predicate-specific gaps, and the absence of `.idx` in v2.

IndexedDB adds record overhead compared with a packed native index; this change
has not established throughput or capacity limits for million-event workloads.
There is no retention, compaction, or general analytics engine in this prototype.
