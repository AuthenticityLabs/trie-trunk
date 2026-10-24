"""Collection-side byte assembly only; no claim of end-to-end search timing."""
import csv,json
from pathlib import Path
ROOT=Path(__file__).resolve().parent
CASES=[json.loads(p.read_text()) for p in sorted((ROOT/'results').glob('*/*.json'))]
assert len(CASES)==12
MODES=('absolute_forward','relative_forward','relative_expand','absolute_compact')
QUERIES=('all','scattered_1pct','boundary_3h','single')
def find(n,p,s):return next(c for c in CASES if c['events']==n and c['profile']==p and c['sparse']==(s=='sparse'))
def elapsed(c,q,m):return c['codec'][q+'/'+m]['p50_ns']/c['repeats_per_sample'][q]
def bytesout(c,q,m):return c['output'][q+'/'+m]['Bytes']
def table(h,r):return '\n'.join(['| '+' | '.join(h)+' |','|'+'|'.join(['---']*len(h))+'|']+['| '+' | '.join(map(str,x))+' |' for x in r])
parts=['''# Shared time base: collection-to-transit cost

**A shared base need not force collection-time re-encoding.** If transport carries each source block's base and the receiver accepts the relative encoding profile, the frame bodies can be forwarded unchanged. If the receiver requires standalone PIL frames, only the temporal fields need reconstruction; the other encoded spans can be copied. This experiment measures those output paths directly, using trusted, prevalidated PIL frame bytes and cached definition plans.

This is a test-only model of the assembly stage **after matching rows have been selected**. It does not modify production `.idx`/`.pop`, `Store.Search`/`Store.Collect`, Rust/WASM or negotiated HTTP. No disk, binary/time-trie search, predicate evaluation, trie expansion, checksums, TLS/QUIC, socket writes, generic compression, or receiver execution is included in sender timings. The matched source row order is identical across modes. Encoded frames are packed into contiguous absolute/relative byte buffers before timing, to model sequential file data and avoid per-frame heap-placement bias. The preliminary per-frame-allocation run is retained in `../2026-10-16-timebase-transit-first-pass/`; all primary tables below use contiguous sources.

## Paths

```text
absolute_forward : stored absolute PIL → copy → standalone PIL
relative_forward : stored relative PIL + source base → copy → relative PIL + same base
relative_expand  : stored relative PIL → reconstruct time fields, copy other spans → standalone PIL
absolute_compact : stored absolute PIL → subtract source base, copy other spans → relative PIL + base
```

`relative_expand` does not allocate intermediate maps or Value arrays, call a general decoder/encoder, or rewrite offsets/string contents. It locates the eligible direct fields using the definition/presence mask, replaces their bytes, and copies the unaffected spans. The traversal stops at the final temporal field. The normal ABSENT/NULL/DIRECT/OFFSET mask remains unchanged.

Forwarding here means a copy into a reused output packet buffer, **not kernel zero-copy**. A receiver that supports relative frames must retain the base and apply it when consuming timestamp/UUID fields. Its CPU cost is not measured here.

## Proposed experimental context

- UUIDv7's six timestamp bytes become a U32 millisecond offset; the other ten bytes are unchanged: 16 → 14 bytes.
- TIMESTAMP_MS becomes a U32 offset: 8 → 4 bytes.
- An exact signed I64 millisecond base belongs to the package/source block. The experimental definition plan specifies eligible fields and profile. The event role is explicitly constrained to UUIDv7 here; arbitrary UUID fields are not automatically shortened.
- Inputs span September and October 2026. Source blocks hold at most 4,096 rows and never cross a month boundary. Each uses that month's exact UTC start as base. These boundaries are test parameters, not a proposed filesystem change.
- Every transport package has an eight-byte profile/count header; relative packages add the eight-byte base. Source-block changes and the 64-KiB output-buffer limit start another package. A filtered hit therefore pays its own package overhead if no other hit shares that package. Every such base byte is counted.
- PIL bodies remain self-delimiting through their field masks and final End Offset. **No per-record length, base ID, or extra selector is added.** The receiver-side validation discovers frame lengths from the definition and bytes, then reconstructs the originals.
- No cross-base rebasing/merging, per-frame full-width fallback, epoch-unit changes, or lossy precision reduction. Values outside the U32 base interval are rejected; all timestamps and all UUID bits round-trip exactly.

## Full-collection output assembly

Median milliseconds for the **whole selected collection**, with 12 rotated rounds per case. Byte figures are decimal MB including experimental package headers. Unlike earlier sampled codec tests, every selected row is processed in each timed query.
''']
rows=[]
for n in (50000,500000,1000000):
 for p in ('uuid','uuid_ts'):
  for s in ('full','sparse'):
   c=find(n,p,s);q='all'
   rows.append([f'{n:,}',p,s]+[f'{elapsed(c,q,m)/1e6:.3f}' for m in MODES]+[f'{bytesout(c,q,"absolute_forward")/1e6:.3f} → {bytesout(c,q,"relative_forward")/1e6:.3f}'])
parts.append(table(['Events','Temporal fields','Shape','Absolute copy ms','Relative copy ms','Expand ms','Compact on collection ms','Transit MB absolute → relative'],rows))
parts.append('''
`uuid` shortens only the mandatory event UUID. `uuid_ts` additionally carries one TIMESTAMP_MS per record. These are separate scenarios, not an assumption that a timestamp must duplicate UUID time. Full records use twelve fields with range-sized integers, strings and dictionary references. Sparse records retain event/object/revision identity and one/two changed fields; the `uuid_ts` case adds timestamp metadata. Object IDs are not assumed to be UUIDv7.

## Selected results at one million stored records

Queries are prepared outside timing: all rows, one out of every hundred rows across the full source, a three-hour interval crossing September/October, and one row. Every query uses the same selected rows for all modes. Small outputs repeat enough times to reach approximately 1,000 processed records per timing sample; reported times are divided back to one query.
''')
rows=[]
for p in ('uuid','uuid_ts'):
 for q in QUERIES:
  c=find(1000000,p,'sparse')
  rows.append([p,q,f'{c["selected_counts"][q]:,}']+[f'{elapsed(c,q,m)/1000:.3f}' for m in MODES]+[f'{bytesout(c,q,"absolute_forward"):,} → {bytesout(c,q,"relative_forward"):,}',c['output'][q+'/relative_forward']['Packets']])
parts.append(table(['Temporal fields','Query','Returned rows','Absolute copy µs','Relative copy µs','Expand µs','Compact µs','Wire bytes absolute → relative','Relative packages'],rows))
parts.append('''
The single-row case is intentionally unfavorable: sharing nothing, UUID-only relative output grows by 6 bytes (8-byte base minus 2-byte payload saving), and UUID + timestamp grows by 2 bytes (8 minus 6). Sparse/scattered retrieval can also increase overhead per hit; the report counts all source-block and packet boundaries.

## Byte savings and conditional network break-even

These are **complete-frame payload** totals plus the incremental base metadata, not total datastore file sizes. Production currently puts event_id in the index and projects it out of population; applying UUID compression there would require a compatible index/block format and separate search benchmarks. We have not claimed those changes or their performance.
''')
rows=[]
for p in ('uuid','uuid_ts'):
 for s in ('full','sparse'):
  c=find(1000000,p,s);q='all'
  saved=bytesout(c,q,'absolute_forward')-bytesout(c,q,'relative_forward')
  added=elapsed(c,q,'absolute_compact')-elapsed(c,q,'absolute_forward')
  threshold=8*saved/added if added>0 else None # bytes/ns converted to Gbit/s
  rows.append([p,s,f'{c["absolute_frame_bytes"]:,}',f'{c["relative_frame_bytes"]+c["relative_base_bytes"]:,}',f'{saved:,}',f'{100*saved/bytesout(c,q,"absolute_forward"):.2f}%',f'{saved*8/1e9*1000:.2f}',f'{added/1e6:.2f}',f'{threshold:.2f}' if threshold else 'no measured CPU penalty'])
parts.append(table(['Temporal fields','Shape','Absolute frame bytes','Relative frames + stored bases','Transit bytes saved','Transit reduction','Ideal link time saved at 1 Gbit/s, ms','On-demand compact CPU extra ms','Serial break-even Gbit/s'],rows))
parts.append('''
The link calculation is illustrative, not a network benchmark: `saved_bytes × 8 / bandwidth`. The threshold compares added server CPU time for `absolute_compact` with that ideal link-time saving under serial execution. Pipelining, concurrency, network congestion, compression, client reconstruction, and per-byte egress billing alter the practical tradeoff. `relative_expand` sends the original full-size payload, so it gets no network-byte benefit despite its storage-side saving.

## Recommendation from this experiment

If the goal is retaining PIL's storage-to-transit advantage, keep the storage base unchanged and attach it once per transport package. Arbitrary filtered subsets do not require recompressing individual frames; retain their source base and package them together. This requires the receiver's cache and decoder to preserve that context. A base-relative body alone is no longer independently interpretable using just the object definition.

If fully standalone frames are required, the measured reconstruction path is a small temporal-field transformation rather than full object marshalling, but its added cost is real and its transport savings are zero. Adding base compression only during collection incurs the measured subtraction/assembly cost; compare it with bandwidth savings rather than assuming storage savings imply faster collection.

The absolute savings are bounded: approximately 2 MB per million UUID-only records or 6 MB with one additional millisecond timestamp, before context overhead. Large cumulative workloads can justify that, but it is not automatically a large percentage of total storage or transit.

## Validation and reproducibility

All source frames are validated with the existing layout, then compacted and restored byte-for-byte outside timing. Every output package in every measured query/mode is parsed from its field states and offsets and its reconstructed record bytes compared with the expected originals. Received SHA-256 hashes agree across all four modes. Tests cover U32 offset boundaries, before-base/out-of-range timestamps, wrong UUID version, absent/null temporal fields, filtered source-block transitions and revisiting a base. The full cache test suite passed.

All timed output paths reuse one 64-KiB buffer; allocation counts per query are included in raw results/CSV. Input generation, definition compilation, timestamp-to-month calculation, matching row selection, and receiver verification are outside timing. No intermediate expanded JSON/maps are used in the timed path. Sources are trusted and were validated on preparation; this is not a comparison of untrusted-input validation cost.

```sh
python3 benchmarks/storage/2026-10-16-timebase-transit/run.py
python3 benchmarks/storage/2026-10-16-timebase-transit/report.py
```

Raw p50/p95 samples and complete configurations are in `results/`; normalized results are in `summary.csv`. `metadata.json` and `source.tar.gz` preserve measured sources and executable hashes. The runner verifies source hashes at completion. These warmed single-machine results are not production tail-latency estimates or confidence intervals.
''')
(ROOT/'REPORT.md').write_text('\n\n'.join(parts)+'\n')
with (ROOT/'summary.csv').open('w',newline='') as f:
 w=csv.writer(f);w.writerow(['events','profile','shape','query','mode','returned_records','wire_bytes','packages','median_ms','p95_ms','median_ns_per_record','allocations_per_query'])
 for c in CASES:
  assert c['all_records_validated']
  for q in QUERIES:
   hashes={c['received_sha256'][q+'/'+m] for m in MODES};assert len(hashes)==1
   assert bytesout(c,q,'absolute_forward')==bytesout(c,q,'relative_expand')
   assert bytesout(c,q,'relative_forward')==bytesout(c,q,'absolute_compact')
   for m in MODES:
    key=q+'/'+m;out=c['output'][key];count=c['selected_counts'][q]
    assert out['Records']==count and c['allocations_per_query'][key]==0,(key,c['allocations_per_query'][key])
    assert out['Bytes']>0
    w.writerow([c['events'],c['profile'],'sparse' if c['sparse'] else 'full',q,m,count,out['Bytes'],out['Packets'],elapsed(c,q,m)/1e6,c['codec'][key]['p95_ns']/c['repeats_per_sample'][q]/1e6,elapsed(c,q,m)/count,c['allocations_per_query'][key]])
print('All 12 datasets / 192 query-mode outputs validated. Report and CSV written.')
