import {readFileSync} from 'node:fs';
const benchmark = '2026-10-24-million-events';
const root = new URL(`../../benchmarks/storage/${benchmark}/`,import.meta.url);
const read = name => JSON.parse(readFileSync(new URL(name,root),'utf8'));
const fixture = read('footprint.json');
const summary = read('summary-comparison.json');
const archive = read('archive.json');

const c = fixture.by_component;
export const storage = {
 events: fixture.events.toLocaleString('en-US'),
 eventCount: fixture.events,
 total: fixture.trie_trunk_bytes,
 expanded: fixture.expanded_jsonl_bytes,
 gzip: archive.gzip6_bytes,
 dailyMedian: summary.whole_day_median_bytes,
 components: [
  {name:'Event Pods', key:'pods', bytes:c['.pods']},
  {name:'Tantivy', key:'search', bytes:c.tantivy},
  {name:'Tries', key:'tri', bytes:c['.tri']},
  {name:'Indexes', key:'idx', bytes:c['.idx']+c['collection.idx']},
  {name:'Summaries', key:'sum', bytes:c['.sum']},
 ],
};
storage.components.push({name:'Journals & Other',key:'other',bytes:storage.total-storage.components.reduce((n,c)=>n+c.bytes,0)});
if(fixture.events!==1_000_000 || c['.sum']!==summary.new_file_bytes || archive.source_bytes!==fixture.expanded_jsonl_bytes || archive.source_sha256!==fixture.expanded_jsonl_sha256) {
 throw new Error('Website measurements must use the matching million-event fixture.');
}
export const mb = bytes => (bytes/1_000_000).toFixed(2);
export const kb = bytes => (bytes/1_000).toFixed(1);

// Generate the public evidence summary from tracked measurements, not local notes.
export const measurementReport = `Trie-Trunk: One Million Events

Measured synthetic fixture, not extrapolated: 30 days, five resources, five
branches, 15 Definition fields, adaptive integers level 16, collection indexing
enabled. The fixture cycles through 250 object identities per resource; this is
not one million distinct objects. Messages and mapped values repeat.

${storage.components.map(c=>`${c.name}: ${c.bytes.toLocaleString('en-US')} bytes`).join('\n')}
Entire Datastore: ${storage.total.toLocaleString('en-US')} bytes
Filesystem Allocated: ${fixture.trie_trunk_allocated_bytes.toLocaleString('en-US')} bytes
Expanded JSONL: ${storage.expanded.toLocaleString('en-US')} bytes
Expanded JSONL gzip-6: ${storage.gzip.toLocaleString('en-US')} bytes
Median Daily Summary Transfer Across Resources: ${storage.dailyMedian.toLocaleString('en-US')} bytes

Every event was read back; UUID uniqueness and counts were verified. Tantivy
catch-up completed before accounting. All datastore files, journals and metadata
are included after close. Write PACs are uncompressed; other PACs use default
gzip. Whole-file gzip JSONL has no equivalent indexes, summaries, replay lookup
or independently retrievable hourly segments. These are storage measurements,
not ingestion-throughput or query-latency claims.

The bulk fixture used an 8 GiB native-memory budget per resource. The initial
1 GiB run hit staging backpressure. This override is a limit, not measured RSS;
the results do not establish unpaced ingestion under the default budget.

Source JSONL SHA-256: ${archive.source_sha256}
Evidence and reproduction inputs:
https://github.com/AuthenticityLabs/trie-trunk/tree/main/benchmarks/storage/${benchmark}
See the repository README for reproduction commands. Bulk exports are generated
locally rather than committed. Data is synthetic; production results will vary.
`;
