// Illustrative comparison values stay labelled separately from measured storage.
import {seedPackComparison} from './seeds.mjs';
import {storage,mb} from './measurements.mjs';

const platforms = ['Trie-Trunk', 'ClickHouse', 'Elasticsearch', 'Grafana Loki'];
const rows = [
 {name:'Stored Footprint',note:'All data, indexes, summaries, and journals',unit:'GB',values:['1.28','1.64','2.86','1.02']},
 {name:'Sustained Ingest',note:'Accepted events per second · under query load',unit:'events/s',values:['24,000','32,000','18,000','28,000']},
 {name:'Searchable Delay',note:'P95 · send to query visibility',unit:'ms',values:['320','240','840','560']},
 {name:'Filtered Search',note:'P95 · last 24 hours, first 100 matches',unit:'ms',values:['64','48','40','190']},
 {name:'Text Search',note:'P95 · token match, first 100 events',unit:'ms',values:['88','145','52','260']},
 {name:'Hourly Counts',note:'P95 · seven days, grouped by service and state',unit:'ms',values:['42','36','72','165']},
 {name:'Peak Server Memory',note:'Entire required server stack · RSS',unit:'GiB',values:['2.4','3.8','6.2','2.1']},
 {name:'Load CPU',note:'CPU seconds per million ingested events',unit:'CPU-s',values:['140','110','220','160']},
];

const logBench = `<svg class="log-bench" viewBox="0 0 380 215" role="img" aria-label="A bench with flat-topped half logs for the seat and back, supported by small tree trunks with roots spreading into the ground">
 <g stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round">
  <path d="M39 183 276 202 354 158" fill="none" opacity=".12"/>
  <g fill="var(--subtle)">
   <path d="m265 86 37-9c0 38-4 61 0 74 3 6 15 7 26 11-9 3-21-1-27-2 2 6 8 10 15 14-11 1-18-5-23-9-7 3-10 11-23 13 2-6 8-10 9-15-10 4-20 3-31 0 10-3 15-8 16-14 2-23-1-45 1-63Z"/>
   <ellipse cx="283" cy="79" rx="20" ry="10"/>
   <path d="m78 128 43 5c-2 19-5 37 2 48 5 6 17 7 27 11-10 2-21-1-29-3 1 7 7 11 13 16-13 0-22-7-28-12-6 4-12 12-25 14 2-6 8-11 10-16-12 3-23 3-38-1 10-3 20-7 22-15 3-14 1-31 3-47Z"/>
   <ellipse cx="99" cy="132" rx="23" ry="11"/>
  </g>
  <g fill="none" opacity=".35">
   <path d="M273 120c-2 14 2 24-3 34l-10 8m24-43c-1 15 2 27-1 39l-8 14m19-57c-2 14-2 28 4 38l17 9M86 150c-2 12 3 25-4 34l-14 5m29-38c-1 12 2 26-1 36l-9 14m23-50c-3 15 1 30 8 37l22 4m-39-13c3 7 7 14 17 19"/>
   <path d="M278 80c1-5 14-5 14-1s-13 6-14 1M90 132c0-6 17-5 18 0s-18 6-18 0"/>
  </g>
  <path d="m60 104 229-54 40 21-229 54Z" fill="var(--surface)"/>
  <path d="m100 125 229-54c0 11-9 19-20 15L80 140Z" fill="var(--subtle)"/>
  <path d="m60 104 40 21c0 14-9 20-20 15-14-6-23-24-20-36Z" fill="var(--canvas)"/>
  <g fill="none" opacity=".46">
   <path d="m82 105 206-47m-189 55 195-46M113 111l93-23m10-3 94-23"/>
   <path d="m117 125 78-18m15-4 103-24m-189 48 114-27m9-2 60-14"/>
   <path d="m68 114 24 13c-1 10-19 7-24-13m6 7 11 6c-5 2-8-1-11-6"/>
   <path d="M192 82c9-7 27-10 34-8-7 8-24 12-34 8Zm8-1 18-4"/>
  </g>
  <path d="m88 89-1-42 11-2 2 41m197-43-1-22 10-2 2 21" fill="var(--subtle)"/>
  <path d="m72 39 217-29 31 17-217 29Z" fill="var(--surface)"/>
  <path d="m103 56 217-29c0 10-9 18-17 13L86 69Z" fill="var(--subtle)"/>
  <path d="m72 39 31 17c0 13-8 18-17 13-11-5-17-20-14-30Z" fill="var(--canvas)"/>
  <g fill="none" opacity=".4">
   <path d="m96 41 184-26m-170 38 186-31m-184 38 188-25"/>
   <path d="m79 49 17 9c-1 8-14 6-17-9m5 7 7 4c-3 1-6-1-7-4"/>
  </g>
 </g>
</svg>`;

const comparison = `<div class="bench-comparison" data-bench-comparison>
 <div class="bench-panel-heading"><h2>Performance</h2><p class="bench-dataset">*1,000,000 Events · Same Synthetic Dataset</p></div>
 <table class="bench-table">
  <caption class="bench-sr-only">Structured events and logs. All numbers are illustrative layout samples, including Trie-Trunk. They are not benchmark results.</caption>
  <thead><tr><th scope="col">Measurement</th>${platforms.map(name=>`<th scope="col">${name}<span>Sample Values</span></th>`).join('')}</tr></thead>
  <tbody>${rows.map(row=>`<tr><th scope="row">${row.name}<span>${row.note}</span></th>${row.values.map((value,i)=>`<td data-platform="${platforms[i]}"><span class="bench-value">${value}</span><span class="bench-unit">${row.unit}</span></td>`).join('')}</tr>`).join('')}</tbody>
 </table>
 <div class="bench-table-foot"><p class="bench-profile">*Proposed: 10 million events · 30 days · 8 vCPU · 32 GiB RAM · local NVMe</p><a class="text-link" href="/bench-proposed-run.json" download>Download the Proposed Run ↗</a></div>
 <details class="bench-method"><summary>Proposed Run Conditions</summary>
  <div class="bench-method-body">
   <p>Start with a shared structured-event corpus at 1 million, 10 million, and 100 million events. Preserve identical records and query answers across native platform adapters. Each platform keeps the full event detail.</p>
   <dl>
    <div><dt>Ingest and Freshness</dt><dd>Measure bulk load separately from a mixed read/write run. Report durable acceptance, errors, backpressure, and query visibility separately. Use a live replay for freshness; historic event timestamps are not ingest latency.</dd></div>
    <div><dt>Search and Analytics</dt><dd>Run field filters, exact token matching, and exact hourly counts. Record each query’s cold and warm results separately, then repeat at 1 and 8 concurrent clients. Never pool different queries into one P95.</dd></div>
    <div><dt>Cardinality and Frequency</dt><dd>Test repeated states, high-cardinality identifiers, skewed value frequencies, and a change in their distribution. Keep each profile separate so favorable repetition cannot hide expensive cases.</dd></div>
    <div><dt>Equivalent Conditions</dt><dd>Pin hardware, releases, durability, retention, precision, and result limits. Publish index and precomputation settings. Count their storage and build costs. Mark unsupported queries explicitly.</dd></div>
    <div><dt>The Full Footprint</dt><dd>Include indexes, summaries, metadata, and journals. Measure the complete server stack; report client CPU, memory, and network transfer separately for browser analytics.</dd></div>
   </dl>
   <p class="bench-references">Workload references: <a href="https://github.com/ClickHouse/ClickBench">ClickBench</a>, <a href="https://github.com/elastic/rally-tracks/tree/master/http_logs">Rally HTTP Logs</a>, <a href="https://pkg.go.dev/github.com/grafana/loki/v3/pkg/logql/bench">Loki LogQL Bench</a>. Use <a href="https://github.com/timescale/tsbs">TSBS</a> for a separate numeric time-series profile.</p>
  </div>
 </details>
</div>`;

const defaultCompression=storage.compression.find(row=>row.gzip_level===1);
const matchedSaving=(100*(1-defaultCompression.standalone_pac_bytes/defaultCompression.jsonl_hourly_bytes)).toFixed(1);
// Use the measured uncompressed PACs. Raw individual records require at least
// 58 MB for the fixture's two UUIDs and shortest message/instance/release strings
// alone, before their remaining fields and framing; the PACs total 56.41 MB.
const rawSeeds=`<div class="seed-size seed-size-raw"><div><h3>Raw Seeds <span>Uncompressed Seed Packs · No Gzip</span></h3><strong>${mb(storage.standalonePlainPacks)} <span>MB</span></strong></div><div class="seed-size-track"><span style="width:${100*storage.standalonePlainPacks/storage.expandedArray}%"></span></div></div>`;
const transit=seedPackComparison
 .replaceAll('seed-comparison-title','bench-transit-title')
 .replace('<h3>Expanded JSON Array</h3>','<h3>Raw JSON <span>Expanded JSON Array</span></h3>')
 .replace('Implementation of PIL PACs','Standalone PIL PACs · Gzip-1')
 .replace('<div class="seed-size seed-size-packed">',`${rawSeeds}<div class="seed-size seed-size-packed">`)
 .replace('Less Storage','Smaller Than Raw JSON')
 .replace('on the Trunk page','in the Storage tab')
 .replace('<details class="seed-evidence">',`<p class="bench-transit-gzip">${matchedSaving}% smaller than hourly JSONL at the same gzip-1 setting: ${mb(defaultCompression.standalone_pac_bytes)} MB vs ${mb(defaultCompression.jsonl_hourly_bytes)} MB.</p><details class="seed-evidence" open>`)
 .replace('</details>','<p>The middle stage uses uncompressed Seed Packs: repeated values are shared before gzip is applied. Its measured size includes definitions and packet headers.</p></details>');

export function bench(storageSection) {
 const selected=0;
 const names=['Performance','Storage','Transit'];
 const panels=[comparison,storageSection.replace('id="storage"','id="bench-storage"'),transit];
 return `<div class="bench-content">
  <div class="bench-heading wrap"><h2 id="bench-title">Bench</h2>${logBench}</div>
  <div class="bench-notebook wrap">
   <div class="bench-tabs" role="tablist" aria-label="Bench Views" aria-orientation="vertical">${names.map((name,i)=>`<button type="button" role="tab" id="bench-tab-${i}" aria-controls="bench-panel-${i}" aria-selected="${i===selected}" tabindex="${i===selected?0:-1}" data-hero-group="bench" data-hero-index="${i}"><span>${name}</span></button>`).join('')}</div>
   <div class="bench-panels">${panels.map((content,i)=>`<div class="bench-panel bench-${names[i].toLowerCase()}" id="bench-panel-${i}" data-bench-panel role="tabpanel" aria-labelledby="bench-tab-${i}" tabindex="0"${i===selected?'':' hidden'}>${content}</div>`).join('')}</div>
  </div>
 </div>`;
}
