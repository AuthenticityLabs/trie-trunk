#!/usr/bin/env python3
"""Report only complete, explicitly tagged native PIL HTTP-stream measurements."""
import csv,json,pathlib,statistics as st,sys
root=pathlib.Path(sys.argv[1]); reports={}
for sync in ['default','writethrough']:
 for build in ['baseline','buffer4k']:
  p=root/('' if sync=='default' else sync)/(build+'.json')
  r=json.loads(p.read_text())
  assert r['representation']=='native-pil-http-stream-v1',p
  assert len(r['results'])==90 and len(r['reconnect'])==12,p
  assert all(x['updates']==112 for x in r['results']),p
  reports[sync,build]=r
out=[]
def add(s=''):out.append(s)
def rows(sync,build,size,fanout,variant):
 r=[x for x in reports[sync,build]['results'] if (x['description_bytes'],x['subscribers'],x['variant'])==(size,fanout,variant)]
 assert len(r)==3
 return r
def med(rs,*keys):
 v=[]
 for r in rs:
  for k in keys:r=r[k]
  v.append(r)
 return st.median(v)
def table(headers,rs):
 add('| '+' | '.join(headers)+' |');add('| '+' | '.join(['---']*len(headers))+' |')
 for r in rs:add('| '+' | '.join(map(str,r))+' |')
 add()
add('''# Corrected binary PIL delivery and replay

Date: 2026-10-18. This replaces the base64/text-SSE delivery measurements for architectural decisions. [Audit and current results](../RESULTS-AUDIT.md).

## What changed

Full PostgreSQL PIL objects and partial Trie-Trunk PIL revisions now travel as native, self-delimiting PIL frames over persistent HTTP. No base64, text event envelopes, JSON conversion of PIL, or additional per-record length/cursor prefix. The subscriber uses the existing Definition and `Layout.ReadFrame`, then applies the decoded fields. Revision identity is read from the PIL object. Reconnect responses use the same native path.

The standalone JSON control remains actual JSON, delivered as newline-delimited objects. It is not used as a representation of PIL. These results do not compare PIL-in-text against JSON.

## Method and scope

- Actual PostgreSQL committed UPDATE ... RETURNING, one fetch/encode per update shared across subscribers. Full PIL returns all fields; the known-patch publisher returns object/revision identity and sends the committed patch to Trie-Trunk.
- Five paths: full JSON control, full PIL, partial PIL after history lookup, partial PIL reusing the acknowledged frame, and eight individually committed patches per durable ingress batch.
- Two stable description sizes (256/4096 bytes), 1/10/100 subscribers, three rotated rounds, 128 revisions per round with 16 warmups. Each change modifies two attributes. Each subscriber starts with a valid initial object and a cached Definition, outside timing.
- Two ingress buffer builds (128 KiB/4 KiB), two PostgreSQL sync settings: open_datasync and fsync_writethrough. 360 cases, each with 112 measured updates. This is a local Mac benchmark over loopback HTTP/1.1, not browser/SharedWorker, HTTP/3, WAN or cloud performance.
- No HTTP compression in this end-to-end run. Raw-PIL gzip/Brotli costs are measured separately in the [binary compression report](compression/REPORT.md). Do not add medians from different tests to claim measured compressed end-to-end latency.
- Trie-Trunk ingress remains durable-before-publish. Ingress time includes HTTP, validation, swap append/sync and acknowledgement; it is not a standalone fsync timer. The 15-minute permanent-file staging window does not delay replay visibility.
- Publisher waits for all subscribers after each update/batch. This is bounded-load latency, not saturation throughput. The Go clients apply patches and verify every resulting object. The replay cache never reconstructs a full object server-side.
- Body bytes exclude HTTP/TCP headers, definitions and initial state. Times below are medians of three per-round statistics, not pooled percentiles. CPU/allocations include Go publisher, local subscribers and verification, but exclude PostgreSQL's process.
''')
add('## Uncompressed delivery size\n')
t=[]
for size in [256,4096]:
 r=[rows('default','buffer4k',size,1,v) for v in ['pg-json','pg-pil','trunk-direct']]
 vals=[med(x,'body_bytes_per_update_per_subscriber') for x in r]
 t.append([size,*[f'{v:.1f}' for v in vals],f'{100*(1-vals[2]/vals[1]):.2f}%'])
table(['Stable description bytes','Full JSON bytes/update','Full PIL bytes/update','Partial PIL bytes/update','Partial reduction vs full PIL'],t)
add('## Origin acknowledgement → subscriber application\n\nCurrent 4 KiB ingress buffer; milliseconds. Includes durable Trie-Trunk ingress where used.\n')
t=[]
for sync in ['default','writethrough']:
 for size in [256,4096]:
  for fan in [1,10,100]:
   for v in ['pg-pil','trunk-direct']:
    r=rows(sync,'buffer4k',size,fan,v)
    t.append([sync,size,fan,v,*[f'{med(r,*keys)/1000:.3f}' for keys in [('commit_to_apply','p50_us'),('commit_to_apply','p95_us'),('write_to_apply','p50_us'),('publish_to_apply','p50_us'),('ingress','p50_us')]]])
table(['PG sync','Description','Subscribers','Path','Commit→apply p50 ms','p95 ms','Write→apply p50 ms','Publish→apply p50 ms','Ingress p50 ms'],t)
add('## Publication strategies and buffer cost\n\n256-byte description, one subscriber, default PG sync. All figures include the same binary transport. Batch-8 waits for eight origin commits before publication; reduced amortized cost is not free latency.\n')
t=[]
for build in ['baseline','buffer4k']:
 for v in ['pg-json','pg-pil','trunk-lookup','trunk-direct','trunk-batch8']:
  r=rows('default',build,256,1,v)
  t.append([build,v,f'{med(r,"commit_to_apply","p50_us")/1000:.3f}',f'{med(r,"go_bytes_per_update"):.0f}',f'{med(r,"go_allocs_per_update"):.0f}',f'{med(r,"combined_go_cpu_us_per_update"):.1f}'])
table(['Build','Path','Commit→apply p50 ms','Go bytes/update','Go allocs/update','Combined Go CPU µs/update'],t)
add('## Reconnect: missing revisions versus latest full snapshot\n\nCurrent 4 KiB buffer, default PG sync. Each case has 30 measured requests after five warmups; measured once per description, not three rotated repeats. The client already has the base revision. Replay preserves intermediate changes; a latest snapshot does not.\n')
t=[]
for r in reports['default','buffer4k']['reconnect']:
 t.append([r['description_bytes'],r['missing_revisions'],r['variant'],r['body_bytes'],f'{r["request_to_applied"]["p50_us"]:.1f}',f'{r["request_to_applied"]["p95_us"]:.1f}'])
table(['Description','Missing revisions','Path','Response body bytes','Request→applied p50 µs','p95 µs'],t)
add('''## Interpretation

Use partial replay for history and reconnect semantics, reduced payload size and local reuse; these measurements do not establish faster initial publication than the origin. Waiting for a second durable acknowledgement remains substantial on this machine. Publishing before durable acknowledgement is a different policy and has not been benchmarked or enabled here.

Full and partial PIL are now compared without a text handicap. Large stable fields create a large raw-byte advantage for patches; persistent compression can substantially narrow that gap. A long replay can exceed a current snapshot in cost, but returns different information. Definition caching and initial object state remain prerequisites, not free features of a patch.

The raw JSON artifacts retain every fanout/path, timing percentile, origin/ingress distribution, allocation and CPU measurement. Source and binary hashes, PostgreSQL settings and run logs are retained alongside them. No storage-format change or production subscription endpoint was introduced.

## Reproduce

```sh
GOCACHE=/private/tmp/trie-trunk-go-cache bash benchmarks/replay/postgres/run-stream.sh /path/to/fresh-results
PG_SYNC_METHOD=fsync_writethrough GOCACHE=/private/tmp/trie-trunk-go-cache bash benchmarks/replay/postgres/run-stream.sh /path/to/fresh-results/writethrough
python3 benchmarks/replay/postgres/report-stream.py /path/to/fresh-results
```

The runner creates and deletes only its own temporary PostgreSQL cluster and Trie-Trunk fixtures. Tests cover raw payload equality, partial application, fragmented/coalesced frames, rejection at every truncated full-frame boundary, immutable fanout packets and slow-queue failure. Run matrices sequentially to avoid benchmark contention.
''')
(root/'REPORT.md').write_text('\n'.join(out))
flat=[]
for (sync,build),r in reports.items():
 for row in r['results']:
  item={'pg_sync':sync,'build':build}
  for k,v in row.items():
   if isinstance(v,dict):item.update({k+'_'+a:b for a,b in v.items()})
   else:item[k]=v
  flat.append(item)
with (root/'summary.csv').open('w') as f:
 w=csv.DictWriter(f,fieldnames=list(flat[0]));w.writeheader();w.writerows(flat)
print(root/'REPORT.md')
