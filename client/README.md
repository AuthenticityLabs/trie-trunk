# snoot

snoot is the Solid web app for trie-trunk, the data store and server.
Its TypeScript source lives here. Vite builds it into `../web`, which the
Go server serves at `/` alongside the `/collect` API.

From the repository root:

```sh
make web
./trunk ingest 5410 serve 8443
```

Open `https://localhost:8443/`. Restart a previously running server after updating
the Go executable. Frontend-only builds are served from disk without a server
restart; reload the browser. `make build` builds the client, Rust search, and Go
executable together. Node 22.12+ is required for the client build.

The supplied wordmark appears in the content header from `assets/snoot-wordmark.png`.
The anteater portrait at `assets/snoot-portrait.png` heads the navigation drawer.
Click it to toggle light and dark mode: the portrait represents light mode and
`assets/snoot-forward.png` represents dark mode. The choice is stored under `theme`
in the existing `snoot` IndexedDB `preferences` store and restored before the app
mounts. A new workspace starts in light mode.
Images keep their original proportions. The existing `assets/snoot.png`
remains available for the favicon and small workspace marks. The data store,
server, and CLI retain their trie-trunk names.

## Collect and Search

**Collect** chooses which events to pull from the server: a tap, UTC time window,
level, service, resource and operation. Dropdown choices come from negotiated
dictionaries. Every selected field must match; an All selection adds no constraint.
The web UI has no text query, expression syntax or group editor. Advanced search
syntax remains a CLI feature.

Collect replaces the working set and retrieves every matching event, without an
initial event limit. The request sends `limit: null` and `tail: false`; selected
fields are encoded into the server's existing `where` format internally.
**Events collected** shows the deduplicated working-set count as records arrive.
Stop ends the pull while retaining events already collected. The default window
is today in UTC. Custom windows accept a start and optional end; an omitted end
means now for Collect and stays open for Follow live.

**Search**, below Collect, works only on the records already in this browser tab.
It has independent field selectors, tap and optional UTC time bounds. Choices come
from collected records, and selected fields match their dictionary labels rather
than numeric IDs. Time bounds use UUIDv7 milliseconds, inclusive start and exclusive
end. The Ark UI range calendar opens at the collected event span. Completed
collection intervals are shaded; partly collected days are striped. Other past
dates remain selectable. Empty but fully collected intervals are also shaded.
Two day selections choose a range and update the local results immediately,
without an Apply dates button. A single day can be selected twice. UTC time edits
also apply immediately. The displayed Through time is inclusive; the request's
exclusive end is one millisecond later. Today ends at now.

Selecting an uncollected day immediately asks **Expand collection set?** The prompt
shows the missing intervals and original collection criteria. Confirming fetches
only those intervals, appends events, and deduplicates by tap and UUID. Changes to
Collect's draft selectors do not change expansion criteria. Cancel restores the
previous date selection. A new collection resets local dates while retaining the
selected fields and tap. Follow updates the available event span.

Coverage is saved with the collection after verified completion of unlimited finite
pulls. The final 15 minutes at collection time remain untrusted for late arrivals. Failed, stopped and follow requests retain received events but do not claim
complete intervals. Expanding while a pull is running stops it before fetching
missing ranges. A retry may overlap incomplete data; deduplication prevents
repeated events. Existing local search criteria remain active during append.
**Events matched** shows the subset alongside the full collected count.

**Clear search** restores the original working set and summaries without a request.
Changing field selectors edits the choices; pressing Collect or Search applies them.
Calendar selection applies dates immediately; Collect still requires its own action.
Applied search selections remain visible while editing, and an invalid time window
leaves the current view intact. The table, timeline, rankings, breakdowns, Dashboard
and JSONL export reflect the matched subset. Pagination only controls mounted rows.

Service and level breakdowns offer explicit **Use in Collect** and **Use in Search**
actions. They fill the corresponding selector without running either action.
Indicators remain annotations and do not affect membership.

Follow live belongs to Collect. It merges arrivals in UUID order, deduplicates by
tap and UUID across schema versions, and applies the active local search to new
events. A new collection retains the applied field and tap selections while resetting
the local date window. Clear search also cancels a search in progress. Rescans yield between
chunks; late arrivals are included without changing records or expanding JSON.

Event pointers and collection criteria persist in IndexedDB; trie and population bytes remain in OPFS. Reloading restores this tab's last
collection; a new tab opens the most recently created collection for its credentials.
Local search selections are transient. Preferences, indicators and notebook references
retain their existing IndexedDB storage. Event data does not go into IndexedDB.

Events render as field/value tags. Selecting an event reconstructs its JSON on
demand, in presentation order: configured headers, then `id`, derived `time`,
root-to-leaf lineage and population tail. This is a derived representation, not
the original input text. Export reconstructs JSONL for all matched events, not
only the visible page. Alerts remains a placeholder page.

Connection settings accept an optional bearer token, kept only in the current
tab's memory. Credentials are never persisted; event-file namespaces use a SHA-256
hash of the credential. Browser-origin storage already separates server origins. The app
uses the same-origin `/collect` endpoint. If the server requires mTLS, the browser
must present a trusted client certificate. If both mTLS and a token are configured,
both are required.

Snoot uses `/schemas` to populate Collect choices independently. Each collection
uses one `GET /collect` with URL selection parameters and no request body; it does
not require or wait for the choices lookup. Cached tap schemas and PIL definitions survive reloads;
unchanged definitions are advertised in `Definition-Known` (up to 128 cached
digests). `PIL-Version` and `Definition-Version`
select the exchange. Changed definitions precede their events in the same response.
The worker retains up to eight negotiated tap scopes, isolated by credentials.

The binary reader supports arbitrary chunk boundaries, cancellation, malformed
frames and truncated responses. It requires the terminal PIL completion marker;
partial data remains readable but does not establish complete collection coverage.
See [the transport binding](../rust/PIL-TRANSPORT.md#browser-collection-binding-version-2).

## Development

```sh
cd client
npm ci
npm run dev
npm test
npm run build
```

`npm run check` checks types. `npm run build` checks types and creates a production
bundle; `npm run preview` serves that bundle locally. The development and preview
servers only serve the app; no event API proxy is configured. Use the Go server
for collection. No sample events or fabricated metrics are shown when no data is
loaded.

`components` contains rendering components; `services/events.ts` contains the
stream reader. `services/dictionary.ts` holds shared dictionary bindings, field
layouts and on-demand projections. Records retain PIL frames and dictionary
bindings; fields are decoded only as consumers need them.
Named event objects are created only for inspection or explicit export. Visible
field tags resolve labels directly from their dictionary.

`services/working-set.ts` retains the collected set and the current search view.
Search views hold references to the same compact events and their own summary
accumulators. `services/search.ts` binds selector values to local predicates; it performs
no network or database operations.

`services/results.ts` merges batches and incrementally updates service, level,
duration and indicator counts. It extracts the first six UUIDv7 bytes once per
event as an exact numeric timestamp. Timeline bucket counts use binary searches
over sorted timestamps rather than parsing formatted dates or rescanning every
event on each update. Enum indicator expressions evaluate dictionary labels once
per binding, then match numeric references. Changing indicator rules reevaluates
loaded events; appending events evaluates only the new records.

`app.tsx` holds the workspace and collection/search lifecycle. The UI uses Solid
with native browser APIs and Ark UI's Solid date picker. The calendar code loads
only when opened; its styles match the workspace. Only the current table page
mounts field tags and formatted values; loading more events does not mount more
table rows.

`web` is generated output and is cleared on each build. Keep source and assets
under `client`. Deploy `web` alongside `trunk` and `trunk-search`. The Go server
defaults to `./web`; `TRUNK_WEB` or `serve --web` selects another directory. Paths
resolve from the server's working directory.

## Timeline signatures and sample data

Search and Dashboard show the activity timeline for loaded results. Red segments
represent ERROR, FATAL and CRITICAL events; tooltips include each bucket's event
count and error rate. The axis includes UTC dates for multi-day searches.

Each event row renders its fields as compact field/value tags. The event header
shows `resource`, `operation` and `id` as plain-text columns alongside time, level
and service. They have no indicator buttons and are not repeated in the field
list. This only changes the
table presentation, not event storage or JSON inspection order.
Nested objects and arrays retain their field paths. Select a tag to **Add indicator**:
enter a label and choose **Text contains**, **String equals**, **Regular expression**, **Less than or
equal to (≤)** or **Greater than or equal to (≥)**. Text and regex matching optionally
support **Ignore case**. String equals compares the entire string (including whitespace),
allows an empty string, and does not coerce numbers or booleans. Text matching is literal; regular expressions use JavaScript
syntax without surrounding slashes. Numeric comparisons include the threshold and
match finite numeric values only, without coercing strings, nulls or dictionary IDs.
Duration thresholds use microseconds. Invalid expressions cannot be saved.

Fields in the assigned schema's `path` are trie fields: their tags are read-only
and have no indicator button. Indicator evaluation is reserved for dynamic scalar
and text values, including leaves inside population objects. Arrays, objects and
nulls are not evaluated as text. Previously saved rules targeting trie fields are
retained but do not run for those schemas, including records using literal fallback.

Rules live in this browser's IndexedDB database `snoot`, in the `indicators` store.
They persist across reloads and apply to loaded events across searches and taps.
There are no built-in classifications or required event fields. `services/indicators.ts`
compiles rules when they change and attaches matching labels to a separate view map.
It does not add fields to events, change JSON exports, or send rules to the server.

Matching events show their labels in the event row and compact timeline bubbles.
Matches within a histogram bucket share one bubble; an event matching multiple
rules counts once in that bubble. Hover or focus previews the rules and events;
clicking pins the overlay. **Delete** beside a rule removes it from IndexedDB and
the current view. Escape, the close button, or a click outside dismisses the overlay.
Events in the overlay link to their rows in Search. Following a link switches to
the right result page, scrolls to the row, and highlights and focuses it. This also
works from Dashboard, without another event pull.
The overlay does not add height to the chart. Rule changes update the current view
without another event pull or schema negotiation.

**Events per page** controls the number of loaded events displayed at once: 25, 50,
100 (default), 250 or 500. It does not limit collection or local search. The setting
persists in the `preferences` object store of the `snoot` IndexedDB database, under
the key `events_per_page`. Database version 3 preserves existing indicators and
preferences while adding notebooks. Changing the setting does not repeat collection.

The initial search screen's **Explore sample week** selects `demo-week`, the last
seven days, with no event limit. It reads real ingested synthetic data through the
normal schema negotiation and event pull. It never inserts fabricated events into
an empty result set. See [sample generation](../go/server/ingest/sample/README.md)
for the reproducible CBOR generator and upload commands. Counts and timelines
remain limited to loaded results, not store-wide aggregates.

## Notebooks

Each event header has a **Save to notebook** button. Its menu lists notebooks by
most recent save, marks those already containing the event, and offers **+** to
create a notebook and save the event in one operation. Names are entered inline.
Escape or a click outside closes the menu. A failed save leaves it open for retry.

IndexedDB stores notebook names and usage times in `notebooks`, and references in
`notebook_events`. A reference contains the notebook ID, tap, event UUID, schema
version/digest and save time. Its key is `(notebook, tap, event UUID)`, so repeated
saves do not duplicate evidence and the same UUID in different taps stays distinct.
Notebook creation and its first reference commit together. No event payload,
expanded JSON or query is copied into a notebook.

Notebooks and references survive browser reloads. A notebook workspace and saved
queries are future work; this change provides the picker and reference storage.
References do not retain an event payload if the source event is later deleted.

## Operation rankings

The left navigation margin shows **Top Errors** and **Most Latent**, grouped by
the complete `(service, resource, operation)` tuple across the selected taps and
schema versions. Dictionary labels supply the group identity, so the same value
with different numeric IDs combines correctly. Missing, null or non-scalar group
fields display as `Unspecified`; an empty string remains a separate group.
The lists scroll within the sidebar and share its collapse and mobile drawer
controls. They do not take a column from the search results area.
Both lists remain hidden until events load. Each starts with ranks 1 and 2 visible;
**Show all** expands it independently to its top ten, and **Show less** collapses it.

Errors count `ERROR`, `FATAL` and `CRITICAL` events. Latency ranks groups by their
exact nearest-rank p95: the value at `ceil(0.95 × sample_count)` in ascending
duration order. It includes all severities, uses nonnegative finite numeric
durations in microseconds, and shows each group's measured sample count. With
fewer than 20 samples, p95 is the largest duration.

Accumulators update only for newly received, deduplicated events. Each group's
duration values are maintained in two heaps, allowing an exact p95 update without
sorting or scanning its previous events. This keeps one additional numeric value
per measured event. Rankings are cached between updates and reset for a new collection;
follow continues accumulating. Both panels describe the matched subset of collected events, not a store-wide
aggregate. Searching rebuilds the subset accumulators; clearing restores the
working-set accumulators.

## PIL and the SharedWorker

Install the Rust `wasm32-unknown-unknown` target once. `npm run dev`, `build`, and
`test` build the reader from `../pil/wasm` and the small file store from
`../rust/browser`; no wasm-pack installation is required.
The worker negotiates `application/pil`, `PIL-Version`, and `Definition-Version`.
It caches definitions across collections/tabs, validates PIL frames in Rust WASM,
and transfers their original buffers to the requesting tab. A changed catalog
arrives before event frames within the same collection response. See
[the transport binding](../rust/PIL-TRANSPORT.md#browser-collection-binding-version-2).

Rows retain PIL slices, their dictionary binding, and UUID/time metadata. They
have no decoded event value arrays. Local search and summaries read only the
fields they use. Visible field tags resolve their dictionary references and
individual CBOR values at presentation; full JSON is derived only on inspection
or export. Integers outside JavaScript's exact numeric range remain bigint;
derived JSON renders those exceptional values as decimal strings instead of
silently rounding them. Binary fields use base64 only in that derived JSON.

The worker does network/framing/validation; existing local search, histogram,
indicator and percentile accumulators remain in the tab. This is not a WASM
analytics database. Before delivery, batches pass directly to a dedicated file
worker over a transferred MessagePort. Rust stores numeric lineage and CBOR byte
slices in native format-2 files. The original PIL buffers then transfer to the tab.
Backpressure bounds the port queue, and an incomplete response does
not establish collection coverage. The network parser accepts arbitrary chunk
boundaries, including fragments inside prefixes and UTF-8 values.

`npm test` covers the actual compiled WASM, real MessagePorts with transferable
buffers, shared negotiation/cache isolation, version refresh, cancellation,
truncation, sparse dictionary codes and lazy row views. The Go integration tests
exercise the same collection exchange over HTTP/2 and HTTP/3.

## Local event files

`rust/browser` is a small Rust/WASM store kernel with synchronous host file I/O.
It receives a tap schema, PIL definition and PIL frames. A dedicated browser worker
owns OPFS handles; Web Locks serialize file transactions across tabs, including
fallback workers and different application bundle versions.

- `log.map`: original tap schema; `log.pil`: its cached PIL definition.
- `log.tri`: shared numeric lineage, native format 2.
- UTC day directories: CBOR population in `log.pop`; no browser `log.idx`.
- A credential-scoped IndexedDB primary key `[tap, binary UUIDv7]` holds catalog,
  trie leaf and population pointers. Overlapping collects deduplicate across schemas.
- IndexedDB also stores file commit boundaries, collection membership and completed
  predicate-specific time ranges. There are no new collection journal files.

Writes flush trie and population data before atomically publishing pointers, file
ends and collection references in IndexedDB. Incomplete final appends are truncated on reopen; committed checksum
failures surface as errors. Restoring reads population blocks in batches and assembles
PIL frames in WASM. No JSON event reconstruction or network event pull is involved.
See [the local store design](../rust/browser/README.md).

The browser is asked to protect storage from automatic eviction. It may decline;
user-cleared site data and private browsing still limit retention. Storage/quota
failures are reported and are never presented as successful persisted collections.
Finite, unlimited collects first read local indexed events, then fetch only missing
completed ranges for the same tap/predicates. The most recent 15 minutes are always
eligible for refresh. Follow and limited requests use the server directly.
Existing v1 files are imported into v2; the old directory is retained as a migration
backup. This prototype has no retention/compaction policy. Static app assets still
require the server on page load.
