"""Render the corrected experiment. Historical OFFSET-overload results excluded."""
import csv,json
from pathlib import Path
ROOT=Path(__file__).resolve().parent
CASES=[json.loads(p.read_text()) for p in sorted((ROOT/'results').glob('*/*.json'))]
assert len(CASES)==44
MODES=('fixed','width32','width16')
def ns(c,m,op):return c['codec'][m+'/'+op]['p50_ns']/c['records_per_timing_sample']
def find(p,s):return next(c for c in CASES if c['profile']==p and c['shape']==s)
def table(headers,rows):return '\n'.join(['| '+' | '.join(headers)+' |','|'+'|'.join(['---']*len(headers))+'|']+['| '+' | '.join(map(str,r))+' |' for r in rows])
def savings(c,base='fixed'):
 z=c['population_bytes'];return 100*(1-z['width16']/z[base])
parts=['''# Corrected PIL integer benchmark: separate width mask

The earlier compact experiments reused `11 OFFSET` for shortened integers. That conflicts with the intended field-state semantics. Those measurements are preserved as historical experiments, but their size/performance conclusions do not apply to this design. **This run keeps all field-state bits unchanged and counts the entire separate width mask.** It replaces the earlier I16/U16 comparison, including its 32/64-bit control.

## Wire layout measured

```text
[field-state mask][packed width mask][direct values][end offsets][variable values]
```

Field-state mask: `00 ABSENT`, `01 NULL`, `10 DIRECT`, `11 OFFSET`. All integer values stay DIRECT. Definitions retain ordinary strict integer types and their declared range/signedness; no adaptive type is introduced.

| Eligible type | Width selector | Choices |
|---|---:|---|
| U16 / I16 | 1 bit | 0 = 1 byte; 1 = 2 bytes |
| U32 / I32 | 2 bits | 00 = 1 byte; 01 = 2; 10 = 4; 11 invalid |
| U64 / I64 | 2 bits | 00 = 1 byte; 01 = 2; 10 = 4; 11 = 8 |

Selectors are packed MSB-first in definition order for **present DIRECT eligible integers**, including values stored at their native width. ABSENT/NULL fields consume no selectors. U8/I8, UUIDs, dictionary references, and variable fields do not participate. There is no separate length byte: definition plus field-state mask determines selector count. Final unused width bits must be zero. Signed values use two's complement and are sign-extended when accessed.

The benchmark selects a known wire profile before processing a dataset. This is not an optional mask whose presence the decoder guesses from bytes, nor is it silently compatible with the original fixed-width decoder. Profile negotiation is outside the measured record bytes, as with the cached definition. No per-frame compression-disable flag, fallback to fixed framing, or adaptive policy was implemented; skipping the plane on non-fitting records would require a defined signal.

## Byte accounting and the sparse correction

```text
width bytes = ceil((present16 + 2*present32 + 2*present64) / 8)
net saving = sum(declared width - encoded width) - width bytes
```

Counts include only the types enabled by the tested profile. Thus, for an isolated 16-bit payload:

| Present integers | Fit in one byte | Net result versus fixed |
|---:|---:|---|
| 1 | 1 | Break-even: save 1, spend 1 |
| 1 | 0 | Grow by 1 byte |
| 2 | 2 | Save 1 byte |
| 2 | 1 | Break-even |
| 2 | 0 | Grow by 1 byte |
| 15 | 15 | Save 13 bytes: save 15, spend 2 |

A one-field sparse record is **never smaller** in this isolated case. It is flat if the value fits and larger if it does not. Multiple values can amortize the mask. In a mixed record, extra 16-bit bits can use spare capacity in an existing 32/64-bit mask byte; extending eligibility need not always add a byte.

For k present 16-bit integers with independent fitting probability p, expected saving is `k*p - ceil(k/8)` bytes. Fifteen fields break even at p = 2/15 (~13.3%); two at 50%; one at 100%. Uniform full-range signed and unsigned 16-bit values fit with probability 1/256, so this is a poor distribution for automatic shortening.

## Paired workloads and methodology

- 40 controlled cases: 100,000 records each; U16 and I16; fitting probabilities 0%, 25%, 75%, 100%, plus uniform full-range; dense8/dense16/sparse1/sparse2.
- dense8 has seven present integer values plus an omitted UUID position. dense16 has fifteen plus an omitted UUID. Sparse1/2 use the sixteen-position definition with only one/two changed fields and no identity envelope. State masks remain 2 bytes or 4 bytes respectively.
- Four mixed cases: one million records each; full/sparse and signed/unsigned; the preceding range-sized fixture, including strings/dictionary fields and its existing identity attributes. Sparse mixed records have three/four present values, unlike the isolated one/two-field probes.
- 16-bit probability controls assume a legitimate 16-bit domain. All-small is an observed-window best case; permanently byte-sized fields should remain U8/I8.
- `fixed`: matched optimized fixed-width control. `width32`: separate selectors for 32/64 only. `width16`: same algorithm extended to 16. No data or timing from the old overloaded-mask format enters these tables.
- Planned encoder sizes before allocation. Reused-buffer encoder reserves selector space, writes values, and closes unused selector space before adding variable values. 16-bit width choice retains the arithmetic signed/unsigned range check. Fields and lookup tables are compiled once before records.
- Every generated record is validated, but timing uses 1,024 evenly distributed records, eight repetitions and 24 rotated rounds (two rotations through twelve operations). These are warm per-record codec measurements, **not elapsed collection/search or storage timings**. No IPC, disk, transport, general compression, indices, or CBOR comparison is included.

## Main comparison: 75% fitting

Population bytes per record, including both masks. Positive saving means smaller; negative means larger. Median ns/record; scan validates and borrows slices, while scan + access additionally consumes all present numeric values.
''']
rows=[]
for kind in ('u16','i16'):
 for shape in ('dense8','dense16','sparse1','sparse2'):
  c=find(kind+'-75',shape);z=c['population_bytes'];n=c['events']
  rows.append([kind,shape,f'{z["fixed"]/n:.3f} → {z["width16"]/n:.3f}',f'{savings(c):+.2f}%',f'{ns(c,"fixed","encode_reuse"):.1f} → {ns(c,"width16","encode_reuse"):.1f}',f'{ns(c,"fixed","scan"):.1f} → {ns(c,"width16","scan"):.1f}',f'{ns(c,"fixed","scan_access"):.1f} → {ns(c,"width16","scan_access"):.1f}'])
parts.append(table(['Type','Shape','Bytes fixed → width16','Net saving','Reuse encode ns','Scan ns','Scan + access ns'],rows))
parts.append('## Probability sweep\n\nDense16 has fifteen present integers; sparse1 has one. Timings and sizes use the paired fixed control.')
rows=[]
for kind in ('u16','i16'):
 for p in ('0','25','75','100','uniform'):
  for shape in ('dense16','sparse1'):
   c=find(kind+'-'+p,shape);z=c['population_bytes']
   rows.append([kind,p,shape,f'{100*c["shortened16_values"]/c["eligible16_values"]:.3f}%',f'{savings(c):+.3f}%',f'{ns(c,"fixed","encode_reuse"):.1f} / {ns(c,"width16","encode_reuse"):.1f}',f'{ns(c,"fixed","scan"):.1f} / {ns(c,"width16","scan"):.1f}'])
parts.append(table(['Type','Fitting target','Shape','Observed fitting','Net saving','Reuse encode fixed / width16','Scan fixed / width16'],rows))
parts.append('## Mixed fixture: incremental effect of enabling 16-bit shortening\n\nExact population bytes for one million records. Width32 is the **corrected separate-mask** control, not the historical overloaded-state implementation.')
rows=[]
for profile in ('mixed-unsigned','mixed-signed'):
 for shape in ('full','sparse'):
  c=find(profile,shape);z=c['population_bytes'];w=c['width_mask_bytes']
  rows.append([profile,shape,f'{z["fixed"]:,}',f'{z["width32"]:,} → {z["width16"]:,}',f'{savings(c,"width32"):+.3f}%',f'{w["width32"]:,} → {w["width16"]:,}',f'{ns(c,"width32","encode_reuse"):.1f} → {ns(c,"width16","encode_reuse"):.1f}',f'{ns(c,"width32","scan"):.1f} → {ns(c,"width16","scan"):.1f}'])
parts.append(table(['Profile','Shape','Fixed bytes','Width32 → width16 bytes','Incremental saving','Width-mask bytes 32 → 16','Reuse encode ns','Scan ns'],rows))
parts.append('''
The mixed fixture's remaining 16-bit values mostly span thousands and rarely shrink. Whether the extension pays depends on selector-byte rounding as well as the fitting probability. In sparse records without a present 32/64-bit value, enabling 16-bit shortening introduces a mask that otherwise did not exist. This can outweigh savings in other records.

## Full operation timings for the unsigned controls

Fresh encoding allocates output; reused encoding retains capacity. Scan + access is a more relevant comparison than scan alone if every integer will be consumed.
''')
rows=[]
for profile,shape in [('u16-75','dense16'),('u16-75','sparse1'),('mixed-unsigned','full'),('mixed-unsigned','sparse')]:
 c=find(profile,shape)
 for m in MODES:
  rows.append([profile,shape,m]+[f'{ns(c,m,op):.1f}' for op in ('encode','encode_reuse','scan','scan_access')])
parts.append(table(['Profile','Shape','Mode','Fresh encode ns','Reuse encode ns','Scan ns','Scan + access ns'],rows))
parts.append('''
## Assessment

The previous claim of guaranteed one-byte savings for isolated U16 shortening is withdrawn. Preserving DIRECT/OFFSET makes the separate mask a real cost, even when no value can be shortened. Small-value-heavy dense records still benefit in size, but sparse or uniformly distributed 16-bit values are much less attractive. A blanket extension to every U16/I16 field is not supported by these workloads. Enabling it selectively at a negotiated definition/profile level could avoid poor cases; that policy is not measured here.

CPU is also not free. The report distinguishes encode, scan, and numeric access and supplies the actual paired costs rather than carrying forward earlier claims about near-flat performance. This is one implementation, not a proof of the fastest achievable encoder; further optimization must preserve these mask costs. Small differences between timings are subject to machine variation, and p95 samples are not confidence intervals.

## Correctness and reproduction

- Full cache test suite passed.
- Exhaustive U16 and I16 coverage across all 65,536 bit patterns verifies selectors, sizes, round trips, unchanged states, and borrowed-value re-encoding.
- Golden sparse and mixed-selector layouts, cross-byte packing, ABSENT/NULL, malformed integer OFFSET, invalid U32 widths, nonzero padding, oversize inputs and truncation tests passed.
- Five-second fuzz run completed 253,786 executions without failure; it checks malformed input handling, planned/streaming agreement and semantic re-encoding.
- Every record in all 44 measured datasets is checked against an independent signed/unsigned width oracle. Its size is asserted to equal fixed bytes plus rounded width-mask bytes minus saved value bytes. Both encoder strategies must match; the field-state mask must match fixed encoding exactly.
- All reused encode/scan/access paths measured zero allocations per record; fresh planned encoding measured one. Definitions, exact width-mask totals, p50/p95 raw samples, sampled rows, and allocations are retained in JSON and CSV.

```sh
python3 benchmarks/storage/2026-10-16-separate-width-mask/run.py
python3 benchmarks/storage/2026-10-16-separate-width-mask/report.py
```

`metadata.json` records source/executable hashes and commands. `source.tar.gz` preserves measured sources; the runner verifies their hashes at completion. The corrected implementation is confined to `go/server/cache/separate_width_{experiment,validation,measure}_test.go`; production Go/Rust/WASM formats are unchanged. Prior reports now link here and explicitly identify their invalid-for-this-design OFFSET overload.
''')
(ROOT/'REPORT.md').write_text('\n\n'.join(parts)+'\n')
with (ROOT/'summary.csv').open('w',newline='') as f:
 w=csv.writer(f);w.writerow(['profile','shape','events','mode','operation','population_bytes','width_mask_bytes','fitting_fraction','p50_ns_per_record','p95_ns_per_record','allocations_per_record'])
 for c in CASES:
  assert c['all_records_validated'] and c['selector_overhead_verified'] and c['field_states_unchanged']
  z=c['population_bytes'];overhead=c['width_mask_bytes']['width16']-c['width_mask_bytes']['width32']
  assert z['width32']-z['width16']==c['shortened16_values']-overhead
  for k,v in c['codec'].items():
   m,op=k.split('/');n=c['records_per_timing_sample'];alloc=c['allocations_per_record'][k]
   w.writerow([c['profile'],c['shape'],c['events'],m,op,z[m],c['width_mask_bytes'][m],c['shortened16_values']/c['eligible16_values'],v['p50_ns']/n,v['p95_ns']/n,alloc])
   assert alloc==(1 if op=='encode' else 0),(c['profile'],c['shape'],k,alloc)
print('Corrected report and summary written; all 44 cases verified.')
