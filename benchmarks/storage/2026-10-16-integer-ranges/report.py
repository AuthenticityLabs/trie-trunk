"""Render corrected codec comparisons from checked raw measurements."""
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
COUNTS = (50000, 500000, 1000000)
PROFILES = ("range-sized", "full-u64")
SHAPES = ("full", "sparse")


def read(profile, shape, n):
    return json.loads((ROOT / "results" / profile / f"{shape}-{n}.json").read_text())


def mb(n):
    return f"{n / 1e6:.3f}"


def ms(n):
    return f"{n / 1e6:.3f}"


rows = [(p, s, n, read(p, s, n)) for p in PROFILES for s in SHAPES for n in COUNTS]
for profile, shape, n, d in rows:
    assert d["events"] == n and d["sparse"] == (shape == "sparse")
    assert d["integer_profile"] == profile
    assert d["format"] == 6 and d["row_width"] == 26 and d["index_bytes_each"] == n * 26
    assert not d["aggregates"] and not d["object_index"] and d["object_index_bytes"] == 0
    assert d["matched_events"]["all"] == n and d["fixture_sha256"] == d["result_hashes"]["all"]
    assert d["output_contract"] == "standalone PIL"
    assert d["codec_batch_events"] == 1024
    assert d["codec_batch_rows"] == [i * (n - 1) // 1023 for i in range(1024)]
    fields = d["definition"]["fields"]
    wanted = {1: "U16", 2: "U8", 3: "U32", 8: "U32", 9: "U16", 11: "U8"}
    for field, kind in wanted.items():
        assert fields[field]["type"] == (kind if profile == "range-sized" else "U64")
    for field in (4, 5, 6):
        assert fields[field]["type"] == "STRING" and len(fields[field]["values"]) <= 256
    for modes in d["query"].values():
        for timing in modes.values():
            assert len(timing["samples_ns"]) == 9
    for timing in d["codec"].values():
        assert len(timing["samples_ns"]) == 21

lines = []
write = lines.append


def table(headers, values):
    write("| " + " | ".join(headers) + " |")
    write("| " + " | ".join("---" for _ in headers) + " |")
    for value in values:
        write("| " + " | ".join(map(str, value)) + " |")
    write("")


write("# CBOR and PIL with integer types matched to their data ranges")
write("")
write("Measured 2026-10-16 on " + rows[0][3]["platform"] + " with " + rows[0][3]["go"] + ". This corrects the benchmark fixture, not the production codecs or storage format.")
write("")
write("The earlier fixture assigned U64 to small object IDs, revisions, durations, amounts and customer IDs, and U32 to attempts 0–3. Its size result favored CBOR's adaptive integer widths. That is a valid oversized-schema stress case, but it was not an appropriate general demonstration of PIL versus CBOR. The corrected default uses the smallest supported unsigned type that covers each generator domain; a separate full-U64 case samples across the 64-bit range. Historical samples remain available and are not relabeled as corrected measurements.")
write("")
write("## Integer profiles")
write("")
table(["Field", "Range-sized generator domain", "Range-sized type", "Full-U64 type/domain"], [
    ["id", "0–9,999", "U16", "U64 / uniform 0–2^64−1"],
    ["revision_id", "1–100 at 1M records (1–5 at 50k; 1–50 at 500k)", "U8", "U64 / uniform 0–2^64−1"],
    ["duration", "50–100,049", "U32", "U64 / uniform 0–2^64−1"],
    ["amount", "0–99,999", "U32", "U64 / uniform 0–2^64−1"],
    ["customer_id", "0–4,999", "U16", "U64 / uniform 0–2^64−1"],
    ["attempt", "0–3", "U8", "U64 / uniform 0–2^64−1"],
])
write("Full-U64 uses deterministic math/rand/v2 PCG samples with seeds `0x70696c, 0x63626f72`; it is a codec type-range test, not a claim that random revisions or enormous attempt counts model application behavior. Finite samples do not enumerate all 2^64 values. The range-sized schema is fixed before generation/timing, not changed per record. Revision width automatically grows if a larger requested dataset requires it.")
write("")
write("Both profiles retain UUID event IDs, two dynamic strings, and the same three U8 dictionary codes. Full events contain 12 fields. Sparse events retain event/object/revision identity plus state and, on alternating records, amount. The index holds the event UUID and REF16; full populations omit their shared trie fields, while sparse populations carry no trie reference. Thus one or two changed fields does not mean one or two total fields.")
write("")
write("The duration field is an ordinary scalar in these codec fixtures. Its optional aggregation role is unassigned in both profiles because the current production aggregation contract requires U64. Aggregates and replay indexing are disabled. No production contract was relaxed to obtain these measurements.")
write("")
write("## Population size")
write("")
table(["Profile", "Shape", "Events", "CBOR MB", "PIL MB", "PIL reduction"], [
    [p, s, f"{n:,}", mb(d["population_bytes"]["cbor"]), mb(d["population_bytes"]["pil"]),
     f'{100 * (1 - d["population_bytes"]["pil"] / d["population_bytes"]["cbor"]):.1f}%'] for p, s, n, d in rows
])
write("MB means 1,000,000 uncompressed population bytes, excluding file headers, common 26-byte/event indexes, map/trie metadata and transport framing. Both codecs encode the same values and field states within each row of this table. The two integer profiles intentionally contain different numeric values. No compression is enabled.")
write("")
write("## Encode and decode only")
write("")
write("Definitions, positional CBOR descriptors and encoder inputs are cached before timing. Each measurement uses 1,024 evenly spaced records from the entire collection (including its first and last records), with 21 warm repetitions. This replaces the prior first-1,024 sampling, which disproportionately represented early revisions. Values below are median batch time divided by record count; no disk access, search traversal, transmission or JSON expansion is included.")
write("")
table(["Profile", "Shape", "Collection size", "CBOR encode ns/event", "PIL encode ns/event", "CBOR direct/reuse decode ns/event", "PIL reuse scan ns/event"], [
    [p, s, f"{n:,}"] + [f'{d["codec"][key]["p50_ns"] / d["codec_batch_events"]:.1f}' for key in (
        "cbor_encode_1024", "pil_encode_1024", "cbor_direct_reuse_decode_1024", "pil_reuse_decode_1024")]
    for p, s, n, d in rows
])
write("CBOR encoding uses the existing generic encoder with already-built numeric-key maps; PIL encoding uses already-built positional values. Building either input representation is excluded. Encoding includes each implementation's output allocation. Direct CBOR decode builds no intermediate map; both reused decoder modes retain their scratch buffers and borrow text. These are implementation/API measurements, not theoretical codec limits. Raw files retain fresh-buffer and map-decoder controls and allocation counts.")
write("")
write("## Same-reader search producing PIL")
write("")
table(["Profile", "Shape", "Events", "CBOR → PIL ms", "PIL scan + encode ms", "PIL span assembly ms"], [
    [p, s, f"{n:,}"] + [ms(d["query"]["all"][mode]["p50_ns"]) for mode in ("cbor_direct_reuse", "pil_reuse", "pil")]
    for p, s, n, d in rows
])
write("All modes use the same 26-byte index, time/run traversal, 1 MiB population read-ahead policy and no-op result callback. Nine warm samples rotate mode order. Output bytes and counts are verified against the original PIL inputs outside timing. PIL span assembly copies already-validated encoded fields; CBOR must be interpreted to produce PIL. This table does not compare an all-CBOR transport with an all-PIL transport. Native CBOR replication, HTTP and browser rendering remain unmeasured here. No cold-disk or production p95 claim is made.")
write("")
write("## Interpretation")
write("")
for profile in PROFILES:
    values = [read(profile, shape, 1000000) for shape in SHAPES]
    reductions = [100 * (1 - d["population_bytes"]["pil"] / d["population_bytes"]["cbor"]) for d in values]
    write(f"- **{profile}, one million events:** PIL population is {reductions[0]:.1f}% smaller for full events and {reductions[1]:.1f}% smaller for sparse events.")
write("- The prior claim that these fixtures demonstrate a CBOR storage-size advantage does not survive matching PIL integer widths to their generated ranges. The original measurements still describe their original oversized schema, but should not guide the general format decision.")
write("- Strings, dictionaries and sparse field membership were held constant. No format-wide conclusion about all object definitions or all-CBOR end-to-end latency follows from these two profiles.")
write("")
write("## Reproduction and validation")
write("")
write("Run `python3 benchmarks/storage/2026-10-16-integer-ranges/run.py` from the repository. It compiles one test binary and runs all twelve cases sequentially, writing raw samples, complete field definitions, fixture/result hashes and sampled row numbers. `metadata.json` records the toolchain, source/executable hashes and commands; `source.tar.gz` preserves the Go sources used. `report.py` validates the contracts before rendering these tables.")
write("")
write("`TRUNK_MEASURE_INTEGERS=range-sized` is now the default for codec/native measurements; `full-u64` selects the range stress case. `legacy-wide` explicitly reproduces the former field/value choices. The historical aggregate fixture retains its existing U64 aggregation roles. Correctness tests cover width boundaries, revision widening, deterministic generation, high-bit U64 samples, full/sparse field membership, U8 dictionary references and CBOR/PIL round trips.")
(ROOT / "REPORT.md").write_text("\n".join(lines) + "\n")
