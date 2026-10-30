import {storage, mb} from './measurements.mjs';

const tabs = ['Overview', 'Definitions', 'Collections', 'Messaging'];
const path = tab => tab === 'Overview' ? 'seeds' : `seeds/${tab.toLowerCase()}`;
const row = (title, copy) => `<div class="seed-explanation"><h3>${title}</h3><p>${copy}</p></div>`;

export const seedPackComparison = `
<section class="seed-comparison" aria-labelledby="seed-comparison-title">
 <div class="seed-section-heading"><p class="project-line">1,000,000 Events · Same Synthetic Dataset</p><h2 id="seed-comparison-title">Expanded JSON → Seed Packs</h2></div>
 <div class="seed-size"><div><h3>Expanded JSON Array</h3><strong>${mb(storage.expandedArray)} <span>MB</span></strong></div><div class="seed-size-track"><span style="width:100%"></span></div></div>
 <div class="seed-size seed-size-packed"><div><h3>Seed Packs <span>Implementation of PIL PACs</span></h3><strong>${mb(storage.standalonePacks)} <span>MB</span></strong></div><div class="seed-size-track"><span style="width:${100 * storage.standalonePacks / storage.expandedArray}%"></span></div></div>
 <div class="seed-result"><strong>${(100 * (1 - storage.standalonePacks / storage.expandedArray)).toFixed(1)}% <span>Less Storage</span></strong><p>Complete standalone seed packs at default gzip-1, including UUIDs and definitions.</p></div>
 <details class="seed-evidence"><summary>Measurement Details</summary><p>One million synthetic events across five resources over 30 days, with repeated messages and dictionary values. The JSON array retains expanded field names and routing context. Its size is derived from the verified JSONL export by replacing separators and adding brackets: one extra byte.</p><p>All branches, including writes, use compression. Complete event identities stay in this standalone comparison; nothing is moved into an external index. Definition and packet overhead are included. The full indexed datastore is measured separately on the Trunk page.</p><a href="/measurements.txt" class="text-link">Read the Measurements ↗</a></details>
</section>`;

const overview = `${seedPackComparison}
${storage.showCompressionComparison ? `<section class="seed-section"><h2>Smaller After Compression, Too</h2><p class="seed-lead">Same million events. Same hourly boundaries. Same gzip implementation and level.</p>
${storage.compression.map(row=>`<table class="seed-compression-table"><caption>Gzip-${row.gzip_level}${row.gzip_level===1?' · Default':''}</caption><thead><tr><th scope="col">Format</th><th scope="col">Size</th><th scope="col">Compress</th><th scope="col">Decompress</th></tr></thead><tbody>
${[['jsonl','JSONL',row.jsonl_hourly_bytes],['seed_packs','Seed Packs',row.standalone_pac_bytes]].map(([key,label,size])=>{const t=row.timing.find(t=>t.format===key);return `<tr><th scope="row">${label}</th><td>${mb(size)} MB</td><td>${(t.encode_ms/1000).toFixed(2)} s</td><td>${(t.decode_ms/1000).toFixed(2)} s</td></tr>`}).join('')}
<tr class="seed-compression-saving"><th scope="row">Less Storage</th><td colspan="3">${(100*(1-row.standalone_pac_bytes/row.jsonl_hourly_bytes)).toFixed(1)}%</td></tr>
</tbody></table>`).join('')}
<p class="seed-compression-note">Complete seed packs, including definitions and packet headers. Times cover compression and decompression of all one million events; median of three serial runs.</p>
<details class="seed-evidence"><summary>Timing Method</summary><p>Gzip is the supported compressor; these are three compression levels. Timings use prebuilt hourly JSONL and seed packs, excluding disk access, JSON serialization, PIL packing, and rendering. ${storage.timing.cpu}, ${storage.timing.go}, one worker.</p><a href="/measurements.txt" class="text-link">Full Measurement Details ↗</a></details></section>` : ''}
<section class="seed-section"><h2>Define Once. Stop Repeating.</h2>
 ${row('Field Positions Replace Keys', 'The data definition carries field names and types. Each data seed stores values in the agreed positions, instead of repeating the same keys in every object.')}
 ${row('Store the Value at Its Defined Width', 'Numbers use binary types rather than decimal text. Declared string dictionaries replace repeated values with compact IDs. Optional adaptive integer encoding reduces widths when values fit.')}
 ${row('Send Only the Change', 'A data seed can hold a complete object or a partial change. Absent and null remain distinct, so an omitted field does not silently erase a value.')}
 ${row('Share Values, Then Compress', 'Seed packs group data seeds, factor common values, and compress the remaining body. The codec owns decoding, so the same data can travel into local storage and analysis.')}
 <div class="seed-format-strip"><span>Data Definition</span><span aria-hidden="true">→</span><span>Data Seeds</span><span aria-hidden="true">→</span><span>Seed Packs</span></div>
</section>`;

const definitions = `
<section class="seed-section seed-first"><p class="project-line">Shared Structure</p><h2>Data Definitions</h2><p class="seed-lead">Agree on the fields before sending the values. Producers, storage, and readers use the same definition.</p>
 <div class="seed-definition-grid"><div class="seed-definition-copy">
 <h3>Order, Types & Dictionaries</h3><p>Field order supplies position. Types specify how values are encoded. A dictionary gives common strings stable IDs.</p>
 <h3>Portable Format, Application Rules</h3><p>CDDL describes the portable definition structure. Trie-Trunk adds resource bindings and checks for event identity, collection roles, and other application conventions.</p>
 <h3>Room for Extensions</h3><p>Definitions can declare <code>@fields</code> for additional values. Extensible annotations carry metadata; selected validators enforce rules.</p>
 </div><div class="query-example"><div class="example-title">A Data Definition <span>JSON View of PIL Definition</span></div><pre><code>{
  "pil": 0,
  "definition": "orders",
  "version": 0,
  "fields": [
    { "name": "id", "type": "UUID" },
    { "name": "revision_id", "type": "U16" },
    { "name": "state", "type": "STRING",
      "values": ["pending", "paid", "shipped"] }
  ]
}</code></pre></div></div>
</section>
<section class="seed-section"><h2>One Registration Path</h2><p class="seed-lead">Planned: JSON folder ingestion, upload, and a definition editor feeding the same validation and registration pipeline.</p>
 ${row('Validate & Bind', 'Check the portable structure, compile the field layout, then apply the selected application validators before publishing a binding.')}
 ${row('Preserve the Contract', 'Readers need the exact definition used by the producer. Definition changes create a new binding; metadata and unknown annotations can be retained when forwarding the definition document.')}
 <div class="actions"><a class="text-link" href="/PIL#definition">PIL Definitions ↗</a><a class="text-link" href="/definition.cddl">Portable CDDL ↗</a><a class="text-link" href="/trunk-definition.cddl">Trie-Trunk Profile ↗</a></div>
</section>`;

const collections = `
<section class="seed-section seed-first"><p class="project-line">Design Direction</p><h2>Collections Bound to Definitions</h2><p class="seed-lead">Define the object. Work with its changes. Let the collection layer coordinate replication, event subscriptions, and write consistency.</p>
 <div class="seed-format-strip"><span>Object Definition</span><span aria-hidden="true">→</span><span>Revision History</span><span aria-hidden="true">→</span><span>Subscribed Views</span></div>
 ${row('Identity & Revision', 'Bind object identity and revision roles to the definition. Keep changes associated with the object they describe, with explicit handling of absent and null fields.')}
 ${row('Replay & Subscribe', 'The intended experience combines a consistent starting state with subsequent changes, handling reconnects and catch-up through the collection layer.')}
 ${row('Write With a Contract', 'Expose clear acknowledgement, retry, conflict, and ordering rules through the collection API. Callers should not have to implement those rules separately for every integration.')}
</section>
<section class="seed-section"><h2>What Exists, What Comes Next</h2><div class="seed-definition-grid"><div><h3>Available Today</h3><p>Collection indexing and object replay provide the foundation for inspecting recorded revisions in Snoot.</p><a class="text-link" href="/snoot">Explore Snoot ↗</a></div><div><h3>Still to Define</h3><p>The collection API, live subscription handoff, conflict policy, and consistency guarantees. This is the direction for the framework, not a claim of distributed database guarantees today.</p></div></div></section>`;

const messaging = `
<section class="seed-section seed-first"><p class="project-line">Compatibility & Interoperability</p><h2>Keep the Definition Across Platforms</h2><p class="seed-lead">Data seeds carry the values. A transport binding supplies the definition, delivery identity, and acknowledgement contract.</p>
 <div class="seed-format-strip"><span>Producer</span><span aria-hidden="true">→</span><span>Definition + Data Seeds</span><span aria-hidden="true">→</span><span>Consumer</span></div>
 <div class="seed-transport-list">
 ${row('HTTP · Available', 'The Trie-Trunk logger negotiates definitions and delivers batches to ingress. A successful call requires a staging acknowledgement. See the <a href="/hidden#logger">Logger integration</a>.')}
 ${row('Brokers · Proposed Bindings', 'Kafka, AMQP, and JMS are candidates for adapters that carry seed payloads and definition references. Each binding must specify partition or ordering keys, retries, acknowledgements, and replay behavior; these adapters are not implemented yet.')}
 ${row('Existing JSON Producers', 'A bridge can map source fields into an accepted data definition before encoding. Mapping and validation must be explicit; a binary payload alone does not make mismatched schemas compatible.')}
 </div>
</section>
<section class="seed-section"><h2>A Shared Codec Boundary</h2><p class="seed-lead">Pill Packer provides the portable PIL and PAC implementation for Rust, with initial JavaScript/WASM and Go reader bindings. Transport and delivery policy remain with the integration.</p><div class="actions"><a class="button" href="/PIL#libraries">Pill Packer Libraries ↗</a><a class="text-link" href="/hidden#logger">Connect a Producer ↗</a></div></section>`;

export const seedContents = {Overview: overview, Definitions: definitions, Collections: collections, Messaging: messaging};
const contents = seedContents;
export const seedPages = lifecycle => Object.fromEntries(tabs.map(tab => [path(tab), {
 title: `Seeds${tab === 'Overview' ? '' : ` / ${tab}`} — Trie-Trunk`,
 description: tab === 'Overview' ? 'Compare expanded JSON with compact seed packs, then explore data definitions, collections, and interoperability.' : `Data seeds: ${tab.toLowerCase()} built around shared definitions and portable PIL encoding.`,
 body: `<div class="wrap seeds-document"><header class="seeds-heading"><h1>Seeds</h1><p>Defined data. Compact changes. Shared understanding.</p></header><nav class="seed-tabs" aria-label="Seeds Sections">${tabs.map(label => `<a href="/${path(label)}" ${label === tab ? 'aria-current="page"' : ''}>${label}</a>`).join('')}</nav>${tab === 'Overview' ? lifecycle : ''}${contents[tab]}<aside class="seed-spec-link"><p>Data seeds and seed packs are Trie-Trunk’s implementation of <a href="/PIL">PIL and PIL PACs</a>.</p><a class="text-link" href="/PIL">Specification & Pill Packer Libraries ↗</a></aside></div>`
}]));
