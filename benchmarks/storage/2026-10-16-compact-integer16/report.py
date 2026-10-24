"""Render the final mask-only 16-bit experiment; keep raw measurements intact."""
import csv,json
from pathlib import Path
ROOT=Path(__file__).resolve().parent
CASES=[json.loads(p.read_text()) for p in sorted((ROOT/'results').glob('*/*.json'))]
assert len(CASES)==44
MODES=('fixed','compact32','compact16_generic','compact16')
def ns(c,m,op):return c['codec'][m+'/'+op]['p50_ns']/c['records_per_timing_sample']
def find(profile,shape):return next(c for c in CASES if c['profile']==profile and c['shape']==shape)
def table(headers,rows):return '\n'.join(['| '+' | '.join(headers)+' |','|'+'|'.join(['---']*len(headers))+'|']+['| '+' | '.join(map(str,r))+' |' for r in rows])
def savings(c):z=c['population_bytes'];return 100*(1-z['compact16']/z['compact32'])
parts=['''# Mask-only I16/U16 shortening: probability and density

**The extension saves one byte per qualifying present value, with no added selector bits. It is useful when many legitimate 16-bit fields frequently fit in eight bits. It is not a universal near-flat CPU improvement.** The prior implementation optimizations are useful independently; that does not automatically make every additional shortening policy worthwhile.

This report benchmarks strict I16/U16 definitions with the existing `11` field-mask state interpreted as a one-byte integer, alongside fixed-width and U32/I32/U64/I64-only shortening. U8/I8 remain unchanged. This is an isolated Go test prototype, not a deployed or negotiated PIL format.

## Layout and expected savings

| Mask | I16/U16 state |
|---|---|
| `00` | ABSENT; no value |
| `01` | NULL; no value |
| `10` | Native two-byte integer |
| `11` | One-byte integer; no width selector |

Unsigned range: 0–255. Signed range: −128–127, stored in two's complement and sign-extended on access. Other integer and variable-field mask meanings remain as in the earlier compact prototype.

For each 16-bit field `i`, let `qᵢ` be the probability it is present with a value and `pᵢ` the conditional probability it fits one byte:

```text
Expected saving per record = Σ(qᵢ × pᵢ) bytes
```

There is no selector-byte break-even threshold for I16/U16. A sparse update containing one qualifying integer saves exactly one byte. A NULL/ABSENT value gains nothing and requires no integer-width check. A non-fitting value adds no bytes but still incurs the encoder's check.

For uniform full-range U16 **or** I16 values, only 256 of 65,536 values fit: probability 1/256, or 0.390625%. Fifteen present fields then save only about 0.059 bytes per record on average. Cardinality alone does not imply a high shortening probability; the actual value distribution matters.

## Workloads and controls

- Controlled datasets: 100,000 records each; signed and unsigned; 0%, 25%, 75%, 100% targeted fitting probability plus uniform full-range inputs; four shapes = 40 cases.
- `dense8`: eight-position definition, event UUID omitted from population, seven present 16-bit integers.
- `dense16`: sixteen-position definition, event UUID omitted, fifteen present 16-bit integers.
- `sparse1` / `sparse2`: sixteen-position definition, one or two present changed integer fields; all other fields ABSENT. These isolate changed-value population payloads and do not add an identity envelope.
- Existing mixed fixture: full and sparse, unsigned and signed, one million records each = four additional cases. Same smallest-feasible integer declarations, strings, dictionary references, and identity envelope as the preceding reports. Only the remaining U16/I16 fields gain this extension.
- The probability cases assume a **legitimate 16-bit contract**. 100% fitting is an observed-window best case, not a reason to declare a permanently 8-bit domain as 16 bits. The existing mixed fixture remains the range-sized control.
- `fixed`: optimized fixed encoder/scanner. `compact32`: previous optimized shortening of 32/64-bit integers. `compact16_generic`: extend eligibility to 16 bits while retaining the generic width calculation. `compact16`: dedicated 16-bit implementation.

For the integer-only controlled cases, `fixed` is the appropriate simplest CPU baseline: there are no 32/64-bit values to compress. `compact32` still shows the prior compact machinery's overhead. Both controls are retained so that a slow control does not exaggerate the benefit.

## Main result: 75% fitting probability

Sizes are population bytes per record. Timings are warmed median ns/record. Reused-buffer encoding uses the streaming encoder; scan validates positions and borrows bytes. Scan + access also reads every present numeric value.
''']
rows=[]
for kind in ('u16','i16'):
 for shape in ('dense8','dense16','sparse1','sparse2'):
  c=find(kind+'-75',shape);z=c['population_bytes'];n=c['events']
  rows.append([kind,shape,f'{z["fixed"]/n:.2f} → {z["compact16"]/n:.2f}',f'{savings(c):.2f}%',f'{ns(c,"fixed","encode_reuse"):.1f} → {ns(c,"compact16","encode_reuse"):.1f}',f'{ns(c,"fixed","scan"):.1f} → {ns(c,"compact16","scan"):.1f}',f'{ns(c,"fixed","scan_access"):.1f} → {ns(c,"compact16","scan_access"):.1f}'])
parts.append(table(['Type','Shape','Bytes fixed → compact16','Saving','Reuse encode ns','Scan ns','Scan + access ns'],rows))
parts.append('''
Near-flat scanning does not imply near-flat encoding or numeric access. The encoder still checks every eligible present value. Reading every compact integer may cost more than borrowing its byte slice, especially with mixed one/two-byte values and signed normalization.

## Probability sweep: dense sixteen-position objects

Fifteen present integers; 100,000 records per case. The comparison is fixed-width versus the dedicated compact16 implementation.
''')
rows=[]
for kind in ('u16','i16'):
 for p in ('0','25','75','100','uniform'):
  c=find(kind+'-'+p,'dense16');z=c['population_bytes'];actual=100*c['shortened16_values']/c['eligible16_values']
  rows.append([kind,p,f'{actual:.3f}%',f'{savings(c):.3f}%',f'{ns(c,"fixed","encode_reuse"):.1f} / {ns(c,"compact16","encode_reuse"):.1f}',f'{ns(c,"fixed","scan"):.1f} / {ns(c,"compact16","scan"):.1f}'])
parts.append(table(['Type','Target fitting %','Observed fitting %','Payload saving','Reuse encode fixed / compact16','Scan fixed / compact16'],rows))
parts.append('''
The zero-fitting and uniform cases are essential: they expose CPU costs when little or no bandwidth can be saved. The 100%-fitting case is easier to predict than a mixed-width distribution; it should not be used alone to describe general performance.

## Existing mixed fixture: incremental benefit beyond compact32

One million records each. These are exact payload bytes; timings compare the prior optimized 32/64-bit compact implementation to the new 16-bit extension.
''')
rows=[]
for profile in ('mixed-unsigned','mixed-signed'):
 for shape in ('full','sparse'):
  c=find(profile,shape);z=c['population_bytes']
  rows.append([profile,shape,f'{z["compact32"]:,} → {z["compact16"]:,}',f'{savings(c):.3f}%',f'{ns(c,"compact32","encode_reuse"):.1f} → {ns(c,"compact16","encode_reuse"):.1f}',f'{ns(c,"compact32","scan"):.1f} → {ns(c,"compact16","scan"):.1f}'])
parts.append(table(['Profile','Shape','Bytes compact32 → compact16','Extra saving','Reuse encode ns','Scan ns'],rows))
parts.append('''
The mixed fixture's remaining 16-bit values are mostly IDs spanning thousands of values. They rarely fit in a byte. Its already-small revision/attempt fields remain U8/I8, and duration/amount already benefited from the 32-bit compact mode. The small incremental savings here are expected; they are not evidence that a small-value-heavy workload would behave the same way.

## Generic versus dedicated 16-bit implementation

The generic extension was not close to flat on integer-dense mixed-width inputs. The dedicated path keeps the identical wire layout but uses:

1. A wrapping signed bias and arithmetic range test to choose one versus two bytes, instead of general leading-zero/width selection. No selector packing for I16/U16.
2. Mask-derived width `2 - (state & 1)` for present 16-bit fields. ABSENT/NULL slots bypass width/type decisions early, which matters for sparse events.
3. A native-width copy followed by advancing only the retained width, avoiding a value-dependent one-versus-two-byte copy path. Discarded bytes never enter the encoded frame.

The fresh-buffer specialization reserves **one extra internal scratch-capacity byte**, not one extra encoded byte. This is included in the measured allocation cost and can affect allocator size classes; population/transit byte counts exclude the unused capacity. Reused buffers retain capacity across records.
''')
rows=[]
for kind in ('u16','i16'):
 c=find(kind+'-75','dense16')
 for mode in MODES:
  rows.append([kind,mode,f'{ns(c,mode,"encode"):.1f}',f'{ns(c,mode,"encode_reuse"):.1f}',f'{ns(c,mode,"scan"):.1f}',f'{ns(c,mode,"scan_access"):.1f}'])
parts.append(table(['Type','Implementation','Fresh encode ns','Reuse encode ns','Scan ns','Scan + access ns'],rows))
parts.append('''
These are whole implementation comparisons, not claims that all differences are caused solely by the range check. Copy strategy, field-state handling, and allocation contribute. The earlier first-pass sweep is archived in `../2026-10-16-compact-integer16-first-pass/`; it preceded the ABSENT/NULL scanner fast path. All primary tables here use the final paired run only.

## Assessment

- The implementation optimizations are useful independently of the protocol decision.
- Mask-only I16/U16 is a compact and coherent extension: every successful shortening saves a byte, even on a one-field change, without selector overhead or changes to the declared type.
- It is attractive for legitimate 16-bit domains whose present values are usually small, especially when bandwidth/retained size matters. Larger integer-dense populations compound those byte savings.
- It is not automatically worthwhile for IDs or uniformly distributed values that usually require both bytes. This report does not establish a blanket near-flat CPU cost; encoding and full numeric access remain material tradeoffs.
- An eventual encoder policy could prefer DIRECT for fields that consistently fail to shrink, while decoders continue accepting both valid encodings. Such a policy, production negotiation, Rust/WASM support, and end-to-end collection/storage behavior were not implemented in this experiment.

## Validation and measurement details

Every record in every final dataset was encoded and decoded against its prepared input. Planned and streaming encoders were byte-compared. Generic and dedicated compact16 encoders produced identical bytes. The length difference versus compact32 was asserted to equal **exactly the count of qualifying present 16-bit values**, on every record. Frames without any new shortening were asserted byte-identical to compact32.

Exhaustive correctness tests covered all 65,536 bit patterns as both U16 and I16, including negative boundaries, borrowed re-encoding, no-selector framing, mixed 16/32/64-bit selector positions, NULL/ABSENT, truncation, and malformed inputs. The complete cache suite passed. After the final scanner change, five seconds of differential fuzzing completed 130,452 executions without failure. Earlier development runs are not used as final timing results.

Definitions and decoder plans were compiled before handling records. Controlled generation uses PCG seeds `0x70696c16`, `0x63626f7216`; fitting probability is independently sampled, then values are uniform within the fitting/non-fitting subset. Uniform profiles sample the entire 16-bit range. Exact observed probabilities are recorded, not assumed equal to their targets.

Each case samples 1,024 records evenly across its collection, with eight repetitions per sample and 32 timed rounds. Mode order rotates twice through all 16 operations to balance ordering. Cases run sequentially. p50/p95 raw batch samples, sampled row IDs, definitions, exact sizes, and allocations are retained. Timing is in-memory and warm; no network, IPC, file I/O, trie search, optional object index, or general compression is included. Collection sizes identify input datasets, not measured end-to-end search latency. CBOR is not rerun here because this experiment isolates the incremental I16/U16 extension.

Reused scan/access/encode paths measured zero allocations per record; fresh optimized PIL encoders measured one allocation per record. Small differences remain subject to machine/microbenchmark variation and should not be treated as confidence intervals.

## Reproduction

```sh
python3 benchmarks/storage/2026-10-16-compact-integer16/run.py
python3 benchmarks/storage/2026-10-16-compact-integer16/report.py
```

`metadata.json` contains measured source/executable hashes, commands, and timestamps. `source.tar.gz` preserves the measured Go sources; source hashes are verified at run completion. `results/` contains all 44 raw measurements and `summary.csv` contains all modes/operations with normalized p50/p95 and allocation counts.

Test-only implementation: `go/server/cache/compact_integer16_test.go`, `compact_integer16_specialized_test.go`, `compact_integer16_validation_test.go`, and `compact_integer16_measure_test.go`. Earlier prototype sources and production implementations remain unchanged.
''')
(ROOT/'REPORT.md').write_text('\n\n'.join(parts)+'\n')
with (ROOT/'summary.csv').open('w',newline='') as f:
 w=csv.writer(f);w.writerow(['profile','shape','events','mode','operation','population_bytes','fitting_fraction','p50_ns_per_record','p95_ns_per_record','allocations_per_record'])
 for c in CASES:
  assert c['all_records_validated'] and c['exact_one_byte_saving_verified']
  z=c['population_bytes'];assert z['compact32']-z['compact16']==c['shortened16_values'];assert z['compact16']==z['compact16_generic']
  for k,v in c['codec'].items():
   m,op=k.split('/');n=c['records_per_timing_sample']
   w.writerow([c['profile'],c['shape'],c['events'],m,op,z[m],c['shortened16_values']/c['eligible16_values'],v['p50_ns']/n,v['p95_ns']/n,c['allocations_per_record'][k]])
   if op!='encode':assert c['allocations_per_record'][k]==0,(c['profile'],c['shape'],k)
   else:assert c['allocations_per_record'][k]==1,(c['profile'],c['shape'],k)
print('Final report and summary written; all 44 cases verified.')

# Preserve the protocol correction when regenerating this historical report.
_report=ROOT/"REPORT.md"
_notice='> **Superseded for the intended PIL design.** This experiment repurposed `11 OFFSET` for compact integers. The field-state bits are reserved for DIRECT/OFFSET, so its size and latency results do not describe the requested separate width mask. See the [corrected benchmark](../2026-10-16-separate-width-mask/REPORT.md). Historical data is retained for audit.\n\n'
if not _report.read_text().startswith("> **Superseded"):
 _report.write_text(_notice+_report.read_text())
