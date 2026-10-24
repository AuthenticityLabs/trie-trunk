"""Render the recorded before/after observations, without selecting samples."""
import json
from pathlib import Path

root = Path(__file__).parent / "2026-10-12"

def load(folder, name):
    return json.loads((root / folder / (name + ".json")).read_text())

def ms(v):
    return f"{v / 1e6:,.3f}"

lines = [
    "# Storage prototype: before and after",
    "",
    "Measured 2026-10-12 on the same development Mac (darwin/arm64, 10 logical CPUs, Go 1.26.4).",
    "The existing format-4 source and release reader were frozen before runtime changes. Raw reports retain every sample and matching logical-fixture hashes.",
    "",
    "**This is a prototype comparison, not a production rollout or a layout-only speed claim.** The baseline returns PIL through the existing resident Rust reader over local IPC. The new reader is an in-process Go API. Neither timing includes HTTP, browser rendering, or an intentionally cold disk. Both use the OS page cache; there are nine warm query observations after a warm-up.",
    "",
    "The fixture contains 12 fields across 30 days: UUIDv7 event identity, U64 object/revision identities, duration, six mapped classification fields, amount, and customer ID. The sparse control retains its metadata envelope and one changed business field; it is not a two-field total wire frame. The definition and source records are prepared before ingestion timing. All records use one write branch for equivalence; the `errors` query is a field predicate, not an automatic branch classifier.",
    "",
    "## Size and durable write/reopen time",
    "",
    "MB means 1,000,000 bytes. Sizes include all emitted metadata, summaries, and trie statistics, but not hypothetical filesystem allocation or compression.",
    "",
    "| Events | Before MB | After MB | Size change | Before ingest s | After ingest s | Before reopen ms | After reopen ms |",
    "| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |",
]
for n in (50000, 500000, 1000000):
    name = f"full-{n}-aggregates"
    b, a = load("baseline", name), load("after", name)
    assert b["fixture_sha256"] == a["fixture_sha256"]
    assert b["aggregate_count"] == a["aggregate_count"] == n
    assert b["aggregate_sum"] == a["aggregate_sum"]
    before, after = sum(b["logical_bytes"].values()), sum(a["logical_bytes"].values())
    lines.append(f"| {n:,} | {before/1e6:.3f} | {after/1e6:.3f} | {100*(after/before-1):+.1f}% | {b['ingest_ns']/1e9:.3f} | {a['ingest_ns']/1e9:.3f} | {ms(b['reopen_ns'])} | {ms(a['reopen_ns'])} |")
lines += [
    "",
    "The baseline creates **4,325 files**; this prototype creates **12** without the optional object-history index. The legacy fixture uses hourly resource partitions, so its file creation/sync cost is substantial even at 50,000 events. Ingest includes final durable close; reopen includes recovery. These are single ingest/reopen observations, not percentiles.",
    "",
    "The size increase is real. Legacy mature rows are 26 bytes plus block framing, with object/revision values in compact CBOR population. New live rows are **52 bytes**: moving two U64 identities into the index adds 16 bytes, explicit start/end adds 8, and identity-state flags add 2. Fixed-width PIL scalars can also be larger than small CBOR integers. No sealed end-only row compaction or stream compression is credited here.",
    "",
    "## Warm standalone PIL query latency",
    "",
    "All times below are milliseconds. A predicate query has approximately 5% matches.",
    "",
    "| Events | Query | Before p50 | After p50 | Before p95 | After p95 |",
    "| ---: | --- | ---: | ---: | ---: | ---: |",
]
for n in (50000, 500000, 1000000):
    b, a = load("baseline", f"full-{n}-aggregates"), load("after", f"full-{n}-aggregates")
    for key in ("all", "day", "three_hours", "errors"):
        x, y = b["query_latency"][key], a["query_latency"][key]
        lines.append(f"| {n:,} | {key} | {ms(x['p50_ns'])} | {ms(y['p50_ns'])} | {ms(x['p95_ns'])} | {ms(y['p95_ns'])} |")
lines += [
    "",
    "The first native iteration had a million-event predicate regression. Advancing the merge heap's root in place, instead of popping/reinserting every record, and caching reference decisions by ordinal removed it. The original observations remain under `after-initial/`; final observations above are under `after/`.",
    "",
    "## Optional object history and sparse control",
    "",
]
a, o = load("after", "full-50000-aggregates"), load("after", "full-50000-objects")
extra = sum(o["logical_bytes"].values()) - sum(a["logical_bytes"].values())
lines.append(f"At 50,000 events, enabling `nuts.idx` adds {extra:,} logical bytes ({extra/50000:.2f} bytes/event) and one file. Looking up the five revisions of one object takes {ms(a['query_latency']['object_history']['p50_ns'])} ms p50 with the index disabled versus {ms(o['query_latency']['object_history']['p50_ns'])} ms with it enabled. Both are native Go API calls. In-memory object-directory overhead is not included in the disk figure.")
b, a = load("baseline", "sparse-50000-aggregates"), load("after", "sparse-50000-aggregates")
assert b["fixture_sha256"] == a["fixture_sha256"]
lines += ["", f"The 50,000-event sparse control uses {sum(b['logical_bytes'].values())/1e6:.3f} MB before and {sum(a['logical_bytes'].values())/1e6:.3f} MB after; full-window p50 is {ms(b['query_latency']['all']['p50_ns'])} ms before and {ms(a['query_latency']['all']['p50_ns'])} ms after.", "", "## Native range collection", "", "This API delivers source index/population bytes directly, with checksums, and performs no per-event PIL reconstruction. These are chunk-body sizes; manifest/control framing, HTTP, and compression are excluded.", "", "| Dataset | Request | Cold index bytes | Cold population bytes | Repeated index/population bytes | Cold p50 ms | Cached p50 ms |", "| ---: | --- | ---: | ---: | ---: | ---: | ---: |"]
for n in (50000, 500000, 1000000):
    a = load("after", f"full-{n}-aggregates")
    for query in ("day", "three_hours"):
        cold, hot = a["native_chunk_bytes"][query+"_cold"], a["native_chunk_bytes"][query+"_cached"]
        repeated = hot.get("index", 0) + hot.get("population", 0)
        assert repeated == 0
        lines.append(f"| {n:,} | {query} | {cold.get('index',0):,} | {cold.get('population',0):,} | {repeated} | {ms(a['native_collection_latency'][query+'_cold']['p50_ns'])} | {ms(a['native_collection_latency'][query+'_cached']['p50_ns'])} |")
lines += [
    "",
    "A three-hour request deliberately obtains the **whole day's index**, then only three hours of population. Its first transfer can therefore exceed a narrow standalone PIL result. The benefit is subsequent local selection and population reuse, not a promise that every initial request sends fewer bytes. A repeated request still emits state/manifest metadata (55 state-body bytes in this fixture); only repeated index/population bytes are zero.",
    "",
    "## Reproduce and interpret",
    "",
    "- `go/cmd/storage-bench` supports `--backend legacy|cache`, `--events`, `--shape full|sparse`, `--aggregates`, `--object-index`, and `--samples`.",
    "- The legacy runner needs `trunk-search` beside it or on PATH and permission to open a local Unix socket.",
    "- `baseline/metadata.json` and `baseline/source.tar.gz` identify the frozen pre-change source. Benchmark executables are ignored build products.",
    "- `source-metadata.json` hashes the native implementation/runner used for the final reports. `report.py` generates this report without discarding samples.",
    "- Subsequent correctness fixes clean disabled object indexes during retention and exclude stale-generation object locators. Those maintenance paths were not exercised by the timed matrix; the archived benchmark source remains available.",
    "- Raw JSON files contain sample-level latencies, bytes by extension, file counts, fixture hashes, aggregate totals, runtime details, and caveats.",
    "- Count and duration-sum results match across implementations. Unit tests separately check sparse duration samples, branch separation, late corrections, rollups, recovery, retention, and duplicate delivery.",
    "",
    "Production HTTP/SharedWorker integration, a controlled same-runtime layout comparison, physical-disk-cold tests, concurrency, memory budgets, and migration fidelity are **not established by these measurements**. The running application has not been switched to this prototype.",
]
(root / "REPORT.md").write_text("\n".join(lines)+"\n")
