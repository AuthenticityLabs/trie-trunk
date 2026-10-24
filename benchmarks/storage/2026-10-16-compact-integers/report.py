"""Render the measured mask-override experiment without altering raw results."""
import csv
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
CASES = [json.loads(p.read_text()) for p in sorted((ROOT / 'results').glob('*/*.json'))]
assert len(CASES) == 24
MODES = ('pil_fixed', 'pil_compact', 'pil_fixed64', 'pil_compact64', 'cbor')
NAMES = {'pil_fixed': 'Fixed PIL', 'pil_compact': 'Compact PIL', 'pil_fixed64': 'Fixed PIL, 64-bit contract', 'pil_compact64': 'Compact PIL, 64-bit contract', 'cbor': 'CBOR'}

def ns(c, mode, op):
    return c['codec'][mode + '/' + op]['p50_ns'] / c['records_per_timing_sample']

def shape(c):
    return 'Sparse' if c['sparse'] else 'Dense'

def table(headers, rows):
    return '\n'.join(['| ' + ' | '.join(headers) + ' |', '|' + '|'.join(['---'] * len(headers)) + '|'] + ['| ' + ' | '.join(map(str, row)) + ' |' for row in rows])

def select(profile, n=1_000_000):
    return sorted([c for c in CASES if c['events'] == n and c['profile'] == profile], key=lambda c: c['sparse'])

parts = ['''# PIL integer mask override — isolated prototype

Definitions remain ordinary `U32`, `I32`, `U64`, and `I64`. There is **no adaptive type**. This experiment shortens the wire representation when the value fits a smaller supported width, preserving the declared range and signedness. `U8`, `I8`, `U16`, `I16`, timestamps, dictionary references, and other types retain their existing representation.

**Result:** automatic shortening works, including signed and sparse values. It trades additional codec work for fewer bytes. It is most useful when a legitimately wide declared integer often contains small values; it gives modest additional savings when developers already select the smallest feasible types. Full-range data has no size penalty in this sample, but still incurs CPU overhead.

This is a Go **test-only** codec experiment. Production PIL, Rust/WASM, stored populations, and collection transport have not changed. It is not a compatible extension of the current protocol without negotiation.

## Experimental layout

For eligible integer fields, interpret the existing two-bit field state as:

| Mask | Meaning |
|---|---|
| `00` | ABSENT; no bytes or width selector |
| `01` | NULL; no bytes or width selector |
| `10` | DIRECT; the definition's native width, no selector |
| `11` | COMPACT; shorter integer bytes plus a packed width selector |

For variable fields, `11` continues to mean OFFSET. The definition distinguishes the meanings. An old decoder rejects COMPACT integers.

```text
[field mask]
[packed width selectors, only for COMPACT fields]
[direct values in positional order, including compact integer bytes]
[end offsets for actual variable fields]
[variable values]
```

- U32/I32 compact widths: 1 or 2 bytes, selected by one bit. Otherwise use native 4-byte DIRECT.
- U64/I64 compact widths: 1, 2, or 4 bytes, selected by two bits. Otherwise use native 8-byte DIRECT. Selector `11` is reserved/invalid.
- Selectors are MSB-first, packed across fields, and padded to a byte boundary. No selector is allocated for ABSENT, NULL, or native-width values.
- Signed values use the shortest supported **two's-complement** width and are sign-extended when accessed. No ZigZag conversion.
- A shortening removes at least two payload bytes, so even one extra selector byte leaves a net saving. No speculative compression threshold or multi-record dependency is required for this prototype.
- The scanner borrows integer bytes. It does not allocate a widened object/map; typed access returns the original logical value.
- Frame boundaries remain discoverable from definition, mask, selectors, and the final variable-field end offset. No new outer length prefix.

For example, one `U32` value of 1,000 uses two value bytes plus one selector byte instead of four value bytes, with the existing mask unchanged in length. A `U32` value of 70,000 stays native four bytes because this experiment uses 1/2/4/8-byte widths, not arbitrary byte lengths.

## One-million-record size results

All sizes below are decimal MB of encoded population payload only. Definitions, primary/optional indices, aggregate files, transport headers, and general-purpose compression are excluded equally. A size reduction here is a potential uncompressed storage/transit reduction, not a measured network-speed improvement.
''']
rows=[]
for profile in ('bounded-unsigned','bounded-signed','full-range-unsigned','full-range-signed'):
    for c in select(profile):
        z=c['population_bytes']; saving=100*(1-z['pil_compact']/z['pil_fixed'])
        rows.append([profile,shape(c),f"{z['pil_fixed']/1e6:.3f}",f"{z['pil_compact']/1e6:.3f}",f'{saving:.2f}%',f"{z['cbor']/1e6:.3f}"])
parts.append(table(['Profile','Shape','Fixed PIL MB','Compact PIL MB','PIL saving','CBOR MB'],rows))
parts.append('''
For bounded profiles above, the fixed and compact modes use the **same smallest feasible declared types**. This is the fair primary comparison for a developer who sizes fields carefully. The signed profile alternates values `x` and `-x-1` to exercise signed boundaries; the numeric magnitudes are controlled rather than random small U64 values masquerading as a natural workload.

The additional wide-contract control below uses the same bounded values but declares those six scalar fields as I64/U64. It illustrates the benefit when a wide contract is genuinely required. It is **not** evidence that developers should widen otherwise narrow fields.
''')
rows=[]
for profile in ('bounded-unsigned','bounded-signed'):
    for c in select(profile):
        z=c['population_bytes'];saving=100*(1-z['pil_compact64']/z['pil_fixed64'])
        rows.append([profile,shape(c),f"{z['pil_fixed64']/1e6:.3f}",f"{z['pil_compact64']/1e6:.3f}",f'{saving:.2f}%'])
parts.append(table(['Profile','Shape','Fixed 64-bit MB','Compact 64-bit MB','Saving'],rows))
parts.append('''
## Codec latency at one million records

Median nanoseconds per record, from repeated in-memory batches sampled across the collection. `Scan` discovers/validates positions and borrows scalar bytes. `Scan + access` also reads every present numeric value, including sign extension. These are distinct operations, not two names for full object materialization.
''')
for profile in ('bounded-unsigned','bounded-signed','full-range-unsigned','full-range-signed'):
    parts.append('\n### '+profile+'\n')
    rows=[]
    for c in select(profile):
        for mode in MODES if profile.startswith('bounded') else MODES[:2]+('cbor',):
            rows.append([shape(c),NAMES[mode],f'{ns(c,mode,"encode"):.1f}',f'{ns(c,mode,"scan"):.1f}',f'{ns(c,mode,"scan_access"):.1f}'])
    parts.append(table(['Shape','Codec','Encode ns','Scan ns','Scan + access ns'],rows))
parts.append('''
The compact encoder performs width checks and an additional layout pass; the scanner counts selectors and reads their widths. This is real overhead, including for unshortenable values. The no-COMPACT decoder path delegates to the original scanner after checking the mask. In the measured sparse profiles, direct positional CBOR scanning remains faster than either PIL scanner; compact PIL encoding remains faster than the current CBOR encoder.

All reused scanners and numeric-access paths measured **zero allocations per record**. Encoding starts with an empty output buffer for every record, consistently with the earlier encode measurements; it includes output allocation. Both PIL modes start from prepared positional primitive bytes. CBOR starts from prepared numeric-key maps using the existing generic encoder. Preparation/boxing is outside timing for both, so this compares these APIs, not arbitrary application-object marshaling or an optimized future positional CBOR encoder.

Identical full-range narrow/wide controls can show timing variation despite identical bytes. Small timing differences should be treated cautiously; these are single-process development-machine microbenchmarks, not confidence intervals or production service latency.

## Collection-size sweep

Primary comparison: the same definition, fixed encoding versus automatic compact override. Time values are per record; the 50k/500k/1M labels describe the source collections and their integer ranges. **They are not full-collection elapsed search/collect times.**
''')
rows=[]
for c in sorted(CASES,key=lambda c:(c['profile'],c['sparse'],c['events'])):
    z=c['population_bytes']
    rows.append([c['profile'],shape(c),f"{c['events']:,}",f"{z['pil_fixed']:,} → {z['pil_compact']:,}",f"{ns(c,'pil_fixed','encode'):.1f} → {ns(c,'pil_compact','encode'):.1f}",f"{ns(c,'pil_fixed','scan'):.1f} → {ns(c,'pil_compact','scan'):.1f}"])
parts.append(table(['Profile','Shape','Records','Payload bytes fixed → compact','Encode ns fixed → compact','Scan ns fixed → compact'],rows))
parts.append('''
## Method, coverage, and limits

- Four profiles × dense/sparse × 50,000/500,000/1,000,000 records = **24 cases**. Each uses the same prepared values for all codec variants. Every generated record was decoded and compared against its input in every variant, outside timing.
- A 12-position definition preserves the previous benchmark's scalar, string, and dictionary composition. Dense population payloads have eight present fields; event UUID and three classification fields are excluded as in the prior storage projection. Sparse payloads have two identity fields plus one or two changed fields (state, and amount on alternate records). The mask still describes all twelve positions. These are population projections, not complete standalone event transports.
- Bounded integer domains match the earlier fixture: id 0–9,999; revision 1–ceil(N/10,000); duration 50–100,049; amount 0–99,999; customer 0–4,999; attempt 0–3. Signed stress uses corresponding negative values on alternating positions. Optional object/revision/duration roles and aggregations are unbound so domain-role restrictions do not force wider types. Signed identity values are a codec stress test, not proposed application IDs.
- Uniform full-range inputs use Go `math/rand/v2` PCG, seeds `0x70696c`, `0x63626f72`, one Uint64 draw per scalar; signed cases reinterpret the bits as I64. No shortening occurred in these sampled full-range records. This is a range stress test, not an ordered revision generator.
- Compile/cached definition and direct-CBOR decoder setup occur before handling records. No JSON expansion, CBOR intermediate decode map, IPC, file I/O, trie traversal, network transfer, or compression is timed.
- 1,024 records sampled evenly from each collection, eight batch repetitions per timing sample, 21 samples per mode. Mode order rotates each round. All 24 cases run sequentially; raw samples and p50/p95 are in `results/`.
- Whole `go/server/cache` test suite passed. Focused tests cover signed/unsigned boundaries, state preservation, native byte compatibility, selector packing across bytes, empty/null records, malformed frames, truncation, stream framing, strict declared-width limits, and borrowed decode/re-encode. Five seconds of fuzzing completed 130,944 executions without failure.
- Native arrays, Rust/WASM, definition/protocol negotiation, storage/transport assembly, and end-to-end latency are not implemented or measured by this experiment. Reusing old fixed-width value consumers would be incorrect without typed compact-aware access. The negotiated wire change would require all of those paths to be audited before adoption.

## Assessment

This matches the proposed behavior: **strict definition types with optional compact representation**, rather than a separate adaptive type. It avoids a wire-size penalty when all values need their native width, and leaves sparse absent/null slots without per-value selector overhead.

It does not make integer handling free. For well-sized definitions, the measured byte savings are small enough that the extra encoding/scanning CPU is a meaningful tradeoff. For legitimate wide integer contracts with predominantly small values, the size benefit is much larger. Retain this as an explicit protocol experiment pending a decision on that tradeoff; these results alone do not justify silently changing the production format.

## Reproduction

```sh
python3 benchmarks/storage/2026-10-16-compact-integers/run.py
python3 benchmarks/storage/2026-10-16-compact-integers/report.py
```

`run.py` records source and executable hashes and archives the measured Go sources before the run. `metadata.json` verifies sources stayed unchanged during measurement. Raw JSON files include complete narrow/wide definitions, sampled row numbers, allocation counts, exact byte totals, and timing samples. `summary.csv` includes all modes, sizes, profiles, and p50/p95 normalized per record.

Implementation: `go/server/cache/compact_integer_experiment_test.go`; correctness tests: `compact_integer_experiment_validation_test.go`; measurements: `compact_integer_experiment_measure_test.go`. All are test-only files.
''')
(ROOT/'REPORT.md').write_text('\n\n'.join(parts)+'\n')
with (ROOT/'summary.csv').open('w',newline='') as f:
    writer=csv.writer(f)
    writer.writerow(['profile','shape','events','mode','payload_bytes','operation','p50_ns_per_record','p95_ns_per_record','allocs_per_record'])
    for c in CASES:
        for mode in MODES:
            for op in ('encode','scan','scan_access'):
                v=c['codec'][mode+'/'+op]; divisor=c['records_per_timing_sample']
                writer.writerow([c['profile'],shape(c),c['events'],mode,c['population_bytes'][mode],op,v['p50_ns']/divisor,v['p95_ns']/divisor,c['allocations_per_record'][mode+'/'+op]])
assert all(c['all_records_validated'] for c in CASES)
assert all(c['allocations_per_record'][m+'/'+op] == 0 for c in CASES for m in MODES for op in ('scan','scan_access'))
print('Report and summary written; 24 cases verified.')

# Preserve the protocol correction when regenerating this historical report.
_report=ROOT/"REPORT.md"
_notice='> **Superseded for the intended PIL design.** This experiment repurposed `11 OFFSET` for compact integers. The field-state bits are reserved for DIRECT/OFFSET, so its size and latency results do not describe the requested separate width mask. See the [corrected benchmark](../2026-10-16-separate-width-mask/REPORT.md). Historical data is retained for audit.\n\n'
if not _report.read_text().startswith("> **Superseded"):
 _report.write_text(_notice+_report.read_text())
