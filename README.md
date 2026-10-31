# Trie-Trunk

An open-source time-series database for typed events, temporal search, and
precomputed analytics. Optional collection indexing supports object revision replay.
Ingest typed events once; reuse complete hourly datasets and compact summaries in
the browser instead of repeating every investigation against the server.

[Website](https://trietrunk.com) · [Snoot](https://snoot.trietrunk.com) ·
[PIL Bootstrap Definition (.rx)](https://github.com/AuthenticityLabs/pill-packer/blob/main/schemas/pil.rx)

Version zero: storage designs and fixtures can change without migration paths.
The sample datasets are disposable.

## Pack and Replication Design

[Pill Packer](../pill-packer/README.md#pack-api) owns PIL/PAC framing,
named headers, optional embedded member directories, fixed-width validation,
and explicit compression. Trie-Trunk owns grouping, identity, storage placement,
replication, and retention. Ordinary event members remain variable-width.

The development branch index uses six-byte rows: four timestamp bytes and a
two-byte state leaf. Record position supplies the ordinal; payload boundaries
come from the PAC directory. The former 16-byte index and the entropy-preserving
experiment are historical baselines, not current layout descriptions.

Current Search order: `/trunk` bootstrap, scoped `.tri` ranges and immediate
counts, then collection of missing whole hourly `.pods` packs into `.sods`.
Local matching begins as each collected window becomes available. Fast dashboards
replicate summary PACs separately. Tantivy remains an explicit server comparison;
production client Tantivy replication and execution are deferred.

The proposed leaf-to-hour occurrence index is **not implemented**. Current local
search bounds by time and checks state leaves while scanning the selected rows.
Its reader currently brings payload bytes into memory before leaf rejection.
A directly routed leaf/hour directory with paged occurrence lists remains design
work; no lookup complexity or performance improvement is claimed for it yet.

## Search Performance Verification

The [historical comparison](benchmarks/client/2026-10-30-framework/results.json)
rebuilds the preserved pre-redesign implementation and its original Pill Packer
sources against identical 50,000-event inputs. Three isolated WebKit runs separate
server-assisted retrieval from whole-window client collection. Matching INFO and
error filters have identical result counts. Measurements end at data-ready delivery,
not DOM paint; cold means an empty client replica, not cold server storage.

The collector retains bounded 4 MiB/4096-record transfers, coalesced reads and
publication after data writes. The planner preserves early-page delivery, then
streams the remaining range without splitting every calendar day. Repeated queries
reuse the existing bounded event cache after checking replica identity and coverage.
Tests protect request counts, bounded concurrency, reset and retention invalidation.

The query benchmark worker is `client/benchmarks/query-path.worker.ts`; the browser
driver is `client/scripts/benchmark-query-path.mjs`. The recorded fixture is generated
by each revision's `seed-demo`, with canonical ingress bytes hashed before encoding.
The report includes wire bytes, request counts, first-page latency, full-result latency,
source archive hashes and whole-server storage footprint. Broad cold client searches
still perform substantially more work than the historical selected-page path.

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
Blind CBOR and JSON inputs are decoded at ingestion. Definitions, event payloads, controls, metadata, and browser state use PIL.

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
| Pods — Packed Observability Datastore | `.pods` | Packed Data Storage — sealed server storage for seed packs; write packs remain uncompressed for object lookup |
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
  resource.tri
  <service>/<resource>.v<major>/
    collection.idx                 # optional object/revision lookup
    collection.state               # invalidation state
    <minor>/
      resource.rx
      state.tri
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

The separate Cloudflare Pages project is `trietrunk`. Its homepage contains
Trunk, Snoot, Seeds, and Bench sections. Bench has Performance, Storage, and
Transit tabs. Performance currently displays explicitly labelled sample values;
Storage and Transit use the measured million-event fixture.
`/PIL` holds the format, library examples, and Definitions notebook. The earlier
detail pages remain at `/hidden`. After committing website changes, publish with:

```sh
node site/publish.mjs
```

Publishing rebuilds all routes together from the current source and refuses
uncommitted website changes. Do not deploy an older temporary artifact to update
one page. `/release.json` records the commit and homepage/PIL hashes so the
combined release can be verified. `node site/build.mjs` remains available for
local builds; `BENCH_PREVIEW=1` writes a noindex local preview to
`site/artifacts/bench-preview`, separate from the production output.

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

Definition-declared object IDs have a distinct link color. Clicking one opens
Object History and performs a fresh `/<cache>/collections?branch=<service/resource/write>&id=<id>` lookup in its resource.
Debug and trace leaf buttons sit in the event margin and open separate data
dialogs. They use `correlation_id` or `trace_id` when supplied, with an exact
millisecond timestamp fallback within the same resource. Their records are
excluded from normal event counts, histograms, distributions, and operation
rankings. Diagnostic dialogs request pages without event summaries.

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
branch holds its sparse `events.sods`, `events.idx`, state trie/definition
files, and raw time trie. Committed time-trie leaves determine availability;
there is no second per-event IndexedDB index or persisted package-coverage list.
Allocation/decoding context is stored in `layout.<revision>.pil`. The atomic
`commit.pil` root publishes immutable layout and `branch.<revision>.tri`
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

Client completeness concerns the files being synchronized. A sods completion bit
is published after its data is committed, while other replicated files can become
available independently. Default Search collects whole hourly event packs before
evaluating predicates locally. Client file-completion booleans belong in a compact mask;
the server does not maintain a client receipt mask.

Server time tries contain sealed hourly address/count leaves, without a status
tail. Sparse client copies append one file-completion byte: summary `1`, index
`2`, state `4`, inverted `8`, and sods `16`. Zero means no associated files are
complete. Sparse allocation ranges identify received time leaves, including
known empty hours, independently of those file bits. Importing time alone does
not certify any other file. A time refresh preserves committed file bits.

`GET /trunk` bootstraps all registered resources, signature tries, and `.rx`
definitions in one uncompressed PIL stream. The resource hierarchy is
`resource.tri` at the cache root; each definition folder holds its `state.tri`
signatures and `resource.rx`. The catalog advertises actual relative artifact
paths. `GET /trunk/<relative-folder>/` traverses registered folders without
assuming a fixed hierarchy depth. The client validates artifacts and commits its
bootstrap snapshot only after every file is durable.

Cache-scoped resource endpoints use branch selectors and epoch-millisecond bounds:

| Endpoint | Data |
| --- | --- |
| `/<cache>/registries` | Definition registration and negotiation; `kind=bindings` lists selected bindings |
| `/<cache>/collections` | Object history; `kind=state` checks subscription state |
| `/<cache>/seeds` | Whole sealed packs and the available unsealed interval |
| `/<cache>/summaries` | Daily summary ranges or a batch of summary references |
| `/<cache>/timelines` | Published time-trie ranges and hourly counts |
| `/<cache>/indexes` | Branch index ranges; `mode=search` enables the temporary Tantivy comparison |

`branch=payment/transactions.v1/write` selects a branch. `definition=<digest>`
disambiguates physical resource definitions. `beginTime` is inclusive and
`endTime` is exclusive, both epoch milliseconds. Omit `endTime` on seeds to
retrieve everything currently available from `beginTime`; this is a finite
response, not a continuous tail. Timelines align to whole hours and summaries
to UTC days. Catalog artifact names remain storage identities, not HTTP paths.
`/trunk` remains bootstrap and is independent of query execution.

Bootstrap and time-window replication are connected to Search. Selecting a scope
or applying a window reads available local time leaves and fetches missing ranges
through cache-scoped `/timelines` requests, with at most four concurrent requests.
The histogram shows sealed hourly counts before Search is invoked; debug/trace
branches do not contribute to ordinary event counts. Long ranges combine hourly
counts into at most 300 rendered bars. Time imports preserve existing completion
bits and publish no event/index completeness. Changing the calendar window no
longer submits an event search automatically.

### Client Search Execution

Search uses catalog identities and hourly completeness bits to collect only missing
whole `.pods` packs. It does not call `/events` or independently fetch `.idx`
ranges in the default path. The first nonempty hour is scheduled separately for
prompt first-page results; remaining work is grouped by day with at most four
concurrent branch tasks. Each window is matched locally after collection.
The default Search path never asks the server to evaluate predicates.

State leaves reject impossible matches before decoding event values. Compiled
predicates support AND, OR, XOR, NOT, and parentheses; precedence is NOT, AND,
XOR, OR. An OR match is counted once even when both conditions are true.
Text matching currently searches Definition enum values; text absent from those
Definitions is ignored and reported. Free-form event text and production client
Tantivy search remain deferred. The Tantivy Comparison option invokes the server
explicitly and rejects Boolean expressions it cannot represent.

Native paging retains the two binary boundary searches. Independent aligned
read windows serve index probes, sequential rows, PAC directories, and payloads.
Small first-page reads grow to bounded 4,096-record scans, subject to the existing
byte cap. Complete query aggregates are cached for at most eight exact snapshots;
later pages seek to their cursor timestamp, resolve ties by the full source key,
and stop after the requested page. A changed query, window, generation, or local
rebase invalidates reuse. A fresh worker can rebuild aggregates from durable data
without recollecting complete hours. Local latency rankings report their actual
average, rather than labeling that mean as P95.

Summary PACs are stored in OPFS `branch.sum` for planted service dashboards.
Their bits publish only after durable storage and agreement with received hourly
counts. Search event charts aggregate the locally matched events; Fast dashboards
consume precomputed summary columns. The performance flyover separates planning,
collection, and client work.

`go test ./server/cache -run TestSearchPlannerWorkerAgainstHTTP -v` exercises the
production worker and WASM codec against an isolated Go server: cold/warm search,
restart, paging, ignored text, truncated whole-pack delivery, and failed summary
publication followed by retry. It asserts that default Search never requests
`/events` or an independent `.idx`. Client tests also cover Boolean combinations,
same-timestamp cross-branch paging, cache invalidation, and cancellation. The
Node process is a test client, not an application server. Browser verification
covers the actual Search page and real storage-worker/OPFS I/O separately.

### Client Search Measurements

A development stress test on 1,048,576 synthetic events in 256 hourly packs used Chromium,
production WASM, storage-worker messaging, and durable OPFS. Identity and storage
formats were identical. Both runs returned the exact count and disjoint pages.
The before build was reconstructed from development sources saved before the
optimization pass, not from the deployed release. The unfiltered query requested
100 events while aggregating all million records; the first page was ready long
before the full request finished.

| Measurement | Before | After |
| --- | ---: | ---: |
| Full local scan and statistics | 132.83 s | 5.36 s |
| First 100-event page available | 64.5 ms | 49.0 ms |
| Next 100-event page ready for UI | 62.3 ms | 42.7 ms |
| Subsequent request complete, including statistics | 132.08 s | 42.7 ms |
| Storage-worker page reads per full scan | 8,192 | 268 |

These are sequential single-run browser measurements, not production latency
claims or a deployed-versus-new comparison. Next-page completion improves mainly
by reusing statistics instead of repeating the full scan. Fixture transport was
cached; real network transfer and DOM rendering
are excluded. The full scan remains linear in the number of collected candidates;
this change does not turn event analytics into summary-column performance.
[Raw Chromium results](benchmarks/client/2026-10-29-planner/chromium.json).

A separate three-run memory-backed I/O harness measured median fresh full scans
at 19.26 s before and 1.91 s after. File read requests fell from 96.78 GB to
112.08 MB per scan; those counters measure requested bytes, not physical disk
traffic. [Before](benchmarks/client/2026-10-29-planner/memory-before.json) ·
[After](benchmarks/client/2026-10-29-planner/memory-after.json).

Reproduce the memory harness with
`node --experimental-strip-types client/scripts/benchmark-planner.mjs <checkout> <fixture.pil> <report.json>`.
The fixture requires its adjacent `.pil.json` metadata. Build the browser worker
with `node client/scripts/build-planner-benchmark.mjs`; serve its output, WASM,
and `/fixture/dense.pil` plus metadata through the Go test server, then start
`worker.js` as a module worker and post `{variant: "after"}`. It reports timings,
checks counts/page overlap, and removes its own temporary OPFS namespace.

Snoot's Profile page has Preferences and Connections tabs. Preferences include
name, Light/Dark/System appearance, Trunk/Snoot theme, and events per page.
Connection registrations are local profile data until identity-server integration.
Each enabled registration receives its own `/trunk` request at startup. The
selected server supplies the current workspace controls. Bearer tokens remain
separate per registration in tab memory; they are not saved with the profile.
Worker sessions and durable caches are isolated by server and credential.
Remote servers must permit browser requests from the app's origin.

Only complete collected hours enter durable event coverage; partial query results
remain in memory. A received time leaf establishes count/address availability;
its sods bit establishes committed event-data availability. Other bits certify validated, committed
contents for the matching source and generation. Sparse allocation mappings and
record counts serve address calculations, rather than file-completion tracking.

Storage names are `.pods` on the server and `.sods` on the client. Protocol
`MemberRanges` denotes decoded PAC member coordinates. The `replication.data`
policy and `data=none` collection option control data fetching. Recreate
version-zero caches after the serialized-name and file-identifier cleanup.

### Tantivy Reader Experiment

The read-only WASM reader in `rust/search-wasm` opens immutable Tantivy 0.25
files through synchronous worker file handles. It shares schema construction,
document indexing, text modes, and typed predicate execution with the native
server through `rust/search`. PIL/PAC encoding and compression remain in
pill-packer. The reader has a bounded 8 MiB cache of 64 KiB index blocks;
posting/column reads reuse those blocks instead of making one OPFS call per
small value. Original event payloads are not stored in Tantivy.

Time bounds come from the branch time trie and binary searches of its indexed millisecond
index. Tantivy evaluates text and indexed predicates, then its candidate
collector clips canonical event addresses to those exact snapshot ranges. It
does not enumerate address terms to construct a time-range bitset. Each accepted
address is resolved through the captured time trie before record retrieval.
Tantivy document IDs remain segment-local; they are not event addresses. Pure
time-window searches bypass Tantivy. Broad text matches still visit matching
postings outside the requested window; time-partitioned index storage is deferred.

This Tantivy reader is tested separately and is not invoked by Snoot Search.
Default matching runs in the client planner described above; only explicit
Tantivy comparison uses server search. Whole `.pods` transfers are decoded into
`.sods`, and file-completion bits publish after durable commit.

Replication uses these boundaries; the inverted-index stage remains deferred:

- State/filter parameters remain part of the request contract. Compressed PAC
  collection transfers whole requested hourly packs without scanning state
  references to decide whether an hour is worth transferring. A collection
  response does not claim that its records satisfy those predicates.
- Uncompressed `write.pods` still transfers whole packs. A future heavy-server
  mode may evaluate full searches over uncompressed data; it is not implemented
  by this replication policy.
- Inverted-index replication follows Tantivy's native segment/file layout, not
  forced hourly indexes. Transfer metadata and referenced files from one pinned
  committed snapshot. Existing immutable files may be reused; merges produce
  replacement segments. Keep the prior snapshot usable until the replacement
  is complete. The time trie records coverage of that snapshot; an hour's
  inverted bit requires all index files needed to search that hour, not merely
  one received segment. A snapshot may cover multiple hours.

Production `.tvy` snapshot pinning, publication, and planner integration remain
unimplemented. The default planner uses `.pods`; Fast dashboards use `.sum`.
Independent `.idx` replication exists but is not a prerequisite of default Search.
Source/Definition/generation binding, cancellation,
retention, and credential isolation apply to each artifact. Index and summary
bytes stay in OPFS, outside IndexedDB and session storage.

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
individual subscriptions. The `snoot-profile` IndexedDB database contains the
`preferences` store: theme, user-authored highlighting rules, and service
subscription choices. Selected service, drawer state, page, query, time window,
and other tab settings live in session storage under readable `snoot:` keys.
Search results, event bytes, definitions, and trie fragments are never copied
into session storage. A reload reruns the query using the local file cache.

The `snoot-searches` IndexedDB database holds bounded query metadata in
`SearchesLRU`: up to 20 predicate signatures, expiring two hours after their last
successful evaluation. Reads update recency without extending expiry. Signatures
exclude window, ordering, taxonomy selection, and connection identity; a separate
hashed context guards checkpoint reuse across sources, scopes, and Definitions.
Each entry can hold one newest-common timestamp (the minimum of each scanned
branch's newest timestamp), run counts, and measurements. Branch timestamps are
transient, and no event bytes or credentials are stored here. Checkpoints are
recorded only after full scans, before predicate rejection, with a one-millisecond
overlap for resumption. Searches ending at now use the ordinary `/<cache>/seeds` range:
its retrieval start advances to the checkpoint minus that overlap, while the
query's original window stays unchanged. The server captures `now` once. It sends
sealed PACs through the existing native path and unsealed records as uncompressed,
Definition-bound PACs with shared values, member addressing, and Seed time/index
headers. Predicates remain client-side.

Unsealed records append to a bounded query cache in worker memory (20 queries,
two hours, 64 MB combined); IndexedDB contains metadata only. Overlapping records
are deduplicated by provisional time/record identity. Newly sealed hours are
committed to local SODS before their ephemeral copies are released, including
hours sealed behind the incremental start. Reload or eviction retrieves the full
window again rather than using a checkpoint without its cached records. An empty
branch contributes the evaluated upper bound to the common checkpoint.

A collection respects its requested lower bound; backdated ingress earlier than
that bound requires a wider retrieval window. The live response is bounded to
64 MB of source PIL records across selected branches and fails explicitly above
that limit. Historical packs retain batched native retrieval. Refreshing still
re-evaluates local predicates and aggregates; incremental transfer does not imply
incremental aggregate computation.
[Isolated WebKit measurements](benchmarks/retrieval/2026-10-30-window-collection/results.json) cover first retrieval, overlap-only refresh, and one new event.
Profile → Storage → Recent Searches clears individual entries or the whole cache.

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
all displayed metrics and groups refer to the summarized data set. It does not
invent per-resource metrics for uncovered events or scan those events to fill
missing summaries. Tables export the same displayed data set. Summary dimension
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

Build on macOS with Go, Python 3.12+, CMake, Xcode command-line tools, and the existing
frontend build dependencies installed:

```sh
make desktop
# Repackage an already built frontend:
python3 desktop/build.py --skip-web
```

The default build produces `desktop/dist/Snoot.app` and
`desktop/dist/Snoot-macOS-universal.zip` for Apple Silicon and Intel Macs running
macOS 14 or later. The app uses the front-facing Snoot icon. Neutralino 6.10.0 is
pinned and its source checksum is verified. The build applies
`desktop/neutralino-macos-exit.patch` to route Close, Quit, and termination signals
through Cocoa's main queue, save window state, and let Cocoa finish shutdown.
Compiled runtimes are cached by architecture and patch hash; temporary compiler
outputs are discarded. Local builds are ad-hoc signed;
public distribution still needs Developer ID signing and notarization.

For an Intel-only package targeting Ventura 13.7 (including 13.7.8), build into
a separate directory so the installed universal app is preserved:

```sh
python3 desktop/build.py --skip-web --arch x64 --min-macos 13.7 \
  --output-dir desktop/dist/intel-ventura
```

The runtime deployment target and app minimum OS version both follow
`--min-macos`. Intel Go builds use the baseline AMD64 instruction set. Ventura
builds also apply `desktop/neutralino-macos-ventura.patch` to exclude the
macOS 14-only screenshot API from their native binary. The native JavaScript
API remains disabled in Snoot. Ventura uses its installed WebKit; keep Safari
updated. Cross-compilation and binary
checks do not replace testing on an Intel Mac running Ventura.

The native lifecycle regression test launches isolated windows and exercises the
close button, Close Window, Quit, Command-W, Command-Q, repeated close requests,
and termination signals. It verifies saved window state and bridge cleanup:

```sh
python3 desktop/tests/test_lifecycle.py desktop/dist/Snoot.app
```

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

Events remain in OPFS, profile preferences and the bounded SearchesLRU metadata
in IndexedDB, and page/search state in session storage. Desktop storage is separate from Safari's website storage.
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

Sealed daily summaries in the `.sum` stream are standard PIL PACs, with one hourly
aggregate record per member and a checked-in `go/server/cache/summary.rx`
Definition. Each member carries its compact five-minute column payload as BYTES;
open hourly summaries remain individual `SUMCOL` records until daily sealing.
Pill Packer owns PAC framing and gzip while Trie-Trunk owns aggregation and the
compact summary-column codec. `.pods` members remain
variable length even when the Definition's scalar types have fixed widths:
individual events may omit fields. The six-byte branch-index row contains the low
four bytes of epoch milliseconds followed by a two-byte state trie leaf. The hour
header carries the high timestamp word. The final zero-based record ordinal is
implicit in index position and is stored once as a fixed U24 `@event_id` in the
sealed member. No per-record payload offset or UUID entropy is stored in this index.

The definition-engine registration plan is JSON folders/upload/editor → portable
structure/codec checks → Trie-Trunk profile checks → immutable PIL `.rx` → publication.
Persist definitions, including structured CDDL schemas, as PIL `.rx` documents.
The compiler/runtime and application-profile boundaries are described below.
The fixed `pil/value` bootstrap layout is built into the codec and published as
[`pil.rx`](https://github.com/AuthenticityLabs/pill-packer/blob/main/schemas/pil.rx);
that definition is encoded using the same layout it describes.
Keep source text separately for editor fidelity. Unknown metadata must survive
forwarding; required validation must be explicitly supported, not inferred from
an annotation. Folder ingestion and editor/upload UI are pending.

PIL carries complete objects or partial changes. Definitions specify ordered
fields, primitive types, dictionaries, and integer-encoding options. Definitions
are PIL `.rx` documents; [`definition.cddl`](https://github.com/AuthenticityLabs/pill-packer/blob/main/schemas/definition.cddl) is the editable CDDL description of their structure.
`resource.rx` and `producer-<digest>.rx` store native PIL definitions; internal
metadata such as `registry.map` retains its existing encoding.
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

### Definition Validation Ownership

Pill Packer compiles supported CDDL into a PIL root layout and nested schema plan.
Rust, Go, and WASM share that nested runtime and reject a schema that disagrees
with its compiled layout. Trie-Trunk separately validates resource identity,
version, timezone, role/type bindings, aggregation, and shared projection paths
in `go/server/cache/definition.go` and `references.go`.

Compiler, CLI, bootstrap, and binding parity tests live in Pill Packer's
`rust/tests/schema.rs`, `rust/tests/rx.rs`, `rust/tests/documents.rs`,
`go/codec/schema_test.go`, and npm tests. Cache profile checks stay in Trie-Trunk's
cache tests; demo-generation tests stay with `go/cmd/seed-demo`.

Demo Definitions are still assembled as Go structs. Loading authored `.rx`
resources requires an explicit portable binding for CDDL constraints to storage
roles: for example, `bstr .size 16` compiles as BYTES while the cache identity role
requires UUID. Neither validator should be weakened to disguise that gap.
Generic/recursive rules, group inclusion, dynamic map keys, tuple arrays, and
unbounded `int`/`nint` remain outside the supported CDDL subset. Folder ingestion
and the Definition upload/editor UI remain pending.

## Logger

The Go PIL logger registers a Definition before its first write. It exchanges
identity and digest, sends missing maps, and validates a compatible server layout.
`WriteValues` encodes positional values into the accepted layout; `WriteFrames`
accepts frames under the producer's original Definition. Extensions can use
explicitly enabled `@fields` and be promoted by compatible newer Definitions.

```go
mapBytes, err := os.ReadFile("resource.rx")
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

### Ordinal Identity Format (Development)

Ingress takes the timestamp from the supplied UUIDv7 and discards its entropy.
`@event_id` is a provisional per-hour number while live; sealing sorts the hour and
assigns final zero-based U24 row numbers (0–16,777,215). These numbers are scoped by
resource, physical branch, and hour. They are not globally unique. The original
UUID cannot be reconstructed and UUID-based ingress retry deduplication is no
longer available. Collection object/revision uniqueness remains enforced.

`@leaf` remains a REF16 Definition field, but its stored value lives only in the
two-byte index postfix. State values covered by that leaf are also omitted from
the sealed member and resolved from the state trie. Materialized event views
expose those values and `@leaf` without creating a second persisted copy. The
original UUID field is hidden in ordinal event views; internal live/paging APIs
use a synthetic timestamp/ordinal key, not the producer UUID.

Collection index encoding is unchanged: event positions start at one, and zero
remains invalid/empty. The boundary converts a zero-based `@event_id` to the
existing one-based collection position. Hour values, page layout, independent
width widening, and retention addressing keep their established conventions.

[Ordinal benchmark results](benchmarks/storage/2026-10-29-format-comparison/ordinal-results.json)
use the same 1,048,576 input events and 256 hours as the original baseline.
The resource store includes summaries and excludes Tantivy and collection indexes.
The client replica covers the event/index replication path; summaries are not
part of that client measurement. All bytes below are decimal MB.

| Measurement | Original Baseline | Entropy Experiment | Ordinal Layout |
| --- | ---: | ---: | ---: |
| Total Server Store | 22.59 MB | 27.76 MB | 15.81 MB |
| Total Client Replica | 39.04 MB | 46.47 MB | 35.99 MB |
| Client Decode | 98.8 ms | — | 147.1 ms |
| Client Decode and Durable Replication | 420.5 ms | — | 579.6 ms |
| Local 100-Event Page | 21.4 ms | — | 27.7 ms |

The original and ordinal builds were measured sequentially in the same isolated
Chrome session: seven decode runs, five storage-worker/OPFS replication runs, and
five page reads per replication. Timings exclude network transfer and DOM/GPU
rendering. All 256 hours and 1,048,576 events passed full readback. Original UUID
hashes intentionally differ because this design discards entropy.

The footprint is smaller, but retrieval is still slower than the original
baseline. The [member audit](benchmarks/storage/2026-10-29-format-comparison/ordinal-audit.json)
finds 22-byte decoded members, including the fixed three-byte ordinal and no
payload copy of `@leaf` or trie-covered state. Four-byte PAC directory entries
remain, along with a three-byte mask versus the original two-byte mask. This
fixture has uniform member widths; directory elimination remains an optimization
target. No speedup in total retrieval is claimed.

### Entropy-Preserving Experiment (Superseded)

[Reproducible results and artifact hashes](benchmarks/storage/2026-10-29-format-comparison/results.json)
compare Trie-Trunk `42ebf75` and Pill Packer `e663e89` with the earlier entropy-preserving
experiment. Both use the same 1,048,576 deterministic events in 256 hours, including
full 74-bit UUID entropy. Summaries are enabled; Tantivy and the optional
collection index are disabled in both. These measurements cover the full isolated
resource store, not just event payloads.

| Measurement | Before | Development |
| --- | ---: | ---: |
| Event index | 16.78 MB | 6.29 MB |
| Total server store, logical bytes | 22.59 MB | 27.76 MB |
| Total client replica, logical bytes | 39.04 MB | 46.47 MB |
| Time lower-bound lookup, 1 million rows | 176 ns | 146 ns |
| Time lower-bound lookup, 16 million rows | 481 ns | 365 ns |
| Client decode | 95 ms | 222 ms |
| Client decode and durable replication | 416 ms | 701 ms |
| Local 100-event page with field materialization | 21.5 ms | 27.2 ms |

Client medians use five alternating Chrome runs through the actual storage
worker and OPFS. Transfer bytes are already in memory when timing starts;
network download and DOM/GPU painting are excluded. All million records are
read back, and reconstructed UUID hashes agree. Go lookup runs use five samples
per format and zero allocations per lookup.

In that experiment, the index was smaller and faster, but the entire change was
not a performance improvement. Relocated entropy passed through gzip; that PAC layout
also introduces directory entries, variable-field offsets, and a wider field
state mask. Controlled same-byte decompression is approximately 144 ms with the
old Pill Packer and 142 ms with the new one, so these results do not show a slower
decompression codec. The report includes entropy/directory isolation measurements.
A [full member audit](benchmarks/storage/2026-10-29-format-comparison/after-audit.json)
accounts for the replica growth: 10 fewer index bytes, but 13 more member bytes
and four directory bytes per event. All measured members are exactly 32 bytes,
so this fixture does not need a directory. The state field is also still present
in the payload despite its leaf being indexed. Original UUID and `@time` values
are absent from sealed members; entropy is stored once. This is an implementation
overhead finding, not evidence of duplicated complete event copies.

An idle native-buffer suffix saturation bug found during restart benchmarking
has been fixed and covered by a bounded-memory regression test.

To regenerate the development fixture in an isolated temporary store:
`TRUNK_FORMAT_BENCH_OUTPUT=/tmp/trunk-format/dense.pil go -C go test ./server/cache -run '^TestFormatComparisonFixture$' -count=1 -v`.
Run index measurements with `go -C go test ./server/cache -run '^$' -bench '^BenchmarkFormatIndex$' -benchmem -benchtime=500ms -count=5`.


**The full Trie-Trunk datastore is 70.9% smaller than expanded JSONL** in the
million-event synthetic fixture, including indexes, summaries, journals, and
metadata. This historical measurement used compressed writes with Collections
disabled; it predates the current uncompressed `write.pods` policy.

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
The scopes and matching data sets differ; this comparison locates work rather
than claiming an equivalent-workload speedup.

Measurements include HTTP gzip decoding, routing metadata parsing, and summary
SHA-256 verification. They exclude seed-pack application decoding, SharedWorker
messaging, OPFS writes, chart calculation, and painting. No native coverage
receipts are sent between seed-pack downloads, so overlapping dependency bytes
may be transferred again. The separate `KnownMetadataMS` metric is one request
with cache receipts, not a complete warm-view time. No Node runtime is used in
this benchmark. No competing platform has been measured on this workload yet.

The browser now delivers the validated event page before loading chart summaries
from the same snapshot. Missing summary packs are selected through `POST /<cache>/summaries`
with `{ "packs": [{ "source": "…", "definition": "…", "key": "…" }] }`, using identities from the
search manifest. The response is a streamed PIL record containing `ARRAY<BYTES>`: each
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

Dashboard statistic values fit their available card width, including space for
trend indicators. Full numbers stay on one line; font sizing is recalculated only
when the value, available width, or loaded font changes.
