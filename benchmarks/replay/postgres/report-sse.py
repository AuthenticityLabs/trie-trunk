#!/usr/bin/env python3
"""Historical text-SSE report only. Use report-stream.py for native PIL."""
import csv
import json
import pathlib
import re
import statistics as st
import sys

root = pathlib.Path(sys.argv[1])
reports = {}
for setting in ('default', 'writethrough'):
    for mode in ('baseline', 'buffer4k'):
        path = root / ('' if setting == 'default' else setting) / (mode + '.json')
        report = json.loads(path.read_text())
        assert len(report['results']) == 90 and len(report['reconnect']) == 12, path
        assert all(r['updates'] == 112 for r in report['results']), path
        reports[setting, mode] = report
compression = json.loads((root/'compression.json').read_text())
assert len(compression) == 36
buffer_rows = {}
for line in (root/'buffer-bench.txt').read_text().splitlines():
    match = re.search(r'payload(\d+)/frames(\d+)/buffer(\d+)-\d+\s+\d+\s+([\d.]+) ns/op.*?([\d.]+) B/op', line)
    if match:
        key = tuple(map(int, match.group(1,2,3)))
        buffer_rows.setdefault(key, []).append(tuple(map(float, match.group(4,5))))
assert len(buffer_rows) == 8 and all(len(v)==5 for v in buffer_rows.values())


def rows(setting, mode, size, fanout, variant):
    found = [r for r in reports[setting, mode]['results'] if (r['description_bytes'], r['subscribers'], r['variant']) == (size, fanout, variant)]
    assert len(found) == 3
    return found

def med(rs, *keys):
    values = []
    for r in rs:
        for k in keys:
            r = r[k]
        values.append(r)
    return st.median(values)

out = []
def add(s=''): out.append(s)
def table(headers, data):
    add('| ' + ' | '.join(headers) + ' |')
    add('| ' + ' | '.join(['---']*len(headers)) + ' |')
    for row in data: add('| ' + ' | '.join(map(str,row)) + ' |')
    add()

add('''# Persistent SSE: full PostgreSQL objects versus Trie-Trunk revisions

> Superseded for native PIL evaluation. Use the [corrected binary delivery report](../2026-10-18-binary-stream/REPORT.md) and [audit](../RESULTS-AUDIT.md). These historical numbers used base64-wrapped PIL.

Date: 2026-10-18. Local Mac measurements, not a deployment or cloud cost estimate.

Partial revisions substantially reduce uncompressed delivery bytes. They do **not** make a newly committed change reach the client sooner in this experiment: durable replication into Trie-Trunk adds a second storage acknowledgement. Reconnects with a small revision gap benefit from the live in-memory reader; a long backlog can cost more than a fresh origin snapshot.

Applied a measured ingress optimization that reduces the request buffer from 128 KiB to 4 KiB. The before/after matrix holds all other production code constant. Reusing acknowledged frames and batching ingress are benchmark publisher variants, not a newly deployed subscription service.

## Scope and fairness

- Persistent, real HTTP SSE connections, 1/10/100 subscribers; two fixtures with 11 business fields and unchanged descriptions of 256 or 4,096 bytes. Each change modifies two attributes. PIL also carries event identity. Every subscriber already has the same valid initial object.
- PostgreSQL executes an actual committed `UPDATE ... RETURNING` once per change. Full-object variants return all columns; the Trie-Trunk publisher already knows the committed patch and returns only object/revision identity. This models application-managed publication, not a CDC implementation. An origin outbox/CDC integration would still be required for reliable production publication across an origin-process crash.
- PostgreSQL fetches/encodes each full object **once**, then shares the same packet across subscribers. Trie-Trunk likewise prepares one revision packet. There is no per-subscriber SQL query strawman.
- `pg-json`: full JSON object. `pg-pil`: full PIL object, controlling for codec. `trunk-lookup`: partial PIL via real durable HTTP ingress, then an in-process history lookup. `trunk-direct`: same ingress/ack, reuse the exact acknowledged frame. `trunk-batch8`: eight distinct PG commits, one durable ingress batch, eight individual SSE messages.
- Literal SSE is UTF-8. PIL is base64 encoded; all SSE body sizes include base64 and id/event/data framing. Definition and initial-object delivery are outside the measurement. HTTP/TCP/TLS overhead is excluded.
- Three rotated rounds per case, 128 revisions, first 16 warm; 112 measured updates per round. Tables report **median of the three per-round statistics**, not a pooled percentile. The publisher waits for all clients to apply each update/batch: this measures latency under bounded load, **not saturation throughput**. Per-client application timestamps precede correctness checking.
- Every applied state must equal the expected state; explicit NULL and omitted fields are covered. Replay reconstructs client state only. No full-object Trie-Trunk endpoint was added.

## Delivery size

Uncompressed bytes per update per subscriber, averaged over measured revisions. Replication is one binary PIL body sent to Trie-Trunk per update, independent of subscriber count.
''')
data=[]
for size in (256,4096):
    rs=[rows('default','buffer4k',size,1,v) for v in ('pg-json','pg-pil','trunk-direct')]
    js,pi,tr=[med(r,'sse_bytes_per_update_per_subscriber') for r in rs]
    data.append([size,f'{js:.1f}',f'{pi:.1f}',f'{tr:.1f}',f'{100*(1-tr/js):.1f}%',f"{med(rs[2],'replication_body_bytes_per_update'):.1f}"])
table(['Description bytes','Full JSON SSE','Full PIL SSE','Partial PIL SSE','Reduction vs JSON','Upstream PIL body'], data)
add('''These are not billable egress ratios. Streaming compression can materially change them, as measured below. The PIL full-object/base64 control can be larger than JSON for long strings; the major raw-byte benefit here is sending changed fields, not renaming JSON to PIL.

## Live delivery latency

Optimized 4 KiB ingress build, PostgreSQL default `open_datasync`. Times in milliseconds. “Commit → apply” starts after the origin database acknowledgement. “Publish → apply” starts after payload preparation and durable Trie-Trunk acknowledgement. The latter isolates fanout/network/client work; it is not end-to-end latency.
''')
data=[]
for size in (256,4096):
    for fan in (1,10,100):
        for v in ('pg-json','pg-pil','trunk-direct'):
            r=rows('default','buffer4k',size,fan,v)
            data.append([size,fan,v,*[f"{med(r,*k)/1000:.3f}" for k in [('commit_to_apply','p50_us'),('commit_to_apply','p95_us'),('write_to_apply','p50_us'),('publish_to_apply','p50_us')]]])
table(['Description','Subscribers','Path','Commit → apply p50','p95','Write → apply p50','Publish → apply p50'],data)
add('''The pending revisions are queryable before disk promotion. These events remain inside the staging window throughout the test; the reader has no 15-minute visibility delay. Durable acknowledgement still entails writing and synchronizing swap. That is separate from permanent `.idx`/`.pop` promotion.

## Mac durability control

Both runs use `fsync=on`, `synchronous_commit=on`, and `full_page_writes=on`. The default PG run uses `open_datasync`; Go 1.26.4 `os.File.Sync` on Darwin invokes `F_FULLFSYNC` (with an unsupported-filesystem fallback). A second full matrix uses PostgreSQL `fsync_writethrough` to compare with write-through flushing. This does not claim identical WAL and swap implementations or establish power-failure guarantees for the hardware.

The [PostgreSQL WAL documentation](https://www.postgresql.org/docs/16/runtime-config-wal.html#GUC-WAL-SYNC-METHOD) distinguishes these sync methods. Go source inspected: `src/internal/poll/fd_fsync_darwin.go`. No acknowledgement was weakened to improve scores.

Below: 256-byte description, one subscriber, optimized buffer; median latency in milliseconds.
''')
data=[]
for setting in ('default','writethrough'):
    for v in ('pg-json','pg-pil','trunk-direct','trunk-batch8'):
        r=rows(setting,'buffer4k',256,1,v)
        data.append([setting,v,*[f'{med(r,*k)/1000:.3f}' for k in [('origin_write','p50_us'),('ingress','p50_us'),('commit_to_apply','p50_us'),('write_to_apply','p50_us')]]])
table(['PG sync run','Path','Origin write','Ingress request','Commit → apply','Write → apply'],data)
add('''Trie-Trunk has an additional durable write even when PostgreSQL uses write-through. The batching test waits for eight *independent* origin commits before posting. With slower commits, that wait dominates the first revisions. It is evidence for bounded batch delay, not for always waiting for eight changes. Batch ingress latency is per **request**, not per event; dividing it by eight describes amortized service work only.

## Optimization assessment

### Smaller ingress buffer

For one subscriber and the 256-byte fixture; process allocations per update include publisher, clients and harness. The controlled change is the input buffer only.
''')
data=[]
for v in ('trunk-lookup','trunk-direct','trunk-batch8'):
    a=rows('default','baseline',256,1,v);b=rows('default','buffer4k',256,1,v)
    data.append([v,f"{med(a,'go_bytes_per_update')/1024:.1f}",f"{med(b,'go_bytes_per_update')/1024:.1f}",f"{med(a,'ingress','p50_us')/1000:.3f}",f"{med(b,'ingress','p50_us')/1000:.3f}"])
table(['Path','Before KiB/update','After KiB/update','Before ingress ms','After ingress ms'],data)
add('''The buffer allocation itself falls exactly 124 KiB per request. Total allocation changes also include GC, transport and scheduling variation. The reliable win is less allocation, not a demonstrated durable-ingress latency improvement. Large frames still stream correctly; a 264,000-byte field followed by a sparse frame is tested with fragmented input. `buffer-bench.txt` measures the isolated frame reader for small and large batches without disk synchronization.

''')
data=[]
for size in (32,262144):
    for count in (1,128):
        a=buffer_rows[size,count,131072];b=buffer_rows[size,count,4096]
        data.append([size,count,f'{st.median(x[0] for x in a)/1000:.2f}',f'{st.median(x[0] for x in b)/1000:.2f}',f'{st.median(x[1] for x in a)/1024:.1f}',f'{st.median(x[1] for x in b)/1024:.1f}'])
table(['Field bytes','Frames/request','128 KiB buffer µs','4 KiB buffer µs','Before KiB/request','After KiB/request'],data)
add('''Five measurements per case. The isolated reader improves for all four tested shapes, including 128 large frames. The large 128-frame case deliberately isolates reader behavior across 32 MiB; actual HTTP ingress stages batches at 4 MiB or 1,024 frames. The smallest request falls from about 9.7 µs to 0.73 µs; end-to-end ingress remains dominated by durable sync. The 4 KiB change is applied in production source, with no change to acknowledgement or stored format. It has not been deployed.

### Reuse and batching

Reuse the already acknowledged immutable PIL frame for live fanout when implementing the publisher; history lookup remains necessary for reconnect. The 128-update test does not demonstrate a consistent material end-to-end speedup from avoiding that lookup because durable sync dominates. Do not remove the history path based on this experiment.

Existing ingress accepts batches and syncs once per batch. The eight-event test reduces measured ingress requests from 112 to 14 per round. A production publisher needs a bounded timer/size policy and explicit slow-subscriber handling; an unconditional count of eight penalizes sparse streams. No buffering policy or new production subscription hook is introduced here.

### Streaming compression control

Prepared SSE packets, same fixtures, persistent gzip context, flush after **every** event. Three rounds of 2,048 events, first 16 warm. The larger revision numbers make these uncompressed averages slightly larger than the 128-event live matrix. This is an isolated compression experiment, not measured compressed network latency. Output is decompressed and verified byte-for-byte. CPU column is wall time for compression into a preallocated buffer per event; it excludes codec/SQL/client work and does not establish multi-subscriber server CPU cost.
''')
data=[]
for size in (256,4096):
    for v in ('pg-json','pg-pil','trunk-direct'):
        for level in (1,-1):
            rr=[r for r in compression if (r['description_bytes'],r['variant'],r['gzip_level'])==(size,v,level)]
            assert len(rr)==3
            data.append([size,v,'fast' if level==1 else 'default',f"{med(rr,'raw_bytes_per_update'):.1f}",f"{med(rr,'gzip_bytes_per_update'):.1f}",f"{med(rr,'compression_us_per_update'):.2f}"])
table(['Description','Path','gzip','Raw B/event','gzip B/event','Compression µs/event'],data)
add('''Unchanged fields in a persistent full-object stream are highly compressible. Compare compressed with compressed before predicting egress savings. This fixture repeatedly updates one object; interleaved unrelated objects, context resets and high-entropy changes can produce different results. A gzip context belongs to a stream, so sharing uncompressed packets does not automatically share compression work across subscribers. Reverse-proxy buffering/flush behavior is untested.

## Reconnect: missing revisions versus a fresh origin object

Default PG run, optimized buffer. New HTTP request with a resumable SSE-style cursor; one client already holds a trusted base. Each distribution contains 30 requests after five warmups. PostgreSQL returns one complete current PIL object; Trie-Trunk sends every missing revision. Both end in the same client state, but only the latter preserves intervening changes. The fixture maps revision to a known timestamp for the current history API; this is not a production cursor specification.
''')
data=[]
for r in reports['default','buffer4k']['reconnect']:
    data.append([r['description_bytes'],r['missing_revisions'],r['variant'],r['sse_body_bytes'],f"{r['request_to_applied']['p50_us']:.1f}",f"{r['request_to_applied']['p95_us']:.1f}"])
table(['Description','Missing revisions','Path','SSE bytes','p50 µs','p95 µs'],data)
add('''A fresh snapshot can be cheaper for a long disconnect when the consumer only needs current state. That does not make it equivalent to lossless revision replay. Neither path can safely apply patches without a valid base and continuity; invalidation/deletion/gap recovery remain explicit application requirements.

## Limits and verification

- Apple M1 Max, 32 GiB; Go/PostgreSQL exact versions and PG settings recorded in JSON. All fixtures live in a temporary isolated cluster/cache and are deleted afterward. No production data, ports, or deployment settings were changed.
- Single object per live stream, loopback HTTP/1.1, no TLS/WAN/edge/proxy/CDC. Subscriber processes share the publisher's Go runtime. No production throughput, AWS/Cloudflare cost, large-object-count lookup scaling or RSS claim follows from this matrix.
- `combined_go_cpu_us_per_update`, allocations and total Go bytes include the benchmark subscriber and correctness harness, exclude the separate PostgreSQL process, and exclude isolated fixture creation. They are useful paired diagnostics, not total system resource costs. Raw p99s, all-client completion times, and SQL result-field byte counts remain in JSON/CSV.
- SQL result bytes count pgx field payloads, not protocol packets. Upstream replication body size excludes request/response headers; one HTTP request per revision incurs overhead beyond the roughly 26-byte average PIL frame. Persistent/batched ingress can amortize that overhead.
- All 360 matrix cases complete, each applying the same revisions to every subscriber. The default-sync run completed all measurements but its runner failed afterward during log archival after an in-flight script edit; both JSON files were checked for the complete matrix. The separate write-through run completed normally. Its run logs and database log are retained. No incomplete timing cases were substituted.
- Passed the SSE harness race tests, full cache race suite and HTTP/2, HTTP/3, socket and CLI integration tests. Coverage includes atomic SSE framing, truncation, shared immutable packets, slow-queue refusal, large/fragmented ingress, live history, promotion, restart warming, 24-hour expiry, SIGKILL staging recovery and snapshot continuity. Outputs are retained as `harness-race.txt`, `cache-race.txt`, and `transport-tests.txt`.
- Storage formats and stored bytes are unchanged by this turn. The memory-allocation optimization is not a storage compression change. No server-side full-object materialization was introduced.

## Next decisions

1. Keep the applied 4 KiB ingress buffer and existing acknowledgement durability; it reduces allocation with no regression in these frame-reader cases.
2. For live publication, share prepared immutable frames and apply bounded batching only where acceptable delivery delay is specified. Do not fetch/encode once per subscriber.
3. Select stream compression from measured payloads and choose snapshot versus replay based on whether the consumer needs current state or every transition. Do not infer a universal latency/egress win from the uncompressed fixture.

## Reproduce

```sh
GOCACHE=/private/tmp/trie-trunk-go-cache bash benchmarks/replay/postgres/run-sse.sh /path/to/results
PG_SYNC_METHOD=fsync_writethrough GOCACHE=/private/tmp/trie-trunk-go-cache \\
  bash benchmarks/replay/postgres/run-sse.sh /path/to/results/writethrough
(cd benchmarks/replay/postgres && go run . -sse-compression -output /path/to/results/compression.json)
(cd go && go test ./server/cache -run '^$' -bench '^BenchmarkIngressFrameBuffer$' -benchmem -count=5 > /path/to/results/buffer-bench.txt)
python3 benchmarks/replay/postgres/report-sse.py /path/to/results
```

`baseline` is the 128 KiB input-buffer build; `buffer4k` changes only that buffer using Go build overlays. The runner records hashes of the exact binaries. Test suites are run separately from the measured matrices. Default matrix dimensions are required by this report generator; environment overrides are intended for smoke tests.
''')
(root/'REPORT.md').write_text('\n'.join(out))
with (root/'summary.csv').open('w') as f:
    writer=csv.writer(f)
    writer.writerow(['pg_sync','build','round','text_bytes','subscribers','variant','commit_apply_p50_us','commit_apply_p95_us','commit_apply_p99_us','write_apply_p50_us','publish_apply_p50_us','ingress_p50_us','go_bytes_per_update','go_allocs_per_update','combined_go_cpu_us_per_update','sse_bytes_per_update'])
    for (setting,mode),report in reports.items():
        for r in report['results']:
            writer.writerow([setting,mode,r['round'],r['description_bytes'],r['subscribers'],r['variant'],r['commit_to_apply']['p50_us'],r['commit_to_apply']['p95_us'],r['commit_to_apply']['p99_us'],r['write_to_apply']['p50_us'],r['publish_to_apply']['p50_us'],r['ingress']['p50_us'],r['go_bytes_per_update'],r['go_allocs_per_update'],r['combined_go_cpu_us_per_update'],r['sse_bytes_per_update_per_subscriber']])
print(root/'REPORT.md')
