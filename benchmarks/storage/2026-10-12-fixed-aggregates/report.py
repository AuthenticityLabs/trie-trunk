#!/usr/bin/env python3
"""Render the measured report; retain every sample in adjacent JSON files."""
import json
from pathlib import Path
root = Path(__file__).resolve().parent
load = lambda p: json.loads(p.read_text())
ms = lambda d: d['p50_ns']/1e6
mb = lambda n: n/1e6
lines = ['# Fixed aggregate rows and a shared-reader codec comparison', '',
'Measured 2026-10-12 on darwin/arm64 with Go 1.26.4. Definitions and fixture generation occur before timed work. These are local prototype measurements, not a server/browser rollout.', '',
'## Fixed aggregates: before and after', '',
'The frozen before binary uses the prototype’s original CBOR aggregate encoding. The after binary writes `SUM1` bucket groups and `AGG1` rollups. Both use the same Go store, event fixture, batch size (1,024), PIL event population, source watermarks, CRC framing, and query API. This comparison does not use Rust or IPC.', '',
'Each bucket stores its timestamp once, then sorted 42-byte rows: REF16 plus five U64 accumulators. A summary has a 28-byte source/bucket directory header and a 12-byte header per bucket; each rollup has a 24-byte period header. Existing 64-byte file and 8-byte record framing are included in file sizes. Fixed aggregate rows are a storage encoding, not standalone PIL messages.', '',
'The 12-field fixture spans 30 days and includes identity fields, dictionary values, dynamic strings, and integers. Aggregate counts and fixture hashes agree before/after. All configured aggregates are on the write branch; debug/trace/nuts exclusions are covered by correctness tests.', '',
'| Events | CBOR summary MB | Fixed summary MB | Change | CBOR file read+decode ms | Fixed file read+decode ms | Speedup |',
'| ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
pairs = []
for n in (50000,500000,1000000):
 b,a=load(root/'before'/f'{n}.json'),load(root/'after'/f'{n}.json')
 assert b['fixture_sha256']==a['fixture_sha256'] and b['aggregate_events']==a['aggregate_events']==n
 assert b['aggregate_rows']==a['aggregate_rows']
 pairs.append((n,b,a))
 x,y=ms(b['file_read_decode']),ms(a['file_read_decode'])
 lines.append(f"| {n:,} | {mb(b['sum_bytes']):.3f} | {mb(a['sum_bytes']):.3f} | {(a['sum_bytes']/b['sum_bytes']-1)*100:+.1f}% | {x:.3f} | {y:.3f} | {x/y:.2f}× |")
lines += ['', 'All latency values below are p50 unless labeled otherwise. Every measurement retains nine warm samples; the p95 of nine observations is the largest sample, not a production percentile estimate. The OS page cache was not flushed.', '',
'| Events | Encode CBOR/fixed ms | Decode CBOR/fixed ms | Warm full-window query CBOR/fixed ms | Store reopen CBOR/fixed ms | Ingest CBOR/fixed s |',
'| ---: | ---: | ---: | ---: | ---: | ---: |']
for n,b,a in pairs:
 lines.append(f"| {n:,} | {ms(b['encode']):.3f} / {ms(a['encode']):.3f} | {ms(b['decode']):.3f} / {ms(a['decode']):.3f} | {ms(b['query']['all']):.3f} / {ms(a['query']['all']):.3f} | {ms(b['reopen']):.3f} / {ms(a['reopen']):.3f} | {b['ingest_ns']/1e9:.3f} / {a['ingest_ns']/1e9:.3f} |")
lines += ['', 'Encode/decode processes the entire summary set; file read+decode also includes ReadAt and CRC verification. Store reopen verifies the broader index/population, so its timing is not aggregate decoding alone. Ingest includes durable close and is a single observation, not a median. Warm summary queries merge groups already decoded in memory; their algorithm is unchanged, so timing fluctuations are not evidence of a format-specific query improvement.', '',
'| Events | Daily rollup CBOR/fixed bytes | Read 30 daily rollups CBOR/fixed ms |', '| ---: | ---: | ---: |']
for n,b,a in pairs:
 lines.append(f"| {n:,} | {b['rollup_bytes']:,} / {a['rollup_bytes']:,} | {ms(b['rollup_read_30_days']):.3f} / {ms(a['rollup_read_30_days']):.3f} |")
lines += ['', 'Daily rollups share the yearly `.365` file. The read metric uses the unchanged public API to fetch each of the 30 days; that currently scans the file per request. This result does not claim a new rollup directory or direct seek optimization.', '',
'## CBOR versus PIL: identical search path', '',
'Both variants call the same in-process Go search loop: the same time trie, sorted runs, fixed-width index fields, predicate checks, REF16 expansion, ReadAt population access, and PIL output callback. Only population encoding/decoding and its corresponding byte offsets differ. There is no socket, Rust reader, HTTP, or browser in either measurement. Numeric CBOR keys and dictionary ordinals remain compact. No JSON is built.', '',
'The CBOR population is an isolated benchmark projection of exactly the same stored fields as the PIL population, not a rerun of the old hourly filesystem layout. It uses the existing `codec.Decode` implementation and converts its decoded map into positional values. PIL borrows primitive bytes directly. Thus the result includes the current CBOR library/API’s intermediate map and conversion costs; it does not prove an intrinsic format speed limit or compare against a specialized direct-to-position CBOR decoder.', '',
'PIL result SHA-256 hashes and row counts match for every query and both codecs. Verification is outside timed samples. Full events use 12 fields; sparse PATCH events retain the three-field identity envelope plus one or two business fields and have no trie-root classification. Definitions/maps are compiled before timing. Query samples alternate codecs to limit order bias.', '',
'| Shape | Events | CBOR population MB | PIL population MB | PIL size change | Full query CBOR ms | Full query PIL ms | PIL speedup |',
'| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
codecs=[]
for shape in ('full','sparse'):
 for n in (50000,500000,1000000):
  d=load(root/'codecs'/f'{shape}-{n}.json');codecs.append((shape,n,d))
  b,a=d['population_bytes']['cbor'],d['population_bytes']['pil']
  x,y=ms(d['query']['all']['cbor']),ms(d['query']['all']['pil'])
  lines.append(f'| {shape} | {n:,} | {mb(b):.3f} | {mb(a):.3f} | {(a/b-1)*100:+.1f}% | {x:.3f} | {y:.3f} | {x/y:.2f}× |')
lines += ['', 'Population sizes exclude shared index/map/trie bytes, which are equal between codecs. No compression is applied. PIL’s fixed-width integers can occupy more bytes than CBOR’s small integers, particularly for sparse changes; faster decoding does not imply smaller population.', '',
'| Shape | Events | Query | Matches | CBOR p50 ms | PIL p50 ms | CBOR p95 ms | PIL p95 ms |',
'| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: |']
for shape,n,d in codecs:
 for name in ('day','three_hours','state'):
  q=d['query'][name]
  lines.append(f"| {shape} | {n:,} | {name} | {d['matched_events'][name]:,} | {ms(q['cbor']):.3f} | {ms(q['pil']):.3f} | {q['cbor']['p95_ns']/1e6:.3f} | {q['pil']['p95_ns']/1e6:.3f} |")
lines += ['', 'The `state` predicate selects one third of events. Full-event classification can reject other shared references before reading population; sparse changes have no root/reference and require value decoding.', '',
'## Codec-only batch control', '',
'The first committed batch (1,024 projected records) is prepared before timing, then independently encoded or decoded 21 times. The table uses the million-event fixtures, divided by batch count. CBOR decode includes adaptation to the common positional frame; CBOR encode starts with prepared compact maps and PIL encode starts with prepared positional values. Neither encode measurement includes construction of its input representation. These controls measure those APIs, not the cost of constructing a complete application object.', '',
'| Shape | CBOR encode ns/event | PIL encode ns/event | CBOR decode/adapt ns/event | PIL scan ns/event |',
'| --- | ---: | ---: | ---: | ---: |']
for shape,n,d in codecs:
 if n!=1000000:continue
 c=d['codec'];k=d['codec_batch_events']
 lines.append(f"| {shape} | {c['cbor_encode_1024']['p50_ns']/k:.1f} | {c['pil_encode_1024']['p50_ns']/k:.1f} | {c['cbor_decode_1024']['p50_ns']/k:.1f} | {c['pil_decode_1024']['p50_ns']/k:.1f} |")
lines += ['', '## Compatibility and reproduction', '',
'- Existing CBOR aggregate batches/rollups remain readable alongside new fixed records. New writes and retention rewrites use fixed encoding. An older executable cannot read the new tagged records.',
'- Source checkpoints, late deltas, the 15-minute provisional window, branch eligibility, and rollup conflict semantics are preserved.',
'- `before/source.tar.gz` and its ignored `cache.test` freeze the pre-change implementation plus measurement harness. `after-source.tar.gz` and metadata identify the measured follow-up source. Raw JSON retains all observations.',
'- Normal unit tests skip the large measurement fixtures. Build the test binary, then run one fixture at a time:', '', '```sh',
'GOCACHE=/private/tmp/trie-trunk-go-cache go -C go test -c ./server/cache -o /tmp/cache.test',
'TRUNK_MEASURE_EVENTS=1000000 TRUNK_MEASURE_OUT=/tmp/aggregates.json \\',
'  /tmp/cache.test -test.run "^TestMeasureAggregateStorage$" -test.timeout 10m',
'TRUNK_MEASURE_EVENTS=1000000 TRUNK_MEASURE_SHAPE=full \\',
'  TRUNK_MEASURE_OUT=/tmp/codecs.json \\',
'  /tmp/cache.test -test.run "^TestMeasurePopulationCodecs$" -test.timeout 10m',
'```', '',
'Run `report.py` to regenerate this report from the archived results. No production data was migrated and the running server was not restarted.']
lines[2:2] = ["> **Population-codec fixture correction (2026-10-16):** These codec measurements use wide PIL integers containing small values. See the [range-matched and full-U64 measurements](../2026-10-16-integer-ranges/REPORT.md) before drawing a general format conclusion. Historical measurements, including the separate aggregate comparison, are retained.", ""]
(root/'REPORT.md').write_text('\n'.join(lines)+'\n')
