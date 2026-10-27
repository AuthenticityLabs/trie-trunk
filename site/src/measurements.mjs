import {readFileSync} from 'node:fs';
export const benchmark = '2026-10-26-million-events-standard';
const root = new URL(`../../benchmarks/storage/${benchmark}/`,import.meta.url);
const read = name => JSON.parse(readFileSync(new URL(name,root),'utf8'));
const fixture = read('footprint.json');
const summary = read('summary-comparison.json');
const compression = read('compression.json');
const timing = read('compression-timing.json');
const c = fixture.by_component;
const fast = compression.levels.find(row=>row.gzip_level===1);
const dense = compression.levels.find(row=>row.gzip_level===6);
if(fixture.events!==1_000_000 || fixture.object_index!==false || fixture.pac_gzip_level!==1 ||
   (c['collection.idx']||0)!==0 || c['.sum']!==summary.new_file_bytes ||
   compression.events!==fixture.events || compression.source_bytes!==fixture.expanded_jsonl_bytes ||
   compression.source_sha256!==fixture.expanded_jsonl_sha256 || timing.source_sha256!==compression.source_sha256 || timing.events!==fixture.events || timing.hourly_packets!==compression.hourly_packets || !fast || !dense) {
 throw new Error('Website measurements require the verified million-event, collections-disabled fixture and matching compression results.');
}
for (const row of compression.levels) {
 const matches = timing.rows.filter(t=>t.gzip_level===row.gzip_level);
 if (matches.length!==2 || !['jsonl','seed_packs'].every(format=>matches.some(t=>t.format===format && Number.isFinite(t.encode_ms) && t.encode_ms>0 && Number.isFinite(t.decode_ms) && t.decode_ms>0))) {
  throw new Error('Each compression level requires measured JSONL and seed-pack timings.');
 }
}
export const storage = {
 events: fixture.events.toLocaleString('en-US'), eventCount: fixture.events,
 total: fixture.trie_trunk_bytes, expanded: fixture.expanded_jsonl_bytes,
 // N compact objects plus N line feeds become N-1 commas and two brackets.
 expandedArray: fixture.expanded_jsonl_bytes + 1,
 seedPacks: c['.pods'], standalonePacks: fast.standalone_pac_bytes,
 dailyMedian: summary.whole_day_median_bytes,
 compression: compression.levels.map(row=>({...row,timing:timing.rows.filter(t=>t.gzip_level===row.gzip_level)})),
 timing,
 showCompressionComparison: [fast,dense].every(row=>1-row.standalone_pac_bytes/row.jsonl_hourly_bytes>=0.15),
 components: [
  {name:'Pods', key:'pods', bytes:c['.pods']},
  {name:'Tantivy', key:'search', bytes:c.tantivy},
  {name:'Tries', key:'tri', bytes:c['.tri']},
  {name:'Indexes', key:'idx', bytes:c['.idx']},
  {name:'Summaries', key:'sum', bytes:c['.sum']},
 ],
};
storage.components.push({name:'Journals & Other',key:'other',bytes:storage.total-storage.components.reduce((n,c)=>n+c.bytes,0)});
export const mb = bytes => (bytes/1_000_000).toFixed(2);
export const kb = bytes => (bytes/1_000).toFixed(1);

export const measurementReport = `Trie-Trunk: One Million Events — Standard Storage

Measured synthetic fixture: 30 days, five resources, five branches, 15 Definition
fields, adaptive integers level 16. Collection indexing is DISABLED. All branches,
including writes, use the default gzip-1 policy; incompressible packs stay plain.
The fixture cycles through 250 object IDs per resource; it is not one million
distinct objects. Messages and dictionary values repeat. Production results vary.

${storage.components.map(c=>`${c.name}: ${c.bytes.toLocaleString('en-US')} bytes`).join('\n')}
Entire Datastore: ${storage.total.toLocaleString('en-US')} bytes
Reduction vs Expanded JSONL: ${(100*(1-storage.total/storage.expanded)).toFixed(1)}%
Filesystem Allocated: ${fixture.trie_trunk_allocated_bytes.toLocaleString('en-US')} bytes
Expanded JSONL: ${storage.expanded.toLocaleString('en-US')} bytes
Expanded JSON Array: ${storage.expandedArray.toLocaleString('en-US')} bytes
Array size is derived by replacing newlines with commas and enclosing the same
objects in brackets (+1 byte). No pretty-printing is added.
Median Daily Summary Transfer: ${storage.dailyMedian.toLocaleString('en-US')} bytes

Standalone Seed Packs vs JSONL — Identical Hourly Boundaries
${compression.levels.map(row=>`Gzip-${row.gzip_level}:
  Hourly JSONL: ${row.jsonl_hourly_bytes.toLocaleString('en-US')} bytes
  Hourly PIL PACs + Definitions: ${row.standalone_pac_bytes.toLocaleString('en-US')} bytes
  Reduction vs hourly JSONL: ${(100*(1-row.standalone_pac_bytes/row.jsonl_hourly_bytes)).toFixed(1)}%
`).join('\n')}

Compression/decompression elapsed time, milliseconds for all one million events:
${timing.rows.map(row=>`Gzip-${row.gzip_level} ${row.format}: compress ${row.encode_ms.toFixed(1)} ms; decompress ${row.decode_ms.toFixed(1)} ms`).join('\n')}
Median of ${timing.repeats} serial passes, GOMAXPROCS=${timing.gomaxprocs}, ${timing.os}/${timing.arch}, ${timing.cpu}.
${timing.notes.join('\n')}

Both formats use ${compression.go} compress/gzip, the same records and ordering.
Hourly grouping: resource, branch, UTC hour (${compression.hourly_packets} packets).
Standalone PACs retain every payload field, including complete event UUIDs.
Routing context is in PACSource; timestamps are verified against UUIDs.
Definition bytes are counted once; compressed PAC headers are included.
Every PAC member was resolved and compared byte-for-byte with its input frame.
Pods are measured separately: stored packs place event identity in branch indexes.
Do not treat Pods alone as a complete standalone encoding or an entire datastore.

Every stored event was read back; UUID uniqueness and counts were verified.
Tantivy catch-up completed before accounting. Datastore totals include all files,
journals and metadata after close. Datastore figures are size measurements; separate codec timings above do not
measure ingestion, retrieval, or query latency. Enabling Collections adds indexing and uncompressed write
storage for direct replay; that optional profile is excluded from these figures.

Native-memory budget override: ${fixture.native_memory_budget_override} bytes per
resource for this bulk run. This is a limit, not measured RSS or an ingestion SLA.
Source JSONL SHA-256: ${compression.source_sha256}
Raw measurement records:
https://trietrunk.com/measurements/footprint.json
https://trietrunk.com/measurements/compression.json
https://trietrunk.com/measurements/compression-timing.json
https://trietrunk.com/measurements/summary-comparison.json
https://trietrunk.com/measurements/metadata.json
`;
