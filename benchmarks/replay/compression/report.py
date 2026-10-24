#!/usr/bin/env python3
import csv,json,pathlib,statistics as st,sys
# Historical text-SSE artifact renderer; native results use report_binary.py.
root=pathlib.Path(sys.argv[1]);data=json.loads((root/'results.json').read_text())
assert len(data)==300
out=[]
def add(s=''):out.append(s)
def rows(obj,size,variant,codec,level):
 r=[v for v in data if (v['objects'],v['description_bytes'],v['variant'],v['codec'],v['level'])==(obj,size,variant,codec,level)]
 assert len(r)==5
 return r
def m(r,k):return st.median(v[k] for v in r)
def table(head,lines):
 add('| '+' | '.join(head)+' |');add('| '+' | '.join(['---']*len(head))+' |')
 for line in lines:add('| '+' | '.join(map(str,line))+' |')
 add()
add('''# Gzip versus Brotli for full-object and partial-revision SSE

> Historical text-SSE experiment: base64-wrapped PIL is not the intended binary transport. See the [direct binary PIL rerun](../2026-10-18-binary-pil-compression/REPORT.md).

Date: 2026-10-18. Compression-only follow-up to the [persistent SSE comparison](../2026-10-18-sse/REPORT.md). No production codec or transport change. Tables corrected to prioritize full PIL; no benchmark rerun or data alteration was needed.

## Assessment

The intended architecture serves full PIL from PostgreSQL for API responses and subscriptions, and partial PIL from Trie-Trunk for revision delivery. Full PIL was measured in both the SSE and compression matrices from the outset; the initial written summary incorrectly centered the JSON control. This report now makes full PIL versus partial PIL the primary comparison, with the original raw measurements unchanged.

Keep object replay as an optional history/reconnect capability. In a warm stream repeatedly updating one object, compressed full PIL can approach the size of partial PIL. With 32 interleaved objects, the measured full-PIL size is materially larger even with Brotli. The compression results therefore do not establish a universal bandwidth argument for or against replay; the relevant workload and history requirements matter.

Replay still preserves intervening mutations, serves short reconnect gaps without querying the origin and can reuse events already retained for telemetry/history. Its costs include event retention, client base/revision tracking, invalidation and the extra durable ingress measured previously. A full origin snapshot is a reasonable alternative when only current state matters or the missing history is long. Trie-Trunk need not reconstruct complete objects.

## Native codec comparison

Five rotated rounds per case; median of round means. Persistent compression/decompression contexts, one flush after every event, immediate decode and exact-byte verification **before the next event**. Each stream has 2,048 events with 64 warmup events. The raw input is the same prepared SSE framing and PIL encoding used by the earlier harness. The base fixture has 11 business fields, an unchanged unique description and two changed attributes per revision.

The comparison uses native zlib and Google's Brotli libraries through C to put both codecs on the same measurement path. Earlier gzip CPU results used Go's standard library; differences between those implementations should not be attributed to the format alone. These are codec-only local timings, not browser, network, PostgreSQL, Go server or end-to-end latency.
''')
for size in (256,4096):
 add(f'### One object; {size:,}-byte unchanged description\n')
 lines=[]
 for variant in ('pg-pil','trunk-direct'):
  for codec,level in [('gzip',1),('gzip',6),('brotli',1),('brotli',4),('brotli',6)]:
   r=rows(1,size,variant,codec,level)
   lines.append(['Full PIL' if variant=='pg-pil' else 'Partial PIL',f'{codec} {level}',f'{m(r,"compressed_bytes"):.2f}',f'{m(r,"encode_mean_us"):.2f}',f'{m(r,"decode_mean_us"):.2f}',f'{m(r,"encode_p95_us"):.2f}'])
 table(['Payload','Codec/level','Bytes/update','Compress µs/update','Decompress µs/update','Compress p95 µs'],lines)
add('''The larger full object shrinks substantially with Brotli 4/6. Tiny partial revisions favor gzip 6 in both size and CPU for this fixture. Level 1 is not a universally better real-time setting: per-event flush behavior matters, and Brotli 1 performs poorly here. Compression levels are not comparable numeric settings across algorithms. Quality 11 was not evaluated because this experiment targets interactive, frequently flushed streams.

## Interleaving 32 objects

Each object has a distinct description and its own revision counter. Updates cycle through all 32 objects in one stream. One stream is not one subscription per object in this control. This tests sensitivity to the compression history window.
''')
lines=[]
for size in (256,4096):
 for v in ('pg-pil','trunk-direct'):
  for c,l in [('gzip',6),('brotli',4),('brotli',6)]:
   r=rows(32,size,v,c,l)
   lines.append([size,'Full PIL' if v=='pg-pil' else 'Partial PIL',f'{c} {l}',f'{m(r,"compressed_bytes"):.2f}',f'{m(r,"encode_mean_us"):.2f}',f'{m(r,"decode_mean_us"):.2f}'])
table(['Description bytes','Payload','Codec','Bytes/update','Compress µs/update','Decompress µs/update'],lines)
add('''Gzip uses a 32 KiB history window; Brotli is configured with `lgwin=22` (approximately 4 MiB). At 32 × 4 KiB of unique descriptions, this deliberately crosses gzip's history capacity while remaining within Brotli's. The result demonstrates working-set sensitivity, not that every multi-object stream will behave this way. A much larger working set can defeat Brotli's reuse as well. The larger history also has memory implications per active stream; context memory was not measured here.

## Startup and reconnection

These are literal SSE measurements with base64-wrapped PIL, not raw binary PIL API responses. An independent API response has a fresh compression context; its performance needs a separate response benchmark.

The warm-stream averages exclude headers and the first 64 events. The first update cannot refer back to a description already sent on that stream. Below are first-update bytes and processing times, for the 4,096-byte description. Compression time excludes context construction, reported separately. These startup figures are only five observations, so treat them as diagnostic.
''')
lines=[]
for v in ('pg-pil','trunk-direct'):
 for c,l in [('gzip',6),('brotli',4),('brotli',6)]:
  r=rows(1,4096,v,c,l)
  lines.append(['Full PIL' if v=='pg-pil' else 'Partial PIL',f'{c} {l}',f'{m(r,"cold_bytes"):.0f}',f'{m(r,"cold_encode_us"):.2f}',f'{m(r,"cold_decode_us"):.2f}',f'{m(r,"context_init_us"):.2f}'])
table(['Payload','Codec','First-update bytes','Compress µs','Decompress µs','Encoder + decoder creation µs'],lines)
add('''The subscriber is assumed to already hold a valid initial object in the partial-revision case. This table does not make a patch a substitute for initial state. A new HTTP stream also starts a new compression context; the tiny steady-state full-object averages do not apply automatically to an isolated GET or a reconnect snapshot.

## What latency compression adds

An approximate budget is:

`compression CPU + buffering/queue delay + network transfer + decompression CPU`

Compression can reduce the transfer term enough to offset its CPU cost, depending on bandwidth and payload size. It cannot remove origin query or durable-ingress latency. In these warm-stream cases codec work is measured in microseconds, versus approximately four milliseconds of additional durable Trie-Trunk ingress in the earlier Mac test. At large fanout, small per-stream costs still add up; do not assume that sharing one uncompressed packet means sharing compressor state across connections.

Both codecs are explicitly flushed for every message, and the client must decode the complete message from that flush alone. Production application, HTTP and proxy layers must also forward that output promptly. Waiting for a larger batch can dominate codec CPU time. The [Brotli streaming API](https://www.brotli.org/encode.html) specifies flush semantics and the quality/window controls; the [zlib manual](https://zlib.net/manual.html) documents `Z_SYNC_FLUSH` and its compression tradeoff.

Browser HTTP decompression is normally handled beneath application decoding when `Content-Encoding` is negotiated. It still consumes client CPU, even when application code never calls a decompressor directly. See [HTTP compression](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/Compression). Browser/mobile costs were not measured.

## Limits and validation

- Mac M1 Max; native zlib 1.2.12 and Brotli 1.2.0, clang -O3. Exact compiler/library information in `versions.txt`; source and fixture hashes retained. Brotli uses generic mode and window 22. Gzip uses wrapper mode, default strategy, memory level 8 and window 15. No custom dictionaries.
- 300 measured cases (12 fixtures × five codec settings × five rounds). Each encoder output is immediately decoded and compared against the original SSE message; end-of-stream finalization is also validated. No future event bytes are available during each decode.
- All payloads are generated by the existing Go layout/codec. The binary fixture length prefix is only a harness file format and is excluded from compressed input and sizes. PIL data is base64 within actual SSE event framing.
- Timing uses `CLOCK_MONOTONIC_RAW`, with calls around compression+flush and decompression separately. Correctness comparisons, fixture creation and source object/JSON/PIL encoding are outside timing. Timer/function-call overhead remains in the microsecond measurements. No RSS, cloud pricing, end-to-end or many-subscriber throughput claim is made.
- Five rounds rotate codec order. A persistent encoder and decoder process each file on one thread. The first context setup and first event are recorded separately. Raw JSON includes mean, p50 and p95 per round; the table p95 is the median of per-round p95s, not a pooled percentile.
- Descriptions use deterministic distinct hex text. Each stream repeatedly revisits the same object(s), with sparse changes and stable large fields. Actual content, object count, ordering and reconnect rate can change the ranking. JSON controls remain in `results.json`/`summary.csv`; full PIL is the primary comparison above.
- No production dependency, listener, compression policy, durability behavior or storage format was changed. The additional Go command only exports benchmark fixtures.

## Reproduce

Requires Go, clang, zlib and Brotli headers/libraries. Default Brotli prefix is `/opt/homebrew`; override `BROTLI_PREFIX` when needed. No database or network listener is used.

```sh
GOCACHE=/private/tmp/trie-trunk-go-cache python3 benchmarks/replay/compression/run.py /path/to/results
python3 benchmarks/replay/compression/report.py /path/to/results
```

Temporary fixture files and executable are removed by the runner. The results directory is updated only after successful measurement of the complete matrix.
''')
(root/'REPORT.md').write_text('\n'.join(out))
with (root/'summary.csv').open('w') as f:
 writer=csv.DictWriter(f,fieldnames=list(data[0]));writer.writeheader();writer.writerows(data)
print(root/'REPORT.md')
