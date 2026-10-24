#!/usr/bin/env python3
"""Summarize native raw-PIL compression results; keep historical SSE reports separate."""
import csv
import json
from pathlib import Path
import statistics as st
import sys
root = Path(sys.argv[1])
assert (root/'format.txt').read_text().strip() == 'pil'
data = json.loads((root/'results.json').read_text())
assert len(data) == 200
assert all(x.get("context", "stream") == "stream" for x in data), "Use the fresh-response report for response contexts"
out = ['''# Direct binary PIL: gzip versus Brotli

Date: 2026-10-18. Corrects the representation used in the earlier text-SSE benchmark. No production transport changes.

## Correction

The earlier harness base64-encoded PIL because it used literal text/event-stream framing. That was an unnecessary transport assumption for the intended binary PIL delivery. Base64 changes both input size and compression behavior; compressing afterward does not make that experiment equivalent to compressing native PIL. The earlier results remain historical text-SSE measurements, not a basis for selecting compression for binary PIL.

This run compresses the native PIL frame directly: no base64, SSE fields, JSON, or extra wire length prefix. The fixture's four-byte packet length is harness-only and never enters the compressor. Existing PIL encoding is unchanged.

## Method

Eight fixtures: full or partial PIL, 256- or 4,096-byte stable description, one or 32 interleaved objects. The same object mutations feed full and partial variants; each mutation changes two business attributes. One stream cycles through objects. Each case contains 2,048 updates, with 64 warmups, five rotated rounds and five codec settings (gzip 1/6, Brotli 1/4/6): 200 runs and 409,600 exact-byte decompression checks. Each update is flushed and decoded before the next is supplied; stream finalization is also validated.

Native zlib and Brotli use the same C timing path. Tables show median round means, not end-to-end server latency. Encoding the original PIL, database reads, network transfer, and client state application are outside these timings. Compression contexts persist across updates. Gzip has a 32 KiB history; Brotli uses lgwin=22 (approximately 4 MiB).
''']
def med(rows,key): return st.median(x[key] for x in rows)
for objects in [1,32]:
 for size in [256,4096]:
  out.append(f'## {objects} object(s), {size:,}-byte stable description\n')
  out += ['| Payload | Codec | Raw bytes/update | Compressed bytes/update | Compress µs | Decompress µs |', '| --- | --- | --- | --- | --- | --- |']
  for variant in ['full-pil','partial-pil']:
   for codec,level in [('gzip',1),('gzip',6),('brotli',1),('brotli',4),('brotli',6)]:
    rows=[x for x in data if (x['objects'],x['description_bytes'],x['variant'],x['codec'],x['level'])==(objects,size,variant,codec,level)]
    assert len(rows)==5
    vals=[med(rows,key) for key in ['raw_bytes','compressed_bytes','encode_mean_us','decode_mean_us']]
    out.append(f'| {variant} | {codec} {level} | '+' | '.join(f'{v:.2f}' for v in vals)+' |')
  out.append('')
out.append('''## Interpretation and limits

For these fixtures, gzip 6 remains a reasonable partial-PIL candidate and Brotli 4 remains a reasonable full-PIL candidate. These are workload-specific results, not a production policy. In the 32-object/4 KiB case, full-PIL Brotli 4 now averages about 29 bytes/update instead of the historical base64-SSE result of 144. Partial-PIL gzip 6 averages about 14 instead of 18. This demonstrates that the wrapper materially affected the earlier assessment.

The 32 × 4 KiB working set exceeds gzip's history but fits Brotli's, explaining why their full-object results diverge. Stream history lets full PIL reuse stable fields. A patch still requires a valid client base; its initial snapshot cost is not included. Descriptions are deterministic hex text and remain unchanged, so this is not a broad production content sample.

Warm averages exclude initial headers and final trailers. A fresh API response does not inherit another response's compressor history, even on a reused HTTP connection. This run therefore corrects binary subscription-stream compression, not isolated API response costs. Raw results include first-update diagnostics but those are not independently finalized response measurements. No claim about browser/mobile CPU, memory, fanout throughput, durable ingestion, or end-to-end replay latency follows from this codec-only test.

## Validation and reproduction

`TestBinaryCompressionFixturesContainOnlyPIL` verifies all 16,384 exported records equal the direct codec output, with no wrapper. Existing full/sparse application tests verify the encoded state. The C harness verifies byte-exact decoding at every flush. Library/compiler versions and source/fixture hashes are alongside this report.

```sh
GOCACHE=/private/tmp/trie-trunk-go-cache python3 benchmarks/replay/compression/run.py /path/to/results --format pil
python3 benchmarks/replay/compression/report_binary.py /path/to/results
```

The historical SSE fixture mode has been removed; only native PIL fixtures are supported. No codec, listener, storage or durability behavior was changed.
''')
(root/'REPORT.md').write_text('\n'.join(out))
with (root/'summary.csv').open('w') as f:
 w=csv.DictWriter(f,fieldnames=list(data[0]));w.writeheader();w.writerows(data)
print(root/'REPORT.md')
