#!/usr/bin/env python3
"""Render direct decoder comparison without overwriting earlier reports."""
import json
from pathlib import Path
root=Path(__file__).resolve().parent
load=lambda p:json.loads(p.read_text())
ms=lambda q:q['p50_ns']/1e6
rows=[]
for shape in ('full','sparse'):
 for n in (50000,500000,1000000):
  d=load(root/'results'/f'{shape}-{n}.json')
  old=load(root.parent/'2026-10-12-fixed-aggregates/codecs'/f'{shape}-{n}.json')
  assert d['fixture_sha256']==old['fixture_sha256']
  assert d['population_bytes']==old['population_bytes']
  assert d['result_hashes']==old['result_hashes'] and d['matched_events']==old['matched_events']
  rows.append((shape,n,d))
lines=['# Direct CBOR to positional values', '',
'Measured 2026-10-12 on darwin/arm64 with Go 1.26.4. This follow-up isolates the CBOR decoder’s intermediate representation; earlier raw reports remain unchanged.', '',
'## Controlled search comparison', '',
'All five variants use the same in-process Go search loop, time index, trie, population ReadAt calls, predicate checks, and PIL output expansion. Map-based CBOR and direct CBOR read the **same file bytes and offsets**. The PIL population encodes exactly the same projected fields. No Rust IPC, HTTP, browser rendering, or JSON expansion appears in these measurements. Definitions and direct-decoder descriptors are compiled before timed work.', '',
'- **CBOR map**: existing generic `codec.Decode` into a numeric-key map, then conversion to positional values.',
'- **CBOR direct**: compiled field descriptors route numeric CBOR keys directly to positional values; strings/bytes are borrowed, scalars are decoded into a contiguous scratch buffer.',
'- **PIL**: the existing positional scanner.',
'- **Reused controls**: CBOR direct and PIL retain their value/scratch buffers across records. Fresh controls create a new frame for every record. Reuse does not eliminate the shared per-record population ReadAt buffer allocation.', '',
'Each query has nine warm samples, with mode order rotated between samples. Tables show p50. Output SHA-256 hashes and event counts match in all modes and match the prior benchmark; validation is outside timed samples. The OS page cache was not flushed. The p95 of nine samples is the maximum observation, not a production percentile estimate.', '',
'Full fixtures contain 12 fields. Sparse fixtures contain an identity envelope and one or two changed business fields, with no trie root. Populations omit indexed identities and referenced shared fields identically for all codecs.', '',
'| Shape | Events | Map CBOR ms | Direct CBOR ms | PIL ms | Direct CBOR / PIL |',
'| --- | ---: | ---: | ---: | ---: | ---: |']
for shape,n,d in rows:
 q=d['query']['all'];c,p=ms(q['cbor_direct']),ms(q['pil'])
 lines.append(f"| {shape} | {n:,} | {ms(q['cbor']):.3f} | {c:.3f} | {p:.3f} | {c/p:.3f}× |")
lines += ['', 'Ratios near 1 mean similar observed latency; differences of a few percent should not be treated as a durable codec ranking from this run alone.', '',
'| Shape | Events | Reused direct CBOR ms | Reused PIL ms | Direct CBOR / PIL |',
'| --- | ---: | ---: | ---: | ---: |']
for shape,n,d in rows:
 q=d['query']['all'];c,p=ms(q['cbor_direct_reuse']),ms(q['pil_reuse'])
 lines.append(f'| {shape} | {n:,} | {c:.3f} | {p:.3f} | {c/p:.3f}× |')
lines += ['', '## Narrow windows and predicates', '',
'| Shape | Events | Query | Matches | Map CBOR ms | Direct CBOR ms | PIL ms | Reused direct CBOR ms | Reused PIL ms |',
'| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |']
for shape,n,d in rows:
 for name in ('day','three_hours','state'):
  q=d['query'][name]
  values=' | '.join(f'{ms(q[k]):.3f}' for k in ('cbor','cbor_direct','pil','cbor_direct_reuse','pil_reuse'))
  lines.append(f"| {shape} | {n:,} | {name} | {d['matched_events'][name]:,} | {values} |")
lines += ['', 'The state predicate selects one third of events. Full-event shared references can reject rows before population access. Sparse events have no shared classification and need population decoding before the predicate can be resolved.', '',
'## Decode-only control and allocations', '',
'The first batch of 1,024 population records is decoded 21 times with inputs already prepared. Numbers below use the million-event fixtures and divide the median batch time by its record count. This control excludes file access, traversal, expansion and output encoding. It also excludes descriptor compilation. Fresh and reused frames are separate measurements.', '',
'| Shape | Map CBOR ns/record | Direct CBOR ns/record | PIL ns/record | Reused direct CBOR ns/record | Reused PIL ns/record |',
'| --- | ---: | ---: | ---: | ---: | ---: |']
keys=('cbor_decode_1024','cbor_direct_decode_1024','pil_decode_1024','cbor_direct_reuse_decode_1024','pil_reuse_decode_1024')
for shape,n,d in rows:
 if n!=1000000:continue
 values=' | '.join(f"{d['codec'][k]['p50_ns']/d['codec_batch_events']:.1f}" for k in keys)
 lines.append(f'| {shape} | {values} |')
lines += ['', '| Shape | Map CBOR allocations | Direct CBOR allocations | PIL allocations | Reused direct CBOR allocations | Reused PIL allocations |',
'| --- | ---: | ---: | ---: | ---: | ---: |']
for shape,n,d in rows:
 if n!=1000000:continue
 a=d['decoder_allocations_per_record'];values=' | '.join(str(a[k]) for k in ('cbor','cbor_direct','pil','cbor_direct_reuse','pil_reuse'))
 lines.append(f'| {shape} | {values} |')
lines += ['', 'Allocation counts use Go’s AllocsPerRun on the first projected record. Both reused decoders allocate zero objects per record after warmup. The direct CBOR scanner has no decoded map, string copy, or per-value allocation; integer conversion to the common little-endian slots remains necessary.', '',
'## Size is unchanged', '',
'| Shape | Events | CBOR population MB | PIL population MB |',
'| --- | ---: | ---: | ---: |']
for shape,n,d in rows:
 b=d['population_bytes'];lines.append(f"| {shape} | {n:,} | {b['cbor']/1e6:.3f} | {b['pil']/1e6:.3f} |")
lines += ['', 'MB = 1,000,000 bytes. These are uncompressed population bytes, excluding shared metadata/index bytes. Switching CBOR decoding strategy changes no stored bytes. The CBOR sizes and PIL sizes match the previous fixture exactly.', '',
'## Scope and validation', '',
'The scanner implements a deliberately bounded, definition-driven scalar profile using [RFC 8949’s encoding rules](https://www.rfc-editor.org/rfc/rfc8949.html#section-3.1). It supports unsigned/signed primitives, timestamp integers, REF16/dictionary ordinals, UUID byte strings, BOOL, F32/F64, STRING/BYTES, and NULL. Numeric keys can arrive out of order. Missing fields remain ABSENT. Strings/bytes borrow the input; scalar buffers belong to the reusable frame.', '',
'It rejects duplicate/unknown keys, incompatible types, out-of-range values, malformed UTF-8, incomplete items and trailing bytes. It accepts non-preferred integer widths; only definite-length maps/strings are supported. Native arrays, nested CBOR payloads, tags, and indefinite-length items are not supported. Unsupported schema types fail at compilation. This is not a replacement for the full generic CBOR library, and these scalar measurements do not establish performance for complex objects.', '',
'Correctness tests cover full/sparse differential decoding, NULL/empty/ABSENT states, buffer reuse, borrowed bytes, numeric boundaries, floating-point widths, malformed input, and zero-allocation reuse. A 10-second fuzz run completed 572,744 executions without a failure. The full storage/CLI tests are run separately after the measurements, not concurrently.', '',
'## Reproduce', '',
'```sh',
'GOCACHE=/private/tmp/trie-trunk-go-cache go -C go test -c ./server/cache -o /tmp/direct-cbor.test',
'TRUNK_MEASURE_EVENTS=1000000 TRUNK_MEASURE_SHAPE=full \\',
'  TRUNK_MEASURE_OUT=/tmp/direct-cbor.json \\',
'  /tmp/direct-cbor.test -test.run "^TestMeasurePopulationCodecs$" -test.timeout 10m',
'```', '',
'Use `sparse` for the PATCH fixture; the stored matrix also includes 50,000 and 500,000 events. `report.py` renders this report and verifies fixtures/results against the preceding experiment. Raw JSON retains every timing sample. The source archive and metadata preserve the implementation and measured binary hashes. No production storage or running service was changed.']
lines[2:2] = ["> **Codec fixture correction (2026-10-16):** These measurements use wide PIL integers containing small values. See the [range-matched and full-U64 measurements](../2026-10-16-integer-ranges/REPORT.md) before drawing a general format conclusion. Historical decoder comparisons remain available under their original conditions.", ""]
(root/'REPORT.md').write_text('\n'.join(lines)+'\n')
