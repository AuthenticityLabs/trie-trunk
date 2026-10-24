#!/usr/bin/env python3
"""Each PIL frame gets an independent, finalized codec context."""
import json,pathlib,statistics as st,sys
root=pathlib.Path(sys.argv[1]);data=json.loads((root/'results.json').read_text())
assert len(data)==200 and all(x['context']=='response' for x in data)
out=['''# Independent-message PIL compression

Date: 2026-10-18. Each native PIL frame is encoded with a fresh gzip/Brotli context, finalized, independently decoded and compared byte-for-byte. Codec headers/trailers are included; there is no base64 or text envelope. No previous event's compression history is available.

This tests an isolated response body or one independently compressed record, not a persistent stream and not a multi-record disk block. The object fixture and PIL Definition match the warm-stream benchmark. Two changed attributes; a stable 256/4096-byte description is present only in full objects. Partial decoding assumes the client already has base state.

Five rotated rounds, 2,048 messages each, 64 warmups excluded from reported statistics. Warmup warms machine/allocator state, never compression history: each measured message still has a new codec context. Eight fixtures × five settings × five rounds = 200 cases / 409,600 exact-byte round trips.

Tables are medians of five round means for one object's revisions. Raw JSON also contains 32-object interleaving controls. Compression and decompression times exclude codec-context construction and destruction. Mean encoder+decoder construction is shown separately; freeing contexts is outside timing. These are local native-library timings, not full server/client or browser latency.
''']
for size in [256,4096]:
 out.append(f'## {size:,}-byte description\n')
 out.extend(['| Payload | Codec | Raw bytes | Encoded bytes | Encode µs | Decode µs | Encoder+decoder construction µs |','| --- | --- | --- | --- | --- | --- | --- |'])
 for v in ['full-pil','partial-pil']:
  for c,l in [('gzip',1),('gzip',6),('brotli',1),('brotli',4),('brotli',6)]:
   r=[x for x in data if (x['objects'],x['description_bytes'],x['variant'],x['codec'],x['level'])==(1,size,v,c,l)]
   assert len(r)==5
   vals=[st.median(x[k] for x in r) for k in ['raw_bytes','compressed_bytes','encode_mean_us','decode_mean_us','context_init_us']]
   out.append(f'| {v} | {c} {l} | '+' | '.join(f'{x:.2f}' for x in vals)+' |')
 out.append('')
out.append('''## Implications

A tiny patch can be larger after independent compression. Compression per message is not automatically a win even when a persistent stream compresses similar updates well. Compare total framed sizes before selecting raw or compressed representation. Full objects with sizable text benefit on this synthetic fixture, but these descriptions are hex text; production content can differ.

These results do not measure compression of record batches, file block layout, random-read amplification, integrity metadata, or storage replay. Independently compressed blocks can retain random access at block granularity; larger blocks share more compression history but require more decoding for a single record. Persisting continuous HTTP compression chunks as individually addressable records would require compressor state or independent restart points.

## Reproduce

```sh
GOCACHE=/private/tmp/trie-trunk-go-cache python3 benchmarks/replay/compression/run.py /path/to/results --context response
python3 benchmarks/replay/compression/report_response.py /path/to/results
```

No production storage format or HTTP compression configuration changed. Versions, source/fixture hashes and all five-round measurements are alongside this report.
''')
(root/'REPORT.md').write_text('\n'.join(out));print(root/'REPORT.md')
