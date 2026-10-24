"""Render paired checks/allocation/streaming experiments, retaining all raw data."""
import csv
import json
from pathlib import Path
ROOT=Path(__file__).resolve().parent
STREAM=ROOT.parent/'2026-10-16-compact-integer-streaming'
stage1=[json.loads(p.read_text()) for p in sorted((ROOT/'results').glob('*/*.json'))]
stage2=[json.loads(p.read_text()) for p in sorted((STREAM/'results').glob('*/*.json'))]
assert len(stage1)==24 and len(stage2)==8

def ns(c,m,op):return c['codec'][m+'/'+op]['p50_ns']/c['records_per_timing_sample']
def shape(c):return 'Sparse' if c['sparse'] else 'Dense'
def table(headers,rows):return '\n'.join(['| '+' | '.join(headers)+' |','|'+'|'.join(['---']*len(headers))+'|']+['| '+' | '.join(map(str,r))+' |' for r in rows])
def find(profile,sparse=False):return next(c for c in stage2 if c['profile']==profile and c['sparse']==sparse)
parts=['''# Compact integer optimization: checks, allocation, and streaming

**Yes, there is removable implementation overhead. Width checks alone were not the largest issue.** The original experiment used a generic zero-fill/copy integer loader, loaded signed values twice, emitted direct values in a second pass, and grew newly allocated output buffers several times. The optimized experiments preserve the exact same compact wire format and strict definitions.

The strongest improvement for fresh buffers comes from one allocation; fixed-width encoding benefits too. For reused buffers, combining width selection with value writes removes much of the extra pass cost. Direct typed loads/leading-zero checks alone help less. The residual cost of choosing/recording widths and reading selectors remains measurable.

All implementations remain Go test-only prototypes. No production storage, Rust/WASM, PIL negotiation, or server transport has changed.

## Paired dense unsigned result, one million records

These are warmed microbenchmark medians per record from the same run, not comparisons against timings recorded on a different day. Inputs use the smallest feasible fixed integer types. Definitions are compiled/cached before work.
''']
c=find('bounded-unsigned')
rows=[]
for mode,label in [('pil_fixed','Original fixed PIL'),('pil_fixed_fast','Optimized fixed PIL'),('pil_compact','Original compact PIL'),('pil_compact_loads','Compact: load/width check change only'),('pil_compact_fast','Compact: compiled checks + exact allocation'),('pil_compact_stream','Compact: combined selection/write pass'),('cbor','Existing CBOR encoder / positional decoder')]:
 rows.append([label,f'{ns(c,mode,"encode"):.1f}',f'{ns(c,mode,"encode_reuse"):.1f}' if mode!='cbor' else 'Not available',f'{ns(c,mode,"scan"):.1f}',f'{ns(c,mode,"scan_access"):.1f}'])
parts.append(table(['Variant','Fresh encode ns','Reused-buffer encode ns','Scan ns','Scan + numeric access ns'],rows))
parts.append('''
The `loads` ablation retains the original encoder passes and allocation strategy, changing only integer loading and width selection. Its decoder is unchanged. This prevents attributing allocation savings to value checks.

The exact-allocation encoder wins when producing fresh buffers. The combined selection/write encoder is intended for reusable buffers; its fresh-buffer path still grows through appends. A practical implementation can select the strategy by output-buffer context, without changing any field's declared type. These are measured separate paths, not a claim that a production dispatcher has been integrated.

## What changed

1. **Native integer loads.** Read 1/2/4/8-byte integers directly instead of zero-filling an eight-byte scratch buffer and copying into it. Normalize signed magnitude once, then use `bits.Len64` plus a small width-code lookup. The original eligibility and signedness were already compiled; the experiment does not infer schema types from values.
2. **Compiled validation and selector counts.** Cache BOOL/STRING flags, dictionary bounds, offset eligibility, and per-mask-byte selector counts. The optimized scanner avoids type-name comparisons and a second call through the original scanner on native-only frames. No validation is intentionally disabled.
3. **One allocation for fresh output.** Calculate the exact encoded length and allocate once. Apply the same strategy to the fixed-width control. The original fixed encoder is retained as the reused-capacity fast path, since an extra sizing pass would hurt that case.
4. **Combine width selection with direct-value writes.** Reserve the maximum selector area, append shortened values while setting mask/selectors, then close any unused selector space with one overlapping copy. No separate width array or direct-value emission pass. Only direct bytes move; variable strings/payloads are appended afterwards. There is no extra gap in the transmitted frame.

The selector-count table costs `256 × ceil(field_count / 4)` bytes per compiled definition: 768 bytes for these 12-field definitions, up to 16 KiB for 256 fields, plus field metadata. This trades small per-definition memory for fewer runtime operations. Cold-cache behavior across many simultaneous definitions was not measured.

The streaming encoder does **not** change U16/I16 handling: this isolates optimization of the existing U32/I32/U64/I64 prototype. Adding mask-only 16-bit shortening remains a separate experiment.

## Remaining cost against an equally optimized fixed codec

Fresh compact encoding uses exact allocation; reused compact encoding uses the combined selection/write pass. Both use the optimized scanner. Fixed columns use the matched optimized fixed implementation. All values are nanoseconds per record.
''')
rows=[]
for c in stage2:
 rows.append([c['profile'],shape(c),f'{ns(c,"pil_fixed_fast","encode"):.1f} / {ns(c,"pil_compact_fast","encode"):.1f}',f'{ns(c,"pil_fixed_fast","encode_reuse"):.1f} / {ns(c,"pil_compact_stream","encode_reuse"):.1f}',f'{ns(c,"pil_fixed_fast","scan"):.1f} / {ns(c,"pil_compact_fast","scan"):.1f}'])
parts.append(table(['Profile','Shape','Fresh encode fixed / compact','Reused encode fixed / compact','Scan fixed / compact'],rows))
parts.append('''
For the bounded-value **64-bit contract** control (same values, genuinely wider declared range), the paired measurements are:
''')
rows=[]
for c in stage2:
 if not c['profile'].startswith('bounded'):continue
 rows.append([c['profile'],shape(c),f'{ns(c,"pil_fixed64_fast","encode"):.1f} / {ns(c,"pil_compact64_fast","encode"):.1f}',f'{ns(c,"pil_fixed64_fast","encode_reuse"):.1f} / {ns(c,"pil_compact64_stream","encode_reuse"):.1f}',f'{ns(c,"pil_fixed64_fast","scan"):.1f} / {ns(c,"pil_compact64_fast","scan"):.1f}'])
parts.append(table(['Profile','Shape','Fresh encode fixed / compact','Reused encode fixed / compact','Scan fixed / compact'],rows))
parts.append('''
Full-range integers remain important controls: they cannot be shortened in these sampled datasets and gain no bytes, but still pay eligibility/width-selection costs. The improvement is not evidence that checking every value is free or always worthwhile.

## Sizes and correctness

Every optimized encoding was compared **byte for byte** with its original counterpart, for every generated record, outside timing. All records were also decoded and compared semantically. No wire-size changes, no weakened data equivalence, no omitted fields, and no new type definitions explain these speed changes.
''')
rows=[]
for c in stage2:
 z=c['population_bytes']
 rows.append([c['profile'],shape(c),f'{z["pil_fixed"]:,}',f'{z["pil_compact"]:,}',f'{z["cbor"]:,}'])
parts.append(table(['Profile','Shape','Fixed population bytes','Compact population bytes','CBOR population bytes'],rows))
parts.append('''
All reused scan/access paths and reusable PIL encode paths measured zero allocations per record. Exact-allocation PIL fresh encoders measured one allocation per record. The original/combined-pass fresh encoders grow through appends and can allocate several times.

Validation: the whole cache test suite passed after the final changes; boundary comparisons and randomized equivalence cases passed. Differential scanner fuzzing completed 127,903 executions, followed by 132,579 executions exercising the streaming encoder, without failure. Tests include negative extremes, selector byte crossings, partial fields, NULL/ABSENT, malformed/truncated frames, dictionary ordinals, BOOL validation, and borrowed-frame re-encoding.

## Measurement scope

- Stage 1: 24 cases, four signed/unsigned bounded/full-range profiles × dense/sparse × 50k/500k/1M records. Includes original encoders, typed-load-only ablations, matched optimized fixed encoders, compact exact-allocation encoders, and CBOR.
- Stage 2: eight follow-up cases at 1M records with the same controls plus the combined selection/write encoder. The primary tables above all use paired timings from stage 2. They do not mix timings across runs.
- Same fixtures as the [original compact experiment](../2026-10-16-compact-integers/REPORT.md): twelve definition positions; dense populations with eight present fields; sparse populations with two identities and one or two changed fields. Event UUID and shared classification fields are excluded in the same population projection. Signed IDs and randomized full-range revisions are type stress cases, not application-domain recommendations.
- Full-range PCG seeds remain `0x70696c`, `0x63626f72`. Definitions, boxing, prepared positional bytes, and sample generation are outside timing. Numeric-key CBOR uses the current generic map encoder; CBOR decoding goes directly into positions without an intermediate map.
- 1,024 evenly sampled records per collection, eight repetitions per timing sample; mode order rotates for a complete cycle (43 rounds in stage 1, 51 in stage 2). Counts describe source collections, not total timed search duration.
- Runs were sequential. Raw samples, p50/p95, definitions, exact sizes, allocations, and row numbers are retained. Benchmarks are warm, in-memory single-machine tests; no IPC, disk, networking, aggregation, or collection latency is measured. Small timing differences and allocation-path GC variance should not be overinterpreted.

## Recommendation

Keep the type contract strict and compile its execution plan once. Use direct typed loads and avoid a separate width-emission pass when reusing buffers. Use one output allocation when a fresh frame is required. These are implementation optimizations that retain the candidate wire format.

There is still a storage/bandwidth-versus-CPU tradeoff. For already well-sized schemas, a few percent fewer bytes may not justify the residual work. For legitimate wide contracts dominated by small values, the larger byte savings make the tradeoff more attractive. Further possibilities such as generated schema-specific code or bypassing shortening for consistently full-width fields were **not** implemented or measured.

Where persisted bytes already match the negotiated transmission layout, forwarding those frames can avoid re-encoding and width checks at collection time. That is an architectural opportunity, not a measured or newly implemented end-to-end path here.

## Reproduction and artifacts

```sh
python3 benchmarks/storage/2026-10-16-compact-integer-optimization/run.py
python3 benchmarks/storage/2026-10-16-compact-integer-streaming/run.py
python3 benchmarks/storage/2026-10-16-compact-integer-optimization/report.py
```

Each run directory contains its own raw `results/`, `metadata.json`, archived measured sources, and executable hash. Source hashes are verified at the end of each run; the source archives are the authoritative measured versions if files are edited later. `summary.csv` combines both stages with normalized p50/p95 and allocations; raw samples remain separate.

Implementation: `go/server/cache/compact_integer_optimized_test.go` and `compact_integer_streaming_test.go`, with corresponding validation and measurement test files. The original reference implementation remains unchanged.
''')
(ROOT/'REPORT.md').write_text('\n\n'.join(parts)+'\n')
with (ROOT/'summary.csv').open('w',newline='') as f:
 w=csv.writer(f);w.writerow(['stage','profile','shape','events','variant','operation','population_bytes','p50_ns_per_record','p95_ns_per_record','allocations_per_record'])
 for stage,cases in [(1,stage1),(2,stage2)]:
  for c in cases:
   assert c['all_records_validated']
   for k,v in c['codec'].items():
    mode,op=k.split('/');n=c['records_per_timing_sample']
    w.writerow([stage,c['profile'],shape(c),c['events'],mode,op,c['population_bytes'][mode],v['p50_ns']/n,v['p95_ns']/n,c['allocations_per_record'][k]])
    if op in ('scan','scan_access','encode_reuse'):assert c['allocations_per_record'][k]==0,(stage,c['profile'],k)
    if op=='encode' and mode.endswith('_fast'):assert c['allocations_per_record'][k]==1,(stage,c['profile'],k)
   z=c['population_bytes']
   for mode in z:
    if mode=='cbor':continue
    original=mode.removesuffix('_fast').removesuffix('_loads').removesuffix('_stream')
    assert z[mode]==z[original]
print('Report and summary written; 32 cases verified.')

# Preserve the protocol correction when regenerating this historical report.
_report=ROOT/"REPORT.md"
_notice='> **Superseded for the intended PIL design.** This experiment repurposed `11 OFFSET` for compact integers. The field-state bits are reserved for DIRECT/OFFSET, so its size and latency results do not describe the requested separate width mask. See the [corrected benchmark](../2026-10-16-separate-width-mask/REPORT.md). Historical data is retained for audit.\n\n'
if not _report.read_text().startswith("> **Superseded"):
 _report.write_text(_notice+_report.read_text())
