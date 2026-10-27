# Trie-Trunk

An open-source time-series database for typed events, temporal search, and
precomputed analytics. Optional collection indexing supports object revision replay.
Ingest typed events once; reuse complete hourly datasets and compact summaries in
the browser instead of repeating every investigation against the server.

[Website](https://trietrunk.com) · [Snoot](https://snoot.trietrunk.com) ·
[PIL Definition Schema](https://github.com/AuthenticityLabs/pill-packer/blob/main/schemas/definition.cddl)

Version zero: storage designs and fixtures can change without migration paths.
The sample datasets are disposable.

## Architecture

- **Go** manages HTTP connections, authentication, configuration, scheduling,
  sealing, retention, and response assembly.
- **Rust** owns resident PIL buffers, live identity directories, Tantivy indexes,
  and native search execution. Go calls Rust through an in-process C ABI.
- **Snoot** uses Solid, Unovis, and ECharts. A SharedWorker coordinates browser storage,
  PIL/WASM decoding, query execution, and cached complete datasets.
- **Data seeds** implement PIL’s typed positional records. **Seed packs** implement
  PIL PACs, grouping data seeds under a shared Definition with common values and
  optional compression.

This is not yet a fully native record engine. Go still allocates projection,
query-result, and sealing metadata. Measure native memory and process RSS along
with Go allocations; moving ownership alone does not establish a latency win.
The separate standalone CBOR store/search tooling is not Snoot's canonical path.

### Transaction Logging

The intended API/event strategy emits one outcome seed per transaction: `info`
for success or `error` for failure. Supplemental `warn` seeds retain their level
but use the debug storage container, alongside `debug`; `trace` has its own
container. Access remains a separate `read`/`write` field. The Go logger routes
warnings accordingly and classifies HTTP failures, including 4xx and cancellation,
as error outcomes. Cross-seed correlation and enforcement of the complete
outcome-plus-diagnostics strategy remain to be implemented.

### Data Lifecycle

Data seeds are the data records; seed packs group those seeds. Pods and Sods are
**datastores**, not names for individual seeds or seed packs. In both names,
**O** means observability and **DS** means datastore.

| Stage | File | Purpose |
| --- | --- | --- |
| Seeds — Sequentially Encoded Event Data | `.seed` | Observability Ingress — durability until the hourly seal completes, with recent data seeds in native memory for live tail; implementation of PIL |
| Packs — Packed Array Container | `.pack` | Seal & Compress the Hour — hourly grouping after the late-arrival window; implementation of PIL PACs |
| Pods — Packed Observability Datastore | `.pods` | Packed Data Storage — sealed server storage for seed packs; writes compress unless Collections is enabled for direct replay |
| Sods — Sparse Observability Datastore | `.sods` | Sparse Replication — decoded data seeds in sparse browser storage for complete collected hours |
| Stalks — Sparse Telemetry Analytic Lineage Kolumns | In Memory | Analytic Engine — analytic columns; summary columns exist, broader data-seed projection remains future work |
| Summaries | `.sum` | 5-minute columnar aggregates, daily compressed summary packets, and calendar rollups |

Stalks currently uses precomputed columnar summaries for browser analytics.
Lineage is a design direction: definition-declared relationships connecting
resources, such as payments and invoices, through identity and time. Relationship
traversal and broader server-side analysis are not implemented yet.

Research retrieves complete matching seed packs. Object history and replay use
separate access to individual data seeds. A missing client hour is unknown, not
an empty result. Only complete hours enter durable coverage. Summary requests
remain independent of event paging; paging must not reduce full-range counts.

“Event” still describes the meaning of a data seed. Index records and summary
packets are separate structures; they are not renamed to data seeds or seed packs.

```text
caches/<cache>/
  registry.cfg
  registry.map
  registry.tri
  <service>/<resource>.v<major>/
    collection.idx                 # optional object/revision lookup
    collection.state               # invalidation state
    <minor>/
      resource.map
      resource.tri
      resource.stats
      read.* / write.* / error.* / debug.* / trace.*
      lost.log
```

Branch time tries use positional UTC year/day/hour navigation. Branch indexes
carry fixed event rows; collection and Tantivy references identify the source,
hour, and record. Keep object identity separate from event identity. `level` and
`access` are independent query fields. A completed request is not automatically
a committed object mutation.

Configuration can inherit through `trunk.cfg`, `cache.cfg`, `service.cfg`,
`resource.cfg`, and branch configuration. Per-resource/branch policy controls
retention and compression. Source implementations live in
[`go/server/cache`](go/server/cache), [`rust/src`](rust/src), and
[`client/services`](client/services).

Snoot Cache Quotas inventories registered physical storage as Trunk → Cache →
Service → Resource → Branch. Resource minors are combined in the view. Branches
come from opened cataloged stores, not combinations of `level` and `access`
fields. File allocation is measured on disk; shared metadata stays with its
owning parent. Hourly recovery estimates use segment-size weights. Quota settings
are saved on the server, but automatic space recovery is not connected yet. The
allocation diagram uses a fixed height and one scale across all columns; zero
allocations remain zero and are still accessible through Select Node.

Configurations also exposes Compression, Staging, Indexing & Replay, and
Replication editors. These read the same inheritance chain as ingestion and
save explicit overrides into the existing scope `.cfg` files. Saves validate
values, preserve unrelated settings, and reject concurrent edits using file
revisions. Restart the server to activate saved changes. Transforms currently
provides read-only inspection of each resource definition’s `transforms.cfg`.
Quotas still uses its separate `quotas.cfg`; consolidation into the scope files
remains pending.

## Build and Run

Requires Node 22.12+, Go 1.24+, CGO with a C linker, and Rust 1.89+ including
`wasm32-unknown-unknown`. Build Rust before the Go executable.

```sh
rustup target add wasm32-unknown-unknown
make build
./trunk ingest 5410 serve 8443 --http-address 127.0.0.1:3020
```

Runtime configuration, credentials, certificates, and caches are local and
ignored. See [`.env.example`](.env.example) and the CLI implementation under
[`go/cmd/trunk`](go/cmd/trunk) for configuration. Deploy `trunk`, `trunk-search`,
and generated `web/` together. Restart after rebuilding native executables;
frontend-only builds are read from disk without restarting the datastore.

```sh
make web                         # rebuild Snoot
make site                        # build public website into site/dist
node site/preview.mjs             # local website on 127.0.0.1:3030
```

On the development Mac, `trunk-server` is the launcher for the configured snapi
service. It loads local environment configuration and uses `exec` so the server
is supervised directly. Snoot's Tunnel origin is `http://localhost:3020`.
Do not replace that Tunnel when deploying the public website.

### Public Website

The separate Cloudflare Pages project is `trietrunk`, with navigation for
`/trunk`, `/seeds`, `/logger`, and `/snoot` at `trietrunk.com`. Seeds includes
Overview, Definitions, Collections, and Messaging; `/PIL` holds the underlying
specification and Pill Packer library reference. Build and deploy with:

```sh
node site/build.mjs
cd site
wrangler pages deploy dist --project-name trietrunk --branch main
```

The website uses checked benchmark JSON at build time and generates its
measurement download without depending on local Markdown reports. Shared artwork
uses the front-facing Snoot anteater for all logos and lockups; the side profile
is reserved for profile/avatar icons. The trunk-sniffing composite is not used.
Snoot offers Light, Dark, and Auto themes; saved preferences persist.

Snoot duration comparisons default to milliseconds: `events | where duration <= 10000`
means at most ten seconds. Explicit `ns`, `us` (or `µs`), `ms`, and `s` suffixes
override that default. The Definition’s duration role identifies the field;
storage retains integer microseconds. Indexed and residual comparisons normalize
the operand exactly, without rounding stored values. Search Guide and field
completions expose the query unit. Refresh preserves the selected time window
independently of disposable scroll geometry.

Native collection rejects conflicting physical generations before publishing
coverage. A recreated or restored server dataset can have the same Definition
and generation sequence as an older local replica but a different generation
ID; that is an identity conflict, not evidence of damaged event bytes. Temporary
publication races retry once with fresh coverage. Persistent conflicts keep the
existing replica intact and expose **Refresh Local Cache** beside the error.
Search gathers conflicts across the page instead of stopping at the first
failed resource. That action resets all identified resource caches in one
durable transaction, fences in-flight writes, and reruns the search. If a
refreshed resource still conflicts, Conflict Details replaces the refresh loop. Other resources, dashboard preferences and server
data are retained. Worker errors preserve generation IDs, sequence numbers and
state epochs for diagnosis in the console; corruption/checksum errors are not
silently converted into retryable conflicts.


Native event storage uses OPFS under `snoot-store/<credential hash>/`. Each
branch holds its sparse `events.sods`, `events.idx`, resource trie/definition
files, and raw time trie. Committed time-trie leaves determine availability;
there is no second per-event IndexedDB index or persisted package-coverage list.
Allocation/decoding context is stored in `layout.<revision>.cbor`. The atomic
`commit.cbor` root publishes immutable layout and `branch.<revision>.tri`
snapshots after data files have been flushed. Native responses queue up to 4 MiB
or 4,096 control records per batch (a single larger PAC is flushed immediately).
Small responses publish once after wire completion, rather than committing
metadata for each decoded record. Each SODS hour is written with its header and
payload together (split only at physical sparse-page boundaries). Open file
handles, sizes, and retired-byte headers are reused within the batch; dirty
files are flushed before the time-trie snapshot is published. File metadata is reused within one origin
lease and reloaded at the next command, keeping cross-tab visibility intact.
Sealed search hours already present in the committed trie are read locally
without another collection request. Failed publication leaves the
previous committed leaves authoritative. Saved collection bookmarks use a
separate OPFS file, not IndexedDB. Compressed summary packets retain their
bounded Cache Storage cache.

Each hour has one status byte in the time trie. The low two bits encode unknown
(`0`), metadata-only (`1`), or committed event data (`2`); bit `0x04` independently
records a committed replicated Tantivy index. Thus an index can be queried before
its event blocks are collected. Receiving data preserves the index flag; evicting
an index leaves event coverage intact. A known hour with zero records remains
distinct from an unknown hour. Index flags must be published only after verified
index files are flushed; event flags still follow the `.sods` flush. The flag
helpers exist, but the index-replication transport has not yet been connected.
The byte layout adds 6,588 bytes per allocated calendar year per branch compared
with the previous two-bit map. Version-zero directory fixtures are regenerated;
old local directory files require resetting the disposable local cache.

### Client Tantivy Replication

The read-only WASM reader in `rust/search-wasm` opens immutable Tantivy 0.25
files through synchronous worker file handles. It shares schema construction,
document indexing, text modes, and typed predicate execution with the native
server through `rust/search`. PIL/PAC encoding and compression remain in
pill-packer. The reader has a bounded 8 MiB cache of 64 KiB index blocks;
posting/column reads reuse those blocks instead of making one OPFS call per
small value. Original event payloads are not stored in Tantivy.

This reader is tested separately; Snoot's active Search still uses `/events`.
Index replication and the live planner have not yet switched to it. The
remaining integration uses indexes to decide which event data to retrieve:

- Traverse the selected resource/window's time trie and replicate missing
  immutable hourly search indexes alongside their time-trie records. Reuse
  cached indexes and search each segment as it becomes available. Event-data
  collection is not a prerequisite for executing the query. Current branch-wide
  mutable indexes cannot be copied as hour-scoped snapshots.
- Bind each index to the resource, Definition, physical generation, hour and
  event-address range. Index availability and event availability are distinct:
  receiving an index never publishes a `.sods` coverage leaf. Flush `.sods`
  before committing its event-data time-trie leaf. Unknown hours are never
  treated as empty.
- Search indexes to obtain candidate event addresses, then collect only the
  missing hourly seed packs needed for the requested result page or analytics.
  Render progressive results while remaining indexes arrive; final counts need
  complete index evaluation, not complete event replication. Predicates that
  require payload evaluation fetch candidate hours before finalizing matches.
  Exact partial-hour bounds, duration normalization, residual
  predicates, pagination and full-range statistics must match the existing
  server path before cutover. Open/live hours need explicit handling rather
  than a false completeness claim.
- Reuse collected hours/indexes across query changes. Full-window event
  collection is an explicit workload requirement, not the default search plan.
  Credential isolation,
  stale-generation invalidation, cancellation and storage quota failures must
  share the existing collection transaction behavior. No index bytes belong
  in IndexedDB or session storage.

Build the reader with `node client/scripts/build-search-wasm.mjs` after installing
`wasm-bindgen-cli` 0.2.129; `WASM_BINDGEN` can point to an isolated installation.
This is build tooling, not an application runtime. Generate native parity
fixtures with `cargo run --release --locked --manifest-path rust/Cargo.toml
--example search-replication -- <new-output-directory> 256 2000`. Serve the generated
directory at `/fixture`, generated `client/assets/search` at `/search`, and
`client/scripts/test-search-replication-worker.mjs` as a module worker on a local
secure origin. Post one message to start; it writes verified files into a
temporary OPFS directory, compares browser results to native results, reports
timings, and removes its test files. Measurements in
`benchmarks/retrieval/2026-10-27-client-tantivy` cover 512,000 synthetic events
across 256 hourly indexes. They are reader parity/compute measurements, not
end-to-end application latency or product comparisons.

Dashboards has a collapsible Data Seeds directory and a Data Seeds dialog
backed by the authorized `/caches` catalog. Subscriptions target services within a cache and automatically cover their
current and future catalog resources. Resources are dashboard context, not
individual subscriptions. IndexedDB is limited to the `snoot-profile` database
and its `preferences` store: theme, user-authored highlighting rules, and service
subscription choices. Selected service, drawer state, page, query, time window,
and other tab settings live in session storage under readable `snoot:` keys.
Search results, event bytes, definitions, and trie fragments are never copied
into session storage. A reload reruns the query using the local file cache.
Subscription preferences use a `dashboard-services:` key partitioned by a
SHA-256 digest of the trimmed connection token.
The token itself is not stored there; changing credentials selects a separate
workspace. Atomic read-modify-write transactions prevent lost cross-tab updates,
and BroadcastChannel refreshes other open dashboard views after commits.
Unavailable services retain their saved subscription until explicitly removed.
The subscription dialog moves services between Selected and Available lists with
add/remove buttons. Subscribe commits the changes atomically and starts real
summary collection. A workspace-level coordinator continues when switching menu
pages, resumes saved subscriptions on reload, and cancels removed services.
The shared worker collects UTC year-to-date windows newest first, one background
window and one daily packet at a time. Concurrent tabs share the same service
job; requests use `include=summary` and never collect event pages or seed packs.
Completed subscriptions refresh the current and previous UTC day every five
minutes. New catalog resources trigger another year-to-date pass. Older history
is fetched on demand through summary queries, not eagerly replicated.

The Fast dashboard uses one shared scope and time selection for all panels.
Its default layout is defined in
[`client/services/fast-dashboard.json`](client/services/fast-dashboard.json), with
TypeScript contracts in `client/services/fast-dashboard.ts`. JSON is an advanced
representation, available under the collapsed **Default Chart Definition**;
ordinary use is through service, resource, and time selectors. No chart-editor,
JSON import, or layout persistence is implemented yet.

The first layout uses a 12-column desktop grid, two columns on narrower desktop
views, and one column on mobile. Panel order is document order; `span` is 3, 4,
6, or 12. The default contains four values, two timecharts, Top/Bottom 10 resource
bars, services with sparklines, observed levels, and a resource table with CSV
export. Boolean status widgets remain supported but are not in the default
layout. Bottom 10 includes active resources only.
Value widgets accept `view.indicator`: `trend` (Trend Arrow), `sparkline`
(Sparkline), or `none` (None, also the default when omitted). This is a display
choice for the same metric and selected window, ready for a future toolbox
selector. Sparklines show the bucket-by-bucket shape with a zero baseline and
gaps for missing samples; arrows summarize the direction and degree of change.
The four headline statistics use `view.indicator: "trend"` for an arrow comparing
the earlier and later halves of the selected window. Counts and histograms are
combined in the summary worker; means are weighted and P95 uses merged histogram
weights, never averaged percentiles. Tilt is 90 degrees times the difference
divided by the larger value: doubling tilts up 45 degrees, halving tilts down 45.
Changes below 1% remain horizontal. Missing comparison data shows a dash. Hover
text and accessible labels give both half-window values. This adds no requests
or event scans; service-list sparklines remain unchanged.
The **Deep** tab is disabled until event-based analysis is implemented.

### Chart Definition Contract

A dashboard has `id`, `title`, `mode`, `source`, `scope`, `time`, `layout`, and
`panels`. In the initial implementation, `mode` is `fast`, `source` is
`summaries`, and `scope` is `subscriptions`. The selected service and resource
narrow that subscribed scope at runtime. The default time selection is `30d`,
UTC, complete days; selectors also offer `7d` and year to date. Hours are used
for charts spanning up to seven days; larger ranges use days. This is display
aggregation over the underlying five-minute summary cells.

Each panel separates its result shape from its visual presentation:

| Result Shape | Representation | Compatible First Views |
| --- | --- | --- |
| `number` | One aggregate value; null means no samples | `value` |
| `boolean` | A typed comparison, initially `gt` against a number | `status` |
| `set` | Distinct observed dimension members with counts | `tags` |
| `table` | Grouped metric rows | `table`, `bars`, `list-trends` |
| `series` | Ordered time buckets with metric values | `timechart` |

A hash set is a possible execution structure for a `set`; JSON represents its
members as an array. CSV is an export of table results, not another query type.
Top/Bottom 10 are `order` plus `limit`, applied after aggregation. A trend is a
time series rendered compactly; a list with trends binds each group to its own
series. Missing duration samples remain null, not zero.

```json
{
  "id": "top",
  "title": "Top 10 Resources",
  "span": 6,
  "data": {
    "shape": "table",
    "metric": "events",
    "groupBy": "resource",
    "order": "desc",
    "limit": 10
  },
  "view": { "kind": "bars", "unit": "count" }
}
```

`metric` currently accepts `events`, `errors`, `errorRate`, `average`, and `p95`.
`groupBy` accepts `service`, `resource`, and `level`. `view.unit` is `count`,
`percent`, or `ms`; duration aggregates retain the duration role's microseconds
until display conversion. CSV headers explicitly identify microseconds and
percentages. A table's `view.columns` lists metrics; a boolean's `data.test`
contains `{ "op": "gt", "value": 0 }`. These are typed operations, not arbitrary
JavaScript or a chart-library configuration object.

For the future visual editor, the proposed flow is **Scope → Aggregate → Display**.
Dragging a toolbox item or choosing a compatible display creates this same JSON.
The result shape determines offered displays. Default layouts should stay
resettable; saved overrides and custom Fast layouts may rearrange or display
only summary-supported metrics. A request requiring missing dimensions or raw
fields belongs to Deep rather than silently falling back to event scans. Field
IDs, units, optional predicates, and additional reducers will be tied to the
Definition engine as that capability becomes available.

Fast performs packet decoding and histogram merging in the shared worker and
returns chart-sized aggregates. It reuses the same credential-partitioned summary
cache as subscription priming, obtains fresh manifests in seven-day windows,
and does not download event pages or seed packs. The first implementation uses
complete UTC days, up to one year; it is not a live-tail dashboard. It has no
independent polling loop: Refresh or a new completed subscription collection
reloads the selection. Unsubscribing or changing scope cancels the visible request.
Weighted duration means and nearest-rank p95 are calculated after merging
histogram weights, never by averaging averages or percentiles. Groups are ranked
only after combining all selected sources. Per-resource and service keys retain
cache identity to avoid collisions.

Some inventoried branches have event counts without precomputed summary columns.
The dashboard explicitly reports summarized events versus all matched events;
all displayed metrics and groups refer to the summarized population. It does not
invent per-resource metrics for uncovered events or scan those events to fill
missing summaries. Tables export the same displayed population. Summary dimension
incompleteness, duplicate sources, or overlapping requested scopes are errors.

Compressed sealed packets use a 256 MiB Cache Storage budget rather than a packet
count limit; decoded summaries retain their existing 32 MiB memory bound. Cache
pressure can evict older packets, so completing a pass is not a permanent offline
coverage guarantee. Unsealed packets remain in memory and are refreshed; priming
reports persistent-storage failures and offers Retry Collection. Fast charts use
these packets; Deep event analysis remains pending. Search has Events and Charts tabs sharing the same
query and time window. Switching tabs preserves loaded event pages without
restarting retrieval; the selected tab is restored with the search session.

## Snoot for macOS

Snoot's desktop app runs the same frontend, workers, and pill-packer WASM codecs
inside a Neutralino WebKit window. A bundled Go bridge serves the frontend and
streams server requests. There is no Node.js runtime in the app.

Build on macOS with Go, Python 3, Xcode command-line tools, and the existing
frontend build dependencies installed:

```sh
make desktop
# Repackage an already built frontend:
python3 desktop/build.py --skip-web
```

The default build produces `desktop/dist/Snoot.app` and
`desktop/dist/Snoot-macOS-universal.zip` for Apple Silicon and Intel Macs running
macOS 14 or later. The app uses the front-facing Snoot icon. Neutralino 6.10.0 is
pinned and its download checksum is verified. Local builds are ad-hoc signed;
public distribution still needs Developer ID signing and notarization.

Launch the app normally for HTTPS, or select a local Trunk socket:

```sh
open desktop/dist/Snoot.app --args --socket "$PWD/caches/.trunk/search.sock"
```

The first launch creates `~/Library/Application Support/Snoot/connection.json`:

```json
{
  "endpoint": "https://snoot.trietrunk.com",
  "socket": "/absolute/path/to/caches/.trunk/search.sock",
  "port": 34185,
  "transport": "auto"
}
```

The socket is optional. When available at startup, requests use it; otherwise
they use the configured HTTP(S) endpoint. Streaming responses, SSE, and
WebSocket upgrades pass through the bridge. A failed request is not replayed
against another server. Restart to select a different connection. Remote
endpoints require HTTPS; local HTTP endpoints are supported.

In the app, **Connection** shows the active transport and server/socket address.
Choose **Automatic**, **Unix Socket Only**, or **HTTP(S) Only**, then **Save
Connection** and restart Snoot. Automatic prefers the socket and falls back at
startup. Unix Socket Only reports an unavailable socket instead of falling back;
HTTP(S) Only always uses the endpoint. A saved choice never changes an in-flight
session's transport.

Edit `connection.json` to change addresses. `--endpoint`, `--socket`, `--port`, and
`--transport auto|socket|http`
override it for one launch. `SNOOT_SOCKET`, `TRUNK_SOCKET`, or
`TRUNK_DIR/.trunk/search.sock` supply a socket when none is configured.
Keep the loopback port stable: WebKit associates storage with that origin.
The bridge listens only on loopback and requires an application session cookie.

Events remain in OPFS, profile preferences in IndexedDB, and page/search state
in session storage. Desktop storage is separate from Safari's website storage.
Neutralino does not remove WebKit's storage quotas; an adapter for native
filesystem storage remains separate work.

The packaged app can verify its native WebKit capabilities without screenshots:

```sh
desktop/dist/Snoot.app/Contents/MacOS/snoot --self-test \
  --state-dir /tmp/snoot-check --port 34186 \
  --socket "$PWD/caches/.trunk/search.sock"
```

This checks server access, frontend loading, workers, WASM, OPFS synchronous
read/write, IndexedDB scope, and viewport overflow, then exits. Results are
written to the selected state directory as `self-test.json`. `--inspect` enables
WebKit developer tools when needed.

## PIL

PIL/PAC codecs and their authoritative specification now live in the sibling
[Pill Packer](https://github.com/AuthenticityLabs/pill-packer/blob/main/README.md) library. Keep it beside this checkout at
`../pill-packer`: Rust path dependencies, the Go module replacement, the local npm package, fixture tests, and the
site build use that path. The packages are not yet published. This repository owns
[Trie-Trunk's definition profile](schemas/trunk-definition.cddl), routing, storage,
query semantics, and HTTP registration. Pill Packer owns generic encoding,
decoding, definition documents, PAC factoring/framing, gzip and numeric column
transforms; it has no cache/service/resource policy. The Go codec is imported
from `github.com/AuthenticityLabs/pill-packer/go/codec`; browser helpers come from
`pill-packer` exports. Trunk adapters supply resource roles and TPIL control
schemas, without maintaining another PIL field encoder or PAC parser.

The existing `.sum` container is still a Trie-Trunk-specific columnar format,
not a PIL PAC. Its gzip, base-residual and shuffle primitives now use Pill Packer;
aggregate selection and the hourly/daily storage schema remain here. Moving codec
ownership does not by itself convert that storage format. The fixed-width PAC
provision is documented in [Pill Packer's design](https://github.com/AuthenticityLabs/pill-packer/blob/main/README.md#fixed-width-pac-design).

The definition-engine registration plan is JSON folders/upload/editor → portable
structure/codec checks → Trie-Trunk profile checks → immutable CBOR → publication.
Persist definitions as CBOR so they can bootstrap without a preloaded PIL schema.
Keep source text separately for editor fidelity. Unknown metadata must survive
forwarding; required validation must be explicitly supported, not inferred from
an annotation. Folder ingestion and editor/upload UI are pending.

PIL carries complete objects or partial changes. Definitions specify ordered
fields, primitive types, dictionaries, and integer-encoding options. Definitions
are CBOR; [`definition.cddl`](https://github.com/AuthenticityLabs/pill-packer/blob/main/schemas/definition.cddl) describes their structure.
Absent, null, direct, and offset-backed field states remain distinct. `REF16` is
optional. Adaptive integers are configured per Definition.

Trie-Trunk implements PIL records as data seeds and PIL PACs as seed packs.
A seed pack groups data seeds under one Definition and may factor shared values. Its
readable compressed wrapper describes the packed body; decoding is owned by the
client library rather than HTTP content decoding. Storage placement and query
execution belong to Trie-Trunk, not the interchange format.

See [`../pill-packer/rust/src`](https://github.com/AuthenticityLabs/pill-packer/tree/main/rust/src), [`../pill-packer/rust/tests`](https://github.com/AuthenticityLabs/pill-packer/tree/main/rust/tests), and the cross-language fixtures
for the implemented format. Format experiments replace fixtures; old benchmark
snapshots are not compatibility contracts.

## Logger

The Go PIL logger registers a Definition before its first write. It exchanges
identity and digest, sends missing maps, and validates a compatible server layout.
`WriteValues` encodes positional values into the accepted layout; `WriteFrames`
accepts frames under the producer's original Definition. Extensions can use
explicitly enabled `@fields` and be promoted by compatible newer Definitions.

```go
mapBytes, err := os.ReadFile("resource.map")
if err != nil { return err }
logger, err := pil.Open(ctx, pil.Config{
    URL: trunkURL, Cache: "prod", Branch: "write",
    Token: token, Definition: mapBytes,
})
if err != nil { return err }
defer logger.Close()
return logger.WriteFrames(ctx, frames...)
```

Import `trie-trunk/go/logger/pil`. The example assumes application-supplied
context, endpoint, token, and PIL frames. For HTTP instrumentation, use
`client.NewPIL`; see the [runnable example](go/logger/client/examples/http/main.go).

`BatchMilliseconds` optionally batches concurrent producers; zero sends
immediately. HTTP gzip compresses batches before ingress decompression. Revisions
are preserved without coalescing. Calls wait for staging acknowledgement rather
than succeeding on local queue insertion. Observe delivery errors and retain
stable event identities for retries. See [licensing](#licensing) for the current
SDK dependency-boundary caveat.

## Validation

```sh
make test                        # native Rust/PIL and Go suites
cargo test --locked --all-features --manifest-path ../pill-packer/rust/Cargo.toml
npm --prefix client test         # codec, store, query and UI logic tests
npm --prefix client run check    # TypeScript
node site/build.mjs
```

Browser/OPFS probes under `client/scripts` are separate from the unit suites.
They require a supported browser runtime; a passing CLI build does not establish
browser visual correctness, Safari storage behavior, or production performance.

## Measurements

**The full Trie-Trunk datastore is 70.9% smaller than expanded JSONL** in the
million-event synthetic fixture, including indexes, summaries, journals, and
metadata. Collections is disabled; ordinary writes use compression too.

| Artifact | Decimal MB |
| --- | ---: |
| Expanded JSONL | 427.75 |
| Complete Datastore, Including Indexes and Summaries | 124.65 |

The separate codec comparison uses identical hourly boundaries for both formats.
Seed packs retain complete UUIDs and routing context, with definitions and packet
headers included; no external index is needed to decode them.

| Compression | Hourly JSONL | Hourly Seed Packs | Less Storage |
| --- | ---: | ---: | ---: |
| Gzip-1 (Default) | 37.09 MB | 19.98 MB | 46.1% |
| Gzip-6 | 31.36 MB | 17.98 MB | 42.7% |
| Gzip-9 | 29.67 MB | 17.93 MB | 39.6% |

Compression/decompression time for all one million events, median of three serial
runs on Apple M1 Max (Go 1.26.4, one worker):

| Setting | Format | Compress | Decompress |
| --- | --- | ---: | ---: |
| Gzip-1 | JSONL | 1.04 s | 0.60 s |
| Gzip-1 | Seed Packs | 0.84 s | 0.33 s |
| Gzip-6 | JSONL | 2.05 s | 0.50 s |
| Gzip-6 | Seed Packs | 1.83 s | 0.24 s |
| Gzip-9 | JSONL | 3.40 s | 0.48 s |
| Gzip-9 | Seed Packs | 3.33 s | 0.24 s |

The fixture spans 30 days and five resources, with repeated messages and dictionary
values. It cycles through 250 object IDs per resource, not one million independent
objects. Every event was read back and every standalone seed pack was round-trip
verified. Production results depend on the data distribution. The 8 GiB native
budget per resource is a bulk-test limit, not measured RSS.

Gzip-1 is the default; levels 6 and 9 are configurable. Gzip is currently the
supported compressor. Timing results measure compression/decompression separately
from PIL packing, JSON serialization, disk I/O, and queries.

Raw accounting and measurements:
[`benchmarks/storage/2026-10-26-million-events-standard`](benchmarks/storage/2026-10-26-million-events-standard).
Generated exports, packet snapshots, and codec inputs remain local.

```sh
export TRUNK_FOOTPRINT_OUT="$PWD/benchmarks/storage/2026-10-26-million-events-standard"
TRUNK_FOOTPRINT_EVENTS=1000000 TRUNK_FOOTPRINT_COLLECTIONS=false TRUNK_FOOTPRINT_NATIVE_BYTES=8589934592 go -C go test ./cmd/seed-demo -run '^TestDemoFootprint$' -count=1 -timeout=60m -v
TRUNK_SUMMARY_MATCHED="$TRUNK_FOOTPRINT_OUT" go -C go test ./server/cache -run '^TestSummaryMatchedFootprint$' -count=1 -v
TRUNK_COMPRESSION_MATCHED="$TRUNK_FOOTPRINT_OUT" go -C go test ./cmd/seed-demo -run '^TestStandalonePACCompression$' -count=1 -v
TRUNK_COMPRESSION_TIMING="$TRUNK_FOOTPRINT_OUT" TRUNK_BENCHMARK_CPU="$(sysctl -n machdep.cpu.brand_string)" go -C go test ./cmd/seed-demo -run '^TestHourlyCompressionTimings$' -count=1 -timeout=10m -v
```

### Retrieval API Measurements

The October 26 retrieval run uses a **native Go client against the running
Go/Rust server**, with the 50,000-seed demo across five resources and 25 branches.
The query window is September 25–October 26 UTC. The machine is an Apple M1 Max
with 32 GiB RAM. This is separate from the million-event storage fixture above.

Public-path results below verify HTTP/3 on every response. Each row has ten
measured trials after one discarded setup trial. Server/OS caches are warm and
uncontrolled; each trial downloads fresh summary and seed-pack bodies. p95 uses
nearest rank and is the maximum of these ten observations, not a production SLA.

| API Workload | Scheduling | Median | p95 | Requests |
| --- | --- | ---: | ---: | ---: |
| 100 Seeds + Full-Range Summaries | Baseline: Four Requests per Stage | 9.87 s | 10.28 s | 481 |
| Same Seeds + Summaries | Experiment: Sixteen Requests per Stage | 2.97 s | 3.11 s | 481 |
| Same Seeds + Summaries | Experiment: Thirty-Two Requests, Overlapping Stages | 1.86 s | 2.06 s | 481 |
| 100 Seeds Only | Four Requests | 542 ms | 649 ms | 21 |

The scheduling experiments are benchmark options, **not deployed optimizations**.
All full-view runs returned the same selected seed identities. The 481 requests
comprise one search, 460 daily summary packets, and 20 hourly seed packs. Daily
packets belong to individual branches: five resources × (31 Info–Read days +
31 Info–Write days + 30 Error days). The first 100 seeds span two hours across
five resources and two branches. These requests transfer about 0.90 MB of HTTP
response bodies, excluding protocol headers/framing.

Local API delivery medians were 129 ms for seeds plus summaries, 60 ms for seeds
alone, 343 ms for a broad numeric query, and 468 ms for a broad text query.
The numeric and text queries spent roughly 330 ms and 401 ms, respectively, in
the initial search response. One-branch search responses were about 25 ms.
The scopes and matching populations differ; this comparison locates work rather
than claiming an equivalent-workload speedup.

Measurements include HTTP gzip decoding, routing metadata parsing, and summary
SHA-256 verification. They exclude seed-pack application decoding, SharedWorker
messaging, OPFS writes, chart calculation, and painting. No native coverage
receipts are sent between seed-pack downloads, so overlapping dependency bytes
may be transferred again. The separate `KnownMetadataMS` metric is one request
with cache receipts, not a complete warm-view time. No Node runtime is used in
this benchmark. No competing platform has been measured on this workload yet.

The browser now delivers the validated event page before loading chart summaries
from the same snapshot. Missing summary packs are selected through `POST /summaries`
with `{ "packs": [{ "source": "…", "definition": "…", "key": "…" }] }`, using identities from the
search manifest. The response is a streamed CBOR array of byte strings: each
entry is an unchanged daily summary pack in request order. There is no JSON/base64
payload conversion or weekly recompression. Requests contain at most 1,024 packs;
larger sets continue in bounded batches. All references are resolved and their
files pinned before response publication. Expired references return 409; truncated
or corrupt arrays fail without publishing incomplete chart totals. Verified packs
remain individually cached for retries. Client parsing awaits each pack before
reading the next, and background priming awaits persistence. Seven-day subscription
windows remain scheduling units, rather than seven separate HTTP responses.
Compressed summary storage
uses a 256 MiB byte budget. Search cache writes run off the foreground path with
an 8 MiB queue bound; explicit subscriptions still await persistence. The event
table mounts a measured viewport plus overscan. JSON details are generated only
for an expanded event, never for every loaded row.

An isolated Chrome check on the server Mac, without CPU/network throttling,
measured the same 50,000-match search with a 100-event first page:

| Measurement | Before | After |
| --- | ---: | ---: |
| Cold event data ready (one run) | 9.98 s | 1.32 s |
| Cold chart data ready (one run) | 9.98 s | 3.16 s |
| Warm next-frame proxy (median of three refreshes) | 269 ms | 117 ms |
| Mounted events at the top of the table | 100 | 8 |
| Page DOM elements | 9,046 | 1,257 |

The next-frame proxy uses two animation frames after the row DOM appears; it is
not an INP measurement. Network wait varied between runs, so these are local
before/after observations, not a cross-platform benchmark. Search timing now
reports event delivery; the separate Charts timing reports chart-data readiness
from the same search start. Both exclude painting. Raw browser measurements are
in [`browser-optimization.json`](benchmarks/retrieval/2026-10-26-demo-audit/browser-optimization.json).

The batched summary transport was checked separately on the same 50,000-seed
fixture (100-seed page, 460 daily packs, 20 hourly seed packs):

| Batched API Delivery | Median, Three Trials | Summary Requests | Total Retrieval Requests |
| --- | ---: | ---: | ---: |
| Local HTTP | 74.6 ms | 1 | 22 |
| Public HTTP/3 | 785.0 ms | 1 | 22 |

Each run discards one setup trial, uses a persistent transport, validates every
summary digest, and excludes the separate known-metadata probe from retrieval
counts/timing. Server and OS caches were warm and uncontrolled. These are later
observations rather than a controlled speedup comparison with the earlier runs.
The batching deterministically replaces 460 summary requests with one, while
preserving the pack contents. Results: [local](benchmarks/retrieval/2026-10-26-demo-audit/summary-batch-local.json)
and [public HTTP/3](benchmarks/retrieval/2026-10-26-demo-audit/summary-batch-public.json).
An isolated Chrome reload with its summary cache cleared also populated all 460
packs, rendered 50,000 matched seeds, and reported no errors; its event cache was
retained, so this is a functional check rather than a fully cold benchmark.

### Hourly Client Decode and Storage

Measured in Chrome on the Mac on October 27, 2026. These timings start with a
response already in worker memory; they exclude download and UI rendering.
The client uses pill-packer's Rust/WASM scanner, PAC decoder, decompressor, and
CRC32C verifier. Native hourly PACs are checked against their sealed manifest
checksum; member boundaries come from the checksum-verified event index. Reception
does not revalidate event field values. Definitions are compiled once per digest
in a bounded cache; binding context remains separate. Structural bounds, source
binding, and generation checks remain active. Full validation APIs remain available
for ingestion and diagnostics. The storage worker reuses the transport worker's
internal integrity result, then flushes `.sods` and indexes before publishing
committed time-trie leaves.

Sealed native collection now coalesces adjacent delivery windows and streams
contiguous `.pods` byte ranges from pinned file descriptors. Time-trie endpoints
locate the bytes without consulting event index rows. An empty or blank endpoint
walks backward to the preceding populated sibling and reads that PAC's length;
the trie layout is unchanged. Address lookup does not certify coverage: every
confirmed empty hour remains in the replicated trie patch, while blank hours
remain unknown. Index sidecars are copied without decoding, and the client retains
per-PAC integrity checks. HTTP range framing (TPIL tag 9, stream U32 and byte length
U64, then unchanged consecutive PAC bytes) avoids per-pack response markers,
payload allocations, length reads, and unused server SHA-256 calculations.

The in-memory directory uses approximately 143 KB per calendar year per opened
physical branch at default address widths (about 1 MB for seven years). Wider
fields, calendar boundaries, and publication copies add overhead; there is no new
directory copy per PAC. Bounded residency remains a future scaling consideration.

| Workload | Before | After |
| --- | ---: | ---: |
| 721 sparse hours / 6,232 events, decode and durable storage | 10.865 s | 0.465–0.527 s |
| Same sparse workload, file opens / flushes | 2,890 / 2,889 | 5 / 5 |
| 256 compressed hours / 1,048,576 events, decode only | 165–174 ms | 102–115 ms |
| Million-event decode and durable storage through production worker port | 705–784 ms | 482–561 ms |

The sparse baseline is one observed run; optimized ranges are three cold local
storage trials. Dense fixtures contain 4,096 events per hour and use the real
server sealer; fixture generation bypasses ingress and disables full-text index
construction. Every payload byte, event identity, and member boundary was read
back and compared for both fixtures. Failure-injection tests ensure failed
writes/flushes cannot publish trie leaves. These are client measurements, not
server ingest benchmarks or physical iPhone results.

Raw trials and codec/fixture hashes:
[hourly client results](benchmarks/retrieval/2026-10-27-hourly-client/results.json).
The follow-up [checksum/indexed reception results](benchmarks/retrieval/2026-10-27-structural-decode/results.json)
use the same million-event fixture. Its 256 PAC checksum checks take approximately
4 ms. Sparse production-worker storage remains 465–486 ms. All 1,048,576 dense
events and 6,232 sparse events were read back after timing and compared byte for
byte; corrupt input and failed storage cannot publish affected hourly trie leaves.
The opt-in fixture generator is `TestClientHourlyBenchmarkFixture` in
`go/server/cache/client_benchmark_test.go`, selected by `TRUNK_CLIENT_BENCH_OUTPUT`.
`node client/scripts/build-native-benchmark.mjs` builds the isolated worker at
`/bench-native/worker.js` for the existing Go static server. It accepts `prepare`
(with `tap/from/to` or a fixture URL), `decode`, and `write` messages carrying an
`id`; `ipc: true` uses the production storage-worker port and `verifyAll: true`
checks every hour after the timed write. Benchmark OPFS namespaces are separate
from user data. Remove `/bench-native/` and its benchmark namespaces after use.

With contiguous range delivery, the same million-event client workload measures
406–416 ms for decode plus durable storage (97–113 ms decode only). The sparse
month response shrinks from 1,282,141 to 843,074 wire bytes and stores in 404–407 ms.
Both workloads pass complete payload, identity, and member-boundary readback.
The local server comparison uses 256 packs / 65,536 events: approximately 1.03 ms
with per-pack reads and checksums versus 0.63 ms with range streaming. It excludes
network transmission. Raw measurements and source hashes are in
[PAC range results](benchmarks/retrieval/2026-10-27-pac-ranges/results.json);
`BenchmarkPACRangeDelivery` reproduces the server comparison.

Remaining optimization targets:

1. Prioritize foreground summary requests across concurrent subscriptions and
   tabs; measure the deployed batched transport under concurrent load.
2. Investigate Tantivy handle churn: the Rust registry permits 16 idle cached
   sources while this demo searches 25 branches sequentially. Verify opens and
   evictions before separating reader residency from writer budgets.
3. Coalesce native-store maintenance around collection batches while preserving
   flush, publication, and retention ordering.
4. Profile filtering, candidate materialization, and cross-branch merging with
   server phase timings. Broad queries currently evaluate branches sequentially.
5. Measure sustained scrolling and mobile Safari on physical devices. Loaded
   event bytes remain available for scrolling; windowing bounds rendered rows,
   not the accumulated result-byte cache or session checkpoint size.

Raw compressed samples, protocol evidence, source hashes, and the complete table:
[`benchmarks/retrieval/2026-10-26`](benchmarks/retrieval/2026-10-26).

```sh
go -C go test ./cmd/bench-retrieval
go -C go build -o /tmp/trunk-bench-retrieval ./cmd/bench-retrieval
/tmp/trunk-bench-retrieval -trials 10 -out /tmp/retrieval-local.json
/tmp/trunk-bench-retrieval -origin https://snoot.trietrunk.com -protocol 3 -scenario all-full -trials 10 -out /tmp/retrieval-h3.json
# Scheduling experiment only:
/tmp/trunk-bench-retrieval -origin https://snoot.trietrunk.com -protocol 3 -scenario all-full -parallel 32 -overlap -trials 10 -out /tmp/retrieval-h3-parallel.json
python3 benchmarks/retrieval/2026-10-26/summarize.py
```

## Licensing

Copyright 2026 trie-trunk contributors.

This repository uses different licenses for the application and the reusable
codecs and client libraries. These are component licenses, not a choice of
license for the entire repository.

| Component | Paths | SPDX identifier | License text |
| --- | --- | --- | --- |
| trie-trunk server, CLI, search engine, browser store, Snoot, and other repository-owned material outside the exceptions below | Repository default, including `go/server/`, `go/cmd/`, `rust/`, `client/`, and root build scripts | `AGPL-3.0-only` | [GNU AGPL version 3](LICENSE) |
| PIL codec, its WASM wrapper, specification, examples, tests and benchmarks | `../pill-packer/` | `Apache-2.0` | [Apache License 2.0](https://github.com/AuthenticityLabs/pill-packer/blob/main/LICENSE) |
| Shared Go encoding, dictionaries, schemas and UUID helpers | `go/codec/` | `Apache-2.0` | [Apache License 2.0](go/codec/LICENSE) |
| Go logger client, outbound transport, definitions, examples and tests | `go/logger/` | `Apache-2.0` | [Apache License 2.0](go/logger/LICENSE) |
| Shared Go error models | `go/models/` | `Apache-2.0` | [Apache License 2.0](go/models/LICENSE) |

Directory licenses apply recursively unless a file or a more specific component
explicitly states otherwise. Rust manifests and Snoot's package metadata identify
their component licenses. Generated code and binaries retain the licensing
obligations of their source; a generated asset's destination directory does not
relicense it. In particular, `pil.wasm` is built from Apache-licensed PIL, while
`trunk-store.wasm` includes the AGPL-licensed browser store.

Both licenses permit commercial use. The AGPL governs covered derivatives and
includes a source-offer obligation to remote users of modified network versions.
It does not prohibit competing forks or require contributions to this repository.
See the license itself for the full terms, including distribution obligations.

Independent clients and wrappers can use separately chosen licenses. Using the
Apache-licensed SDKs or PIL codec does not by itself place an independent
application under the AGPL. Copying or combining AGPL implementation code may
create a covered work; a wrapper or plugin label does not determine that question.
Current implementation caveat: `go/logger/pil` imports the compiler and types
from `go/server/cache`, so that SDK dependency graph currently includes AGPL
server code. Component labels do not remove dependency licensing obligations.

Third-party dependencies retain their own licenses and notices. These component
licenses do not replace third-party terms or remove existing notices. Retain the
applicable license texts and notices when redistributing components.

The [trademark policy](#trademark-policy) addresses product names and logos separately.
It adds no restriction to the software licenses' permission to use, modify,
redistribute, or fork the code.

Source repository: https://github.com/AuthenticityLabs/trie-trunk

## Trademark Policy

The trie-trunk and Snoot names and project logos identify the project and its
official releases maintained by AuthenticityLabs. The software licenses grant
copyright and other expressly stated rights; they do not grant general rights
to use these marks as your own product identity or to imply endorsement.

You may use the names to describe the project accurately, credit its origin,
document compatibility, discuss or review it, or identify an unmodified official
release. For example, "Acme Tools for trie-trunk" and "Compatible with PIL"
are acceptable factual descriptions when they do not imply official sponsorship.
Use of a protocol's name to identify compatibility is not a claim of endorsement.

Independently maintained forks, modified distributions, extensions, wrappers and
hosted products should use their own primary product names and branding. They
may accurately state that they are based on, integrate with, or are compatible
with trie-trunk or Snoot. Clearly distinguish your product and maintainer from
the official project. Do not use the project logos as your own product logo or
describe an independent product as an official release without permission.

Keep required copyright, license and attribution notices when changing branding.
This policy does not prohibit forks, competing implementations, commercial use,
or redistribution allowed by the software licenses. It does not limit fair use,
nominative use, or other rights granted by applicable law, and does not claim
that a mark is registered.

For branding permission beyond the uses above, contact the project maintainers
through https://github.com/AuthenticityLabs/trie-trunk.
