#!/usr/bin/env python3
import json,pathlib,statistics as st,sys
root=pathlib.Path(sys.argv[1]);reports={}
for sync in ['default','writethrough']:
 p=root/('' if sync=='default' else sync)/'results.json'
 r=json.loads(p.read_text());assert r['representation']=='native-pil-http-stream-v1' and len(r['results'])==54 and len(r['reconnect'])==12,p
 assert all(x['updates']==112 for x in r['results'])
 for x in r['results']:
  if x['variant'].startswith('trunk-'):assert x['durable_before_publish']==(x['variant']=='trunk-durable')
 reports[sync]=r
out=[]
def add(s=''):out.append(s)
def table(headers,rs):
 add('| '+' | '.join(headers)+' |');add('| '+' | '.join(['---']*len(headers))+' |')
 for r in rs:add('| '+' | '.join(map(str,r))+' |')
 add()
def rows(sync,size,fan,v):
 r=[x for x in reports[sync]['results'] if (x['description_bytes'],x['subscribers'],x['variant'])==(size,fan,v)];assert len(r)==3;return r
def m(rs,*keys):
 a=[]
 for r in rs:
  for k in keys:r=r[k]
  a.append(r)
 return st.median(a)
add('''# Publishing before durable acknowledgement: binary PIL

Date: 2026-10-18. Requested comparison: full PIL directly from PostgreSQL versus partial PIL through Trie-Trunk, with durable-before-publish=true and false. No base64 or text transport.

## What changed and what did not

The benchmark's `-durable-before-publish=false` path publishes at the real store's accepted-batch boundary, after validation, swap append and in-memory staging, **before** `swap.Sync()`. An isolated Go build overlay adds that observation point; production source and deployed settings are unchanged. There is not yet a deployed subscription publisher setting to flip.

The writer's HTTP response still waits for real durable staging. Publication and acknowledgement are separate: the sender retains the original revision until acknowledgement succeeds. The benchmark waits for both delivery and durability before producing the next update, bounding outstanding memory. This measures latency overlap, not a higher sustainable ingest rate or a durable asynchronous queue implementation.

A staged-observer build is required; a normal build rejects early publication rather than silently publishing from the origin before Trie-Trunk receives the bytes. The durable path reuses the acknowledged revision as in the previous experiment. This contrast includes the difference between publication inside the receiving server and publication after the writer receives its acknowledgement.

## Controls

- Real PostgreSQL UPDATE ... RETURNING; full PIL reads all columns once per update, partial publisher already knows the committed mutation and returns object/revision identity. No per-subscriber SQL query.
- Cached Definition and valid initial client object. Two changed attributes, 256/4096-byte stable descriptions, 1/10/100 subscribers, three rotated rounds, 128 revisions with 16 warmups.
- Current 4 KiB ingress reader throughout. Default PG open_datasync and Mac fsync_writethrough controls. Total 108 cases; 112 measured updates per case. Local Go HTTP/1.1 loopback, no browser/QUIC/WAN measurements and no HTTP compression in this matrix.
- All subscribers apply and verify every state. The replay cache does not reconstruct full objects. Times below are medians of three per-round percentiles, not a pooled distribution.
- Ingress still includes HTTP, validation, journal write, sync and acknowledgement. Turning off durable-before-publish does not turn off persistence.
''')
add('## Delivery latency after origin commit acknowledgement\n\nMilliseconds. This isolates downstream work after PostgreSQL acknowledges the committed mutation.\n')
t=[]
for sync in ['default','writethrough']:
 for size in [256,4096]:
  for fan in [1,10,100]:
   a=[rows(sync,size,fan,v) for v in ['pg-pil','trunk-durable','trunk-direct']]
   t.append([sync,size,fan,*[f'{m(r,"commit_to_apply","p50_us")/1000:.3f}' for r in a],f'{m(a[2],"commit_to_apply","p95_us")/1000:.3f}',f'{m(a[2],"ingress","p50_us")/1000:.3f}'])
table(['PG sync','Description','Subscribers','PG full PIL p50','Trunk durable p50','Trunk early p50','Trunk early p95','Early-path durable ACK p50'],t)
add('## Full write-to-application latency\n\nIncludes the actual PostgreSQL write, so the origin sync method remains visible. Milliseconds.\n')
t=[]
for sync in ['default','writethrough']:
 for size in [256,4096]:
  for fan in [1,100]:
   t.append([sync,size,fan,*[f'{m(rows(sync,size,fan,v),"write_to_apply","p50_us")/1000:.3f}' for v in ['pg-pil','trunk-durable','trunk-direct']]])
table(['PG sync','Description','Subscribers','PG full PIL','Trunk durable','Trunk early'],t)
add('## Raw bytes and the base64 correction\n\nByte sizes are directly comparable for the same fixtures. Timing differences between the previous runs are not a controlled measure of base64 CPU alone: different framing/parser code and run-to-run scheduling are involved.\n')
old=json.loads((root.parent/'2026-10-18-sse/buffer4k.json').read_text())
t=[]
for size in [256,4096]:
 for v in ['pg-pil','trunk-direct']:
  prev=[x for x in old['results'] if (x['description_bytes'],x['subscribers'],x['variant'])==(size,1,v)]
  before=m(prev,'sse_bytes_per_update_per_subscriber');after=m(rows('default',size,1,v),'body_bytes_per_update_per_subscriber')
  t.append([size,'Full PIL' if v=='pg-pil' else 'Partial PIL',f'{before:.1f}',f'{after:.1f}',f'{100*(1-after/before):.1f}%'])
table(['Description','Payload','Old base64 + text envelope','Native PIL','Bytes removed'],t)
add('''The correction benefits both architectures. It removes base64's roughly one-third expansion and the textual per-event envelope; for a tiny patch the envelope was a large fraction of the total. It does not improve stored bytes, remove an HTTP hop, or remove swap synchronization. Compression also changes because the compressor now sees native byte patterns: gzip/Brotli results for a text wrapper cannot substitute for native PIL results.

The [corrected raw compression matrix](warm-compression/REPORT.md) already showed 32-object/4 KiB warm-stream averages of 29.26 bytes for full PIL with Brotli 4 versus 13.85 bytes for partial PIL with gzip 6. The raw 4,162-to-26 byte advantage is therefore not the same as the compressed advantage. These compare different codecs intentionally and exclude initial state, context setup and HTTP overhead.
''')
add('## Reconnect: current snapshot versus missing revisions\n\nDefault PG sync; 30 measured requests after five warmups per case. The replay tests run after every outstanding durable acknowledgement has completed. A snapshot and an ordered revision history provide different information.\n')
t=[]
for r in reports['default']['reconnect']:
 t.append([r['description_bytes'],r['missing_revisions'],r['variant'],r['body_bytes'],f'{r["request_to_applied"]["p50_us"]:.1f}',f'{r["request_to_applied"]["p95_us"]:.1f}'])
table(['Description','Missing revisions','Path','Body bytes','Request→applied p50 µs','p95 µs'],t)
add('''## Failure and production limitations

The gated test proves a subscriber can apply a revision while sync is blocked, and that no durable writer acknowledgement is issued until the gate opens. An injected error at that boundary is returned to the writer; retaining and retrying the same frame deduplicates to one stored revision. This is a publication-order/failure-propagation test, not a power-loss test or injected filesystem fsync failure.

Automatic subscriber invalidation on persistence failure is not implemented by this benchmark. Production early publication needs separate delivered/durable positions, reliable retry ownership, bounded queues and a reconnect fallback when continuity is unknown. A process crash cannot send an invalidation after it dies. The benchmark sender retains bytes in memory; it is not a crash-safe outbox.

The store still holds its writer mutex through synchronization. Early fanout can proceed, but a concurrent history lookup may still wait for that mutex. This test does not establish nonblocking replay under sustained concurrent ingress. There is no disk-free ingest claim: the swap append precedes publication, and only the durable sync is removed from the delivery dependency.

## Compression on the wire versus in .pop

Compressing a substantial batch or persistent PIL stream can reduce transmission bytes. Independently compressing each tiny patch can increase size. The [fresh-response measurement](fresh-compression/REPORT.md) tests each frame with a new, finalized compressor context rather than borrowing warm history.

A continuous network compressor output is not automatically a useful `.pop` layout. Existing index references locate event byte ranges; a compressed format needs independently decodable blocks and metadata to resolve a record inside a decompressed block. The cold-read tradeoff is fewer bytes read versus extra decompression and reading unrelated records in the block. Keep tiny/noncompressible blocks raw when total compressed storage is larger.

If incoming data already uses the exact independently compressed block layout accepted by storage, retaining those bytes could avoid recompression after validation. Arbitrary network stream chunks are not such blocks. Subset queries may still require decompression and regrouping. No compressed `.pop` format, disk savings, or random-read latency has been implemented or measured here.

These constraints follow the streaming codecs' history/flush behavior; see the [zlib manual](https://zlib.net/manual.html) and [Brotli encoder API](https://www.brotli.org/encode.html). A sync flush permits incremental consumption but does not generally make each flushed chunk independently decodable.

## Reproduce

```sh
GOCACHE=/private/tmp/trie-trunk-go-cache bash benchmarks/replay/postgres/run-publication.sh /path/to/fresh-results
PG_SYNC_METHOD=fsync_writethrough GOCACHE=/private/tmp/trie-trunk-go-cache bash benchmarks/replay/postgres/run-publication.sh /path/to/fresh-results/writethrough
GOCACHE=/private/tmp/trie-trunk-go-cache python3 benchmarks/replay/compression/run.py /path/to/fresh-results/fresh-compression --context response
python3 benchmarks/replay/compression/report_response.py /path/to/fresh-results/fresh-compression
python3 benchmarks/replay/postgres/report-publication.py /path/to/fresh-results
```

The runner includes race tests before measurement and stores its source overlay, hashes, raw results and PostgreSQL logs. Only temporary test clusters and fixtures are created; production acknowledgement semantics remain unchanged.
''')
(root/'REPORT.md').write_text('\n'.join(out));print(root/'REPORT.md')
