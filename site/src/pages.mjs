import {summaries, retention} from './storage-visuals.mjs';
import {icon} from './diagrams.mjs';
import {taxonomy} from './taxonomy.mjs';
import {storage,mb,kb} from './measurements.mjs';
import {seedPages} from './seeds.mjs';
import {pagePath} from './routes.mjs';
import {landing} from './landing.mjs';
import {previousSnootPreview} from './archived-snoot-preview.mjs';
import {homepageCharts as archivedHomepageCharts} from './archived-homepage-charts.mjs';

export const github = 'https://github.com/AuthenticityLabs/trie-trunk';
const source = path => `${github}/blob/main/${path}`;
const arrow = icon('arrow');
const brand = `<svg class="brand-tree" viewBox="225 70 810 930" aria-hidden="true"><image href="/assets/brand/trie-trunk-transparent.png" width="1254" height="1254"/></svg><span>trie-trunk</span>`;
const exampleRows = `
 <div class="record-row"><span>10:42:01.008</span><span>16</span><span>pending</span></div>
 <div class="record-row highlighted"><span>10:42:01.021</span><span>17</span><span>authorized</span></div>
 <div class="record-row"><span>10:42:02.104</span><span>18</span><span>captured</span></div>`;
const table = `<div class="record-table"><div class="record-labels"><span>Event Time</span><span>Revision</span><span>state</span></div>${exampleRows}</div>`;
const stamp = text => `<div class="scene-note">${icon('check')} ${text}</div>`;

export const stages = [
 {
  name:'Seeds', file:'.seed', location:'Server / Ingress', action:'Observability Ingress', acronym:'SEED · Sequentially Encoded Event Data',
  note:'*Data seeds are trie-trunk’s implementation of <a href="/PIL">PIL</a>. Each seed arrives under a negotiated Definition.',
  copy:'The seed journal supports durability until the hourly seal completes, and recent seeds stay in native memory for live tail.\n\nCollections feature supports live fanout revision subscriptions for app synchronization.',
  foot:'Rejected data seeds go to Lost Events with their original content.',
  scene:`<div class="scene-route"><span>payment / transactions</span><span>Data Seed</span></div>
   <div class="field-lines"><div><code>event_id</code><span>UUIDv7</span></div><div><code>id</code><span>Transaction UUID</span></div><div><code>revision_id</code><span>17</span></div><div class="highlighted"><code>state</code><span>authorized</span></div></div>
   <div class="scene-bottom"><span>Append to .seed</span><span>Available in Memory</span></div>`,
 },
 {
  name:'Packs', file:'.pack', location:'Server / Sealing', action:'Seal & Compress the Hour', acronym:'PACK · Packed Array Container',
  copy:'Seed packs are Trie-Trunk’s implementation of PIL PACs. Once the late-arrival window closes, data seeds from the hour are packed under a shared Definition. Common values can move into the prefix; compression is configurable by branch.',
  foot:'Sealing runs separately from incoming writes.',
  scene:`<div class="scene-route"><span>10:00 — 11:00 UTC</span><span>Hourly Seed Pack</span></div><div class="packet-prefix"><span>Shared Definition</span><code>payment/transactions</code></div>${table}${stamp('The illustrated change keeps its place in the hour.')}`,
 },
 {
  name:'Pods', file:'.pods', location:'Server / Storage', action:'Packed Data Storage', acronym:'PODS · Packed Observability Datastore',
  copy:'Pods hold sealed seed packs on the server. Research collects complete seed packs. Object history and replay use a separate lookup for individual data seeds.',
  foot:'All branches can reuse compressed seed packs for delivery. Enabling Collections keeps write packs uncompressed for direct replay.',
  scene:`<div class="scene-route"><span>Pods / Server Datastore</span><span>.pods</span></div><div class="pod-stack"><div><span>09:00</span><i></i><code>Seed Pack</code></div><div class="highlighted"><span>10:00</span><i></i><code>Seed Pack</code></div><div><span>11:00</span><i></i><code>Seed Pack</code></div></div><div class="scene-bottom"><span>Time & Text → Seed Pack</span><span>Object ID → Data Seed</span></div>`,
 },
 {
  name:'Sods', file:'.sods', location:'Client / Local Storage', action:'Sparse Replication', acronym:'SODS · Sparse Observability Datastore',
  copy:'Sods hold decoded data seeds in the browser. Collected seed packs populate complete hours; a missing hour remains unknown.',
  foot:'The SharedWorker coordinates access. Repeated research can reuse the same collected data seeds.',
  scene:`<div class="scene-route"><span>Client Working Set</span><span>.sods</span></div><div class="hour-grid">${[8,9,10,11,12,13].map(h=>`<div class="${h===10?'selected-hour':h===9||h===11?'known-hour':'missing-hour'}"><span>${h}:00</span><b>${h>=9&&h<=11?'Collected':'Missing'}</b></div>`).join('')}</div>${table}<div class="scene-note">A missing interval is different from an empty interval.</div>`,
 },
 {
  name:'Stalks', file:'In Memory', location:'Client / Analytics', action:'Analytic Engine', acronym:'STALKS · Sparse Telemetry Analytic Lineage Kolumns',
  copy:'Stalks use precomputed (and realtime) columnar datasets for event counts, errors, and duration statistics in the browser. Collected summaries can be reused across time views without loading every data seed.',
  foot:'Planned Lineage: definition-declared relationships connecting resources, such as payments and invoices, through identity and time.',
  scene:`<div class="scene-route"><span>5-Minute Columnar Summaries</span><span>In Memory</span></div><div class="column-demo">${[['Period','10:00','10:05','10:10','10:15'],['Count','84','107','92','116'],['Errors','2','0','1','3']].map(col=>`<div><strong>${col[0]}</strong>${col.slice(1).map(v=>`<span>${v}</span>`).join('')}</div>`).join('')}</div><div class="scene-note">Illustrative values · summaries travel independently of event detail.</div>`,
 }
];

const glyphs = [
 `<span class="diagram-glyph seeds-glyph">${[0,1,2,3].map(i=>`<i><b>${i===1?'17':'·'}</b><span></span></i>`).join('')}</span>`,
 `<span class="diagram-glyph pack-glyph"><b>Compress</b><i></i><i></i><i></i></span>`,
 `<span class="diagram-glyph pods-glyph"><i>09</i><i>10</i><i>11</i></span>`,
 `<span class="diagram-glyph sods-glyph">${Array.from({length:12},(_,i)=>`<i class="${[0,3,5,7,8].includes(i)?'gap':''}"></i>`).join('')}</span>`,
 `<span class="diagram-glyph stalks-glyph"><i></i><i></i><i></i><i></i></span>`
];
const lifecycle = `
<section id="lifecycle" class="lifecycle">
 <div class="section-heading"><h2>Lifecycle</h2><p>Ingest events once. Replay object changes, investigate collected data locally, and query compact summaries without loading every event.</p></div>
 <div class="system-map">
  <div class="system-boundaries"><span>Server</span><span>Client</span></div>
  <div class="lifecycle-track" role="tablist" aria-label="Data Lifecycle">
   ${stages.map((stage,i)=>`<button id="stage-${i}" role="tab" data-stage="${i}" aria-controls="stage-panel" aria-selected="${i===0}" tabindex="${i===0?0:-1}">${glyphs[i]}<strong>${stage.name}</strong><code>${stage.file}</code></button>`).join('')}
  </div>
  <div class="map-caption"><span>Select a Stage</span><span>Data Shapes Are Schematic</span></div>
 </div>
 <div id="stage-panel" role="tabpanel" aria-labelledby="stage-0" tabindex="0" class="stage-panel">
  <div class="stage-explanation"><span id="stage-place" class="mono-label">${stages[0].location}</span><h3 id="stage-title">${stages[0].action}</h3><p id="stage-acronym" class="stage-acronym" ${stages[0].acronym?'':'hidden'}>${stages[0].acronym}</p><p id="stage-note" class="stage-note">${stages[0].note}</p><p id="stage-copy">${stages[0].copy}</p><p id="stage-foot" class="aside">${stages[0].foot}</p></div>
  <div class="data-workbench"><div class="workbench-heading"><span>${icon('code')} Data Seed View</span><small>Example Data</small></div><div id="stage-scene">${stages[0].scene}</div></div>
 </div>
</section>`;


const questions = `
<section class="wrap questions" id="search">
 <div class="section-heading"><h2>Search</h2><p>Time, object identity, and text each have a useful way into the journal.</p></div>
 <div class="question-row"><div>${icon('search')}<h3>Time & Text</h3></div><p>Bound a search by time, then refine by fields or text. Collect matching seed packs for local investigation.</p><code>Time Trie + Tantivy</code></div>
 <div class="question-row"><div>${icon('clock')}<h3>Collections</h3></div><p>Follow one object’s revisions via collection subscriptions. The optional collection index supports history and replay without rebuilding the full object.</p><code>collection.idx → Writes</code></div>
 <div class="question-row"><div>${icon('activity')}<h3>Analytics</h3></div><p>Render counts, errors, and duration distributions from summaries. Daily totals also feed weekly, monthly, and yearly rollups.</p><code>.sum → Analytic Columns</code></div>
 <p class="section-note">Retention is configured per resource and branch. Completeness and lost events remain visible. Scheduled reports and external alert delivery are still planned.</p>
</section>`;

const performance = `
<section class="measurements" id="storage"><div class="wrap">
 <div class="section-heading"><h2>Storage</h2><p>A sample of 1 million searchable events modeled in trunk storage.</p></div>
 <p class="measurement-scope">Default compression, search indexes, and precomputed aggregates. 30 days. Five resources.</p>
 <div class="measure-layout"><div class="measurement-total"><p class="measurement-comparison"><strong>Trunk Datastore vs. JSONL</strong></p><p>${mb(storage.total)} MB vs ${mb(storage.expanded)} MB</p><strong>${(100*(1-storage.total/storage.expanded)).toFixed(1)}<span>% Smaller</span></strong><p>Including Indexes, Summaries & Journals</p><a class="text-link" href="/measurements.txt">Read the Benchmark ${arrow}</a></div>
 <div class="storage-detail"><div class="storage-bar" role="img" aria-label="Storage breakdown: ${storage.components.map(c=>`${c.name} ${mb(c.bytes)} MB`).join(', ')}">${storage.components.map(c=>`<span class="bar-${c.key}" style="flex:${c.bytes}"></span>`).join('')}</div>
 <dl class="storage-list">${storage.components.map(c=>`<div class="bar-${c.key}"><dt>${c.name}</dt><dd>${mb(c.bytes)} MB</dd></div>`).join('')}</dl></div></div>
 <div class="measurement-context"><p><strong>${kb(storage.dailyMedian)} KB</strong> median daily summary transfer for analytics across the fixture’s resources.</p><p><strong>${(storage.total/storage.eventCount).toFixed(1)} bytes/event</strong> for the full indexed datastore, including journals and metadata.</p><p><strong>${mb(storage.seedPacks)} MB</strong> of seed packs in Pods, retrievable by hour for event detail. <a href="/seeds">See How Seeds Are Packed ↗</a></p></div>
 <p class="section-note">These are development measurements, not production guarantees. The linked report includes allocation, coverage, and format tradeoffs.</p>
</div></section>`;

const definitionExample = `<pre class="definition-code"><code>{
  "definition": "payment/transactions",
  "pil": 0,
  "version": 0,
  "fields": [
    { "name": "id",           "type": "UUID" },
    { "name": "revision_id",  "type": "U16"  },
    { "name": "state", "type": "STRING",
      "values": ["pending", "authorized", "captured"] }
  ]
}</code></pre>`;
const definition = (pil = false) => `
<section class="wrap definition-section${pil ? '' : ' home-definition'}" id="${pil ? 'definition' : 'definitions'}">${pil ? '' : '<div class="section-heading"><h2>Definitions</h2><p>Define field positions, types, and dictionaries once. Data seeds share that definition and can carry complete state or a single changed field.</p></div>'}<div>${pil
 ? `<span class="mono-label">PIL / Positional Interchange Layout</span><h2>PIL Definitions</h2><p>Field positions, types, and dictionaries live in a shared Definition. A PIL record can carry complete state or a single changed field.</p><p>A <code>.rx</code> file is a binary PIL Definition. Author it as JSON, or compile CDDL into layouts and nested field bindings. The compiled schema remains editable through JSON. The built-in <code>pil/value</code> layout encodes Definitions, including its own Definition.</p><a class="text-link" href="/definition.cddl" download>Download the Definition Schema ${icon('download')}</a>`
 : `<p>Trie-Trunk data definitions are an implementation of <a href="/PIL#definition">PIL Definitions</a>. <a href="/definition.cddl">CDDL</a> describes the definition structure; Trie-Trunk adds resource bindings and application rules.</p>`
}</div><div class="definition-example"><div class="example-title"><span>${pil ? 'A Definition' : 'A Data Definition'}</span><small>JSON View of PIL Definition</small></div>${definitionExample}<div class="sparse-example"><span>A Change</span><code>state: authorized</code><small>Omitted fields remain omitted.</small></div></div></section>`;

const snootIntro = `
<section class="wrap snoot-intro"><div class="snoot-portrait"><img src="/assets/brand/snoot-forward.png" alt="Front-Facing Snoot Anteater" width="1254" height="1254" loading="lazy"/></div><div><img class="snoot-wordmark" src="/assets/brand/snoot-wordmark.png" alt="Snoot" width="1280" height="426" loading="lazy"/><h2>Search & Investigation</h2><p>Search the journal, inspect a change, and look at the pattern. Snoot brings the detail and the summaries into the same research session.</p><div class="actions"><a class="button" href="https://snoot.trietrunk.com">Open Snoot ${arrow}</a><a class="text-link" href="/snoot">About Snoot ${arrow}</a></div></div></section>`;

const archivedTrunk = `
<div class="trunk-document wrap">
 <aside class="document-rail" aria-label="On This Page">
  <a class="brand main-brand" href="/" aria-label="Trie-Trunk Home">${brand}</a>
  <p>A time-series database.<br>Typed events.<br>Local analytics.</p>
  <nav aria-label="Page Sections"><a href="#tries">Tries</a><a href="#storage">Storage</a><a href="#retention">Retention</a><a href="#analytics">Analytics</a><a href="#definitions">Definitions</a></nav>
  <a class="rail-launch" href="https://snoot.trietrunk.com">Open Snoot ${arrow}</a>
  <small>Open Source / Version Zero</small>
 </aside>
 <div class="document-body"><section class="trunk-intro"><h1><span class="trunk-headline">Hierarchical Time-Series</span><span class="trunk-promise">Minimal Footprint<br>at Lightning Speeds.</span></h1><p>Typed events. Tiny indexes. Precomputed columnar analytics. All within a <a href="#storage">70% smaller storage footprint</a> than expanded JSONL in our million-event benchmark.</p></section>${taxonomy}${performance}${retention}${summaries}${definition()}${snootIntro}</div>
</div>`;

const archivedSnoot = `
<section class="wrap snoot-hero"><div><img class="snoot-wordmark" src="/assets/brand/snoot-wordmark.png" alt="Snoot" width="1280" height="426"/><h1>Snoot through Logs with ease!</h1><p class="intro">Dig into your logs, follow object changes, and explore patterns across time.</p><a class="button" href="https://snoot.trietrunk.com">Try It Out ${arrow}</a></div><img class="snoot-hero-portrait" src="/assets/brand/snoot-forward.png" alt="Front-Facing Snoot Anteater" width="1254" height="1254"/></section>
<section class="wrap query-section"><div class="section-heading"><h2>Query Syntax</h2><p>Pipeline and builder syntax share one query model.<br>Definitions guide field and value completion.</p></div><div class="query-example"><div class="example-title"><div class="syntax-toggle" role="tablist" aria-label="Query Syntax"><button id="syntax-pipeline" type="button" role="tab" data-syntax="pipeline" aria-selected="true" aria-controls="query-pipeline">Pipeline</button><button id="syntax-builder" type="button" role="tab" data-syntax="builder" aria-selected="false" aria-controls="query-builder" tabindex="-1">Builder</button></div><small>Illustrative Query</small></div><pre id="query-builder" role="tabpanel" aria-labelledby="syntax-builder" tabindex="0" hidden><code>events.query()
  .where(level, ==, error)
  .search("timeout")
  .order(timestamp, desc)
  .page(50)</code></pre><pre id="query-pipeline" role="tabpanel" aria-labelledby="syntax-pipeline" tabindex="0"><code>events
  | where level == error
  | search "timeout"
  | order timestamp desc
  | page 50</code></pre></div><div class="query-caption"><span>${icon('search')} Refine by Field or Text</span><span>${icon('clock')} Bound by Your Time Window</span><span>${icon('down')} Scroll for More Events</span></div></section>
${questions}<section class="wrap product-notes"><h2>The Research Workspace</h2><div><p>The SharedWorker coordinates local datasets. Complete hours and cached daily summaries can be reused as you refine an investigation.</p><p>Object History is available when collection indexing is enabled. Lost Events surface rejected data seeds. Session state preserves the research workspace across refreshes.</p><a class="text-link" href="https://snoot.trietrunk.com">Try the Current App ${arrow}</a></div></section>`;

const pil = `
<section class="wrap pil-hero"><p class="project-line">PIL / Positional Interchange Layout</p><h1>Positional Interchange Layout</h1><p class="intro">Typed binary records. Shared Definitions. Compact packs.<br>Independent of the datastore that carries them.</p><div class="actions"><a class="button" href="/pill-packer.txt">Read the Specification ${arrow}</a><a class="text-link" href="https://github.com/AuthenticityLabs/pill-packer">Pill Packer on GitHub ${arrow}</a></div></section>
<section class="wrap layout-section"><div class="section-heading"><h2>Inside a PIL Record</h2><p>Widths follow the Definition<br>and the fields present in each record.</p></div><div class="byte-lane">${[['Field States','Absent · Null · Direct · Offset'],['Integer Widths','Definition-Selected'],['Direct Values','Scalars · Fixed Strings · Dictionary IDs'],['End Offsets','Variable Boundaries'],['Variable Values','Text · Bytes · Nested PIL']].map(([name,detail],i)=>`<div><small>0${i+1}</small><strong>${name}</strong><span>${detail}</span></div>`).join('')}</div><p class="pil-copy">Two bits describe each field’s state. Absent fields carry no value; null is explicit. Adaptive integer widths and dictionaries reduce repeated bytes without changing the logical values.</p></section>
${definition(true)}
<section class="wrap seed-section" id="cddl"><h2>CDDL & Nested Data</h2><p class="seed-lead">Define related types together. Nest records and arrays in PIL, without CBOR or a repeated Definition identifier for every child.</p><div class="seed-explanation"><h3>Schema-Bound Values</h3><p>Named records, optional fields, homogeneous arrays, choices, literals, bounded integers, and supported size constraints compile into PIL layouts. The parent schema binds each child layout. Open <code>OBJECT</code> values use PIL’s built-in value layout.</p></div><div class="seed-explanation"><h3>Explicit Constraints</h3><p>Unsupported CDDL constructs are rejected. Recursive schemas, dynamic maps, generics, and tuple arrays are outside the supported subset. <a href="https://github.com/AuthenticityLabs/pill-packer#command-line-reader">Compiler Reference ${arrow}</a></p></div><div class="actions"><a class="text-link" href="/definition.cddl" download>Definition Schema ${arrow}</a><a class="text-link" href="/pil.rx" download>Bootstrap Definition ${arrow}</a></div></section>
<section class="wrap seed-section" id="strings"><h2>Fixed-Capacity Strings</h2><div class="seed-explanation"><h3><code>STRING(n)</code></h3><p>A UTF-8 slot with an explicit byte length and zero padding. Shorter strings, trailing spaces, and embedded NULs round-trip exactly. Overflow and malformed UTF-8 are rejected.</p></div><div class="seed-explanation"><h3>Capacity in Bytes</h3><p>Length prefixes use 1, 2, or 4 bytes. CDDL text-size constraints remain separate from the storage capacity selected by <code>fixed_strings</code>. Optional fields still make a record variable-length.</p></div></section>
<section class="wrap seed-section" id="packs"><h2>PAC · Packed Array Container</h2><p class="seed-lead">Group records under one Definition. Factor common fields, attach named application headers, and choose how members are addressed.</p><div class="seed-explanation"><h3>Sequential</h3><p>Self-delimiting PIL frames follow one another without a member directory.</p></div><div class="seed-explanation"><h3>Indexed</h3><p>An embedded directory stores each member’s start offset in 2, 3, or 4 bytes. The width is checked against the packed data.</p></div><div class="seed-explanation"><h3>Fixed Width</h3><p>A verified encoded record width and member count support direct ordinal addressing. Every member must have that width after common-field factoring.</p></div><div class="seed-explanation"><h3>Whole-Pack Compression</h3><p>Gzip compresses the complete PAC, including headers and common fields. Decompress before accessing members. Compression is explicit; <code>onlyIfSmaller</code> retains the uncompressed form when gzip would expand it.</p></div></section>
<section class="wrap seed-section" id="reader"><h2>Read & Write With rx</h2><p class="seed-lead">Read records as JSONL, inspect a Definition as JSON, or compile an editable schema into a binary Definition.</p><div class="query-example"><div class="example-title"><span>Command Line</span><a href="/purchase.cddl" download>Example CDDL ${arrow}</a></div><pre><code>rx write purchase.cddl purchase.rx --root purchase
rx read purchase.rx --pretty
rx read purchase.rx records.pil
rx read purchase.rx records.pac
rx read purchase.rx records.pac.gz --pretty
rx write definition.json definition.rx</code></pre></div><p class="pil-copy">Omit the data path to inspect the Definition. Use <code>--pretty</code> for indented JSON.</p></section>
<section class="wrap seed-section" id="validation"><h2>Validation & Trusted Reads</h2><div class="seed-explanation"><h3>Validate at the Boundary</h3><p>Full validation checks field values and structure. Trusted reads retain bounds, offsets, masks, and framing checks while skipping repeated semantic validation of already-validated data.</p></div><div class="seed-explanation"><h3>Verify the Bytes</h3><p>CRC32C checks received bytes against a trusted manifest. Compressed PACs also validate gzip integrity and decoded bounds. Checksums detect corruption; authentication belongs to the transport.</p></div></section>
<section class="wrap seed-section" id="libraries"><h2>Pill Packer Libraries</h2><div class="seed-explanation"><h3>Rust</h3><p>Encoding, decoding, borrowed scans, PAC readers, compression primitives, and the shared CDDL schema runtime. A C ABI supports native bindings.</p></div><div class="seed-explanation"><h3>JavaScript / WASM</h3><p>Rust-backed validation and schema handling, plus portable layout, object, and pack modules. Define, fill, pack, open, get, compress, and unpack records.</p></div><div class="seed-explanation"><h3>Go</h3><p>A Go codec for scalar frames, streaming writes, PAC factoring, addressing, and compression. Native Rust bindings supply shared nested-schema compilation and validation.</p></div><p class="pil-copy">Build from source. Bindings share wire formats; their high-level APIs differ.</p><div class="actions"><a class="text-link" href="https://github.com/AuthenticityLabs/pill-packer#build--test">Build Instructions ${arrow}</a><a class="text-link" href="/pill-packer.txt">Format Reference ${arrow}</a></div></section>
<section class="wrap product-notes"><h2>Data Seeds & Seed Packs</h2><div><p>Trie-Trunk implements PIL as data seeds and PIL PACs as seed packs. Pill Packer owns the codec and compression. Trie-Trunk owns storage, replication, retention, and collection policy.</p><p>Resource roles belong to the <a href="/trunk-definition.cddl">Trie-Trunk Definition profile</a>.</p><a class="text-link" href="/seeds">Explore Seeds ${arrow}</a></div></section>`;

const logger = `
<section class="wrap pil-hero"><p class="project-line">Logger / PIL Ingress</p><h1>Define Once. Emit Changes.</h1><p class="intro">Typed data seeds from your application to Trie-Trunk.<br>Negotiate the Definition before the first write.</p><div class="actions"><a class="button" href="${source('README.md#logger')}">Integration Guide ${arrow}</a><a class="text-link" href="${source('go/logger/client/examples/http/main.go')}">HTTP Example ${arrow}</a></div></section>
<section class="wrap query-section logger-example"><div class="section-heading"><h2>Connect With a Definition</h2><p>The Go logger accepts your compiled resource map.<br>Data seeds encoded as PIL frames can be submitted together.</p></div><div class="query-example"><div class="example-title"><span>PIL Logger</span><small>Go · Inside Your Application</small></div><pre><code>mapBytes, err := os.ReadFile("resource.rx")
if err != nil { return err }

logger, err := pil.Open(ctx, pil.Config{
    URL:        trunkURL,
    Cache:      "prod",
    Branch:     "write",
    Token:      token,
    Definition: mapBytes,
})
if err != nil { return err }
defer logger.Close()

// Data seeds encoded as PIL frames against the producer Definition.
return logger.WriteFrames(ctx, frames...)</code></pre></div><p class="section-note">Import <code>trie-trunk/go/logger/pil</code>. Use <code>WriteValues</code> to encode positional values directly into the accepted layout.</p></section>
<section class="wrap questions"><div class="section-heading"><h2>From Application to Journal</h2><p>One Definition binds the producer and receiver.<br>Partial changes keep their original meaning.</p></div>
<div class="question-row"><div>${icon('code')}<h3>Register</h3></div><p>Exchange Definition identity and digest. Send missing maps, validate compatible server layouts, and prepare the encoder before delivery.</p><code>Definition → Accepted Layout</code></div>
<div class="question-row"><div>${icon('clock')}<h3>Batch & Deliver</h3></div><p>Send immediately or configure a millisecond batching window for concurrent producers. HTTP gzip can compress the entire batch; revisions are preserved without coalescing.</p><code>Data Seeds → HTTP → .seed</code></div>
<div class="question-row"><div>${icon('check')}<h3>Handle Failures</h3></div><p>Observe delivery errors and retain event identities for retries. A successful call requires the server’s staging acknowledgement; queueing alone is not success.</p><code>event_id → Delivery Identity</code></div></section>
<section class="wrap product-notes"><h2>Requests & Object Changes</h2><div><p>The request-scoped logger supplies a fluent builder, HTTP middleware, durations, and checkpoints through <code>client.NewPIL</code>. A request completion does not imply a committed object mutation.</p><p>Use the generic PIL logger for explicit object changes. Define object identity and revision roles when collection replay is enabled. Omitted fields remain absent; null remains an explicit value.</p><p>Definitions can allow <code>@fields</code> for extensions. Compatible newer Definitions can promote these values into typed fields without renaming them.</p><a class="text-link" href="${source('README.md#logger')}">Request Logger Reference ${arrow}</a></div></section>`;

const originalSeeds = seedPages(lifecycle);
const archivedSeeds = Object.entries(originalSeeds).map(([name,page])=>{
 const id=name==='seeds'?'seeds-overview':name.replace('/','-');
 const body=page.body.replace(/href="\/seeds(\/[^"#]+)?"/g,(_,suffix)=>`href="#${suffix?`seeds-${suffix.slice(1)}`:'seeds-overview'}"`);
 return `<div class="archive-group archive-seeds" id="${id}"><p class="wrap archive-label">Seeds / ${name==='seeds'?'Overview':name.split('/')[1]}</p>${body}</div>`;
}).join('');
const archiveIndex=[['storage','Storage'],['retention','Retention'],['analytics','Analytics'],['definitions','Definitions'],['tries','Tries'],['time-trie','Time Trie'],['event-state-trie','State Trie'],['archived-snoot','Snoot'],['archived-snoot-preview','Previous Snoot Preview'],['archived-homepage-charts','Previous Homepage Charts'],['archived-homepage-dashboard','Previous Homepage Dashboard'],['seeds-overview','Seeds Overview'],['seeds-definitions','Seeds Definitions'],['seeds-collections','Seeds Collections'],['seeds-messaging','Seeds Messaging'],['logger','Logger']];
export const pages = {
 trunk:{title:'Trie-Trunk — Time-Series Database',description:'An open-source time-series database for typed events, temporal search, and precomputed columnar summaries. Collect data locally for browser analytics.',landing:true,body:landing(lifecycle,brand)},
 PIL:{title:'PIL — Positional Interchange Layout',description:'Definition-driven binary records for full objects, partial changes, dictionaries, and PAC containers. Portable Pill Packer libraries for Rust, JavaScript/WASM, and Go.',body:pil},
 hidden:{title:'Hidden — Archived Content',description:'Content set aside from the public navigation for later review and reintegration.',hidden:true,body:`<aside class="wrap archive-heading"><h1>Hidden</h1><p>Original pages and sections, preserved for later reintegration.</p><nav class="archive-index" aria-label="Archived Sections">${archiveIndex.map(([id,name])=>`<a class="text-link" href="#${id}">${name}</a>`).join('')}</nav></aside><div class="archive-group" id="archived-trunk"><p class="wrap archive-label">Trunk / Previous Page</p>${archivedTrunk}</div><div class="archive-group" id="archived-snoot"><p class="wrap archive-label">Snoot / Previous Page</p>${archivedSnoot}</div><section class="wrap archive-group" id="archived-snoot-preview"><p class="archive-label">Snoot / Previous Preview</p>${previousSnootPreview}</section><section class="wrap archive-group" id="archived-homepage-charts"><p class="archive-label">Trunk / Previous Homepage Charts</p>${archivedHomepageCharts}</section><section class="wrap archive-group" id="archived-homepage-dashboard"><p class="archive-label">Trunk / Previous Full Dashboard Embed</p><a class="text-link" href="/snoot-preview/?view=dashboard">Open the Full Dashboard With Sample Data ↗</a></section>${archivedSeeds}<div class="archive-group" id="logger"><p class="wrap archive-label">Logger / Previous Page</p>${logger}</div>`}
};
const attribute = value => value.replaceAll('&','&amp;').replaceAll('"','&quot;').replaceAll('<','&lt;');
const navigation = ['trunk','snoot','seeds'];
export function render(name,css,js) {
 const p=pages[name], canonical=`https://trietrunk.com${pagePath(name)}`;
 const title=attribute(p.title), description=attribute(p.description);
 const shareImage='https://trietrunk.com/assets/brand/trie-trunk-gray.jpg';
 return `<!doctype html><html lang="en"><head>
 <meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
 <title>${title}</title><meta name="description" content="${description}">${p.hidden?'<meta name="robots" content="noindex, nofollow">':''}
 <meta name="color-scheme" content="light dark"><meta name="theme-color" content="#fafafa">
 <script>try{const p=localStorage.getItem('trietrunk.theme');const mode=['light','dark'].includes(p)?p:'auto';const dark=mode==='dark'||mode==='auto'&&matchMedia('(prefers-color-scheme: dark)').matches;document.documentElement.dataset.theme=dark?'dark':'light';document.documentElement.dataset.themePreference=mode;document.querySelector('meta[name="theme-color"]').content=dark?'#181818':'#fafafa';}catch{document.documentElement.dataset.theme=matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';}</script>
 <meta property="og:type" content="website"><meta property="og:site_name" content="Trie-Trunk">
 <meta property="og:title" content="${title}"><meta property="og:description" content="${description}">
 <meta property="og:url" content="${canonical}"><meta property="og:locale" content="en_US">
 <meta property="og:image" content="${shareImage}"><meta property="og:image:secure_url" content="${shareImage}">
 <meta property="og:image:type" content="image/jpeg"><meta property="og:image:width" content="1254"><meta property="og:image:height" content="1254"><meta property="og:image:alt" content="Trie-Trunk grayscale tree and wordmark">
 <meta name="twitter:card" content="summary"><meta name="twitter:title" content="${title}"><meta name="twitter:description" content="${description}"><meta name="twitter:image" content="${shareImage}"><meta name="twitter:image:alt" content="Trie-Trunk grayscale tree and wordmark">
 <link rel="canonical" href="${canonical}"><link rel="icon" href="/assets/brand/trie-trunk-transparent.png" type="image/png">
 <link rel="stylesheet" href="/assets/${css}"><script src="/assets/${js}" type="module"></script>
 </head><body data-page="${name}"${p.landing?' class="landing-site"':''}><a class="skip-link" href="#main">Skip to Content</a>
 <header class="site-header wrap"><a class="brand header-brand" href="/" aria-label="Trie-Trunk Home">${brand}</a><nav aria-label="Main Navigation">${navigation.map(n=>`<a href="${p.landing?`#${n}`:`/#${n}`}" data-page-link="${n}" ${n===name?'aria-current="page"':(n==='seeds' && (name.startsWith('seeds/') || name==='PIL'))?'aria-current="location"':''}>${n[0].toUpperCase()+n.slice(1)}</a>`).join('')}<a href="${github}">GitHub ${arrow}</a></nav><div class="theme-picker" role="group" aria-label="Color Theme">${['light','dark','auto'].map(mode=>`<button type="button" data-theme-choice="${mode}" aria-pressed="false" title="${mode==='auto'?'Follow System Theme':mode==='dark'?'Dark Mode':'Light Mode'}">${mode[0].toUpperCase()+mode.slice(1)}</button>`).join('')}</div></header>
 <main id="main">${p.body}</main>${p.landing?'':`<footer class="wrap site-footer"><div><a href="${source('README.md#licensing')}">Licensing</a><a href="${source('README.md#architecture')}">Architecture</a><a href="${github}">GitHub ${arrow}</a></div><p>Authenticity Labs · Version Zero</p></footer>`}</body></html>`;
}
