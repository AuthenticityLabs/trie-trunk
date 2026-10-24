"""Render the checked, paired measurement report. No third-party dependencies."""
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
COUNTS = (50000, 500000, 1000000)
SHAPES = ("full", "sparse")


def read(stage, shape, count):
    return json.loads((ROOT / stage / f"{shape}-{count}.json").read_text())


def ms(value):
    return f"{value / 1e6:,.3f}"


def mb(value):
    return f"{value / 1e6:,.3f}"


lines = []


def write(text=""):
    lines.append(text)


def table(head, rows):
    write("| " + " | ".join(head) + " |")
    write("| " + " | ".join("---" for _ in head) + " |")
    for row in rows:
        write("| " + " | ".join(map(str, row)) + " |")
    write()


for shape in SHAPES:
    for count in COUNTS:
        before, after, codec = (read(stage, shape, count) for stage in ("before", "after", "codecs"))
        assert before["format"] == 5 and after["format"] == codec["format"] == 6
        assert before["row_width"] == 52 and after["row_width"] == codec["row_width"] == 26
        assert before["fixture_sha256"] == after["fixture_sha256"] == codec["fixture_sha256"]
        assert before["result_counts"] == after["result_counts"]
        assert before["result_hashes"] == after["result_hashes"]
        for query, digest in after["result_hashes"].items():
            assert codec["result_hashes"][query] == digest
            assert codec["matched_events"][query] == after["result_counts"][query]
        for data in (before, after, codec):
            assert data["object_index"] is False and data["object_index_bytes"] == 0
            assert data["aggregates"] is False
        for data in (before, after):
            for query in ("day_cached", "three_hours_cached"):
                assert data["native_bytes"][query].get("index", 0) == 0
                assert data["native_bytes"][query].get("population", 0) == 0

write("# Compact event index and native PIL output")
write()
write("Measured 2026-10-15 on darwin/arm64, Go 1.26.4. This measures the isolated Go storage prototype, not the running server or browser. Earlier reports and raw measurements are preserved.")
write()
write("The revised index is **26 bytes per event**, with object/revision identities in PIL population. At one million events it saves approximately **10 MB total**, without dropping identities or changing their numeric widths. Standalone output now copies encoded spans; native collection retains the stored population and REF16 references. The measured query improvement is real for these warm fixtures, but combines read batching and output assembly, not a codec-only speedup.")
write()
write("## Controls and contracts")
write()
write("- Frozen format-5 baseline versus format 6, both using the same Go store API; no Rust IPC or HTTP in either path.")
write("- Identical 30-day input bytes and result SHA-256 hashes. Full records have 12 fields; sparse records carry event/object/revision identity plus one or two changed fields, with no trie root.")
write("- Definitions/maps, dictionaries, positional CBOR descriptors and inputs are prepared before timing. Mapped fields use U8 in both variants. U64 object/revision IDs, U64 scalar fields and the U32 attempt field are unchanged.")
write("- Replay indexing and aggregates are disabled in every run; guards verify that no `nuts.idx` exists. Retained identity values are still included in storage totals.")
write("- Nine warm query samples; tables use medians unless noted. Raw JSON includes every sample and p95 (the maximum of nine, not a production tail-latency estimate). No OS cache flush, network, browser, compression, or disk-cold claim.")
write("- Before/after runs are sequential on the same host. Codec modes are interleaved within each fixture. Ingest and reopen are single observations, not percentiles. Temporary stores isolate runs.")
write("- Standalone Search emits complete PIL frames and invokes a no-op consumer during timing. Native Collect sends stored index/population chunks; chunk hashing is included. Checks of output digests/counts happen outside timing.")
write()
write("## Storage size")
write()
table(["Shape", "Events", "Before MB", "After MB", "Reduction"], [
    [shape, f"{n:,}", mb(sum(read("before", shape, n)["logical_bytes"].values())), mb(sum(read("after", shape, n)["logical_bytes"].values())),
     f'{100 * (1 - sum(read("after", shape, n)["logical_bytes"].values()) / sum(read("before", shape, n)["logical_bytes"].values())):.1f}%']
    for shape in SHAPES for n in COUNTS])
write("MB means 1,000,000 logical file bytes, including headers, catalog, configuration, trie, statistics and state. These are not filesystem-allocated blocks. Each run has 11 files; neither variant creates an optional object index.")
write()
table(["1M events", "Before index MB", "After index MB", "Before pop MB", "After pop MB"], [
    [shape] + [mb(read(stage, shape, 1000000)["logical_bytes"][ext]) for ext in (".idx", ".pop") for stage in ("before", "after")]
    for shape in SHAPES])
write("The row drops from 52 to 26 bytes. Moving two U64 identities adds 16 bytes to each population, so the net saving is **10 bytes/event**, primarily the removed explicit start (8) and flags (2). Sparse commit metadata differs by another 20 bytes in these fixtures. Do not describe the index's 26 MB reduction as a 26 MB total reduction.")
write()
write("## Standalone PIL search")
write()
table(["Shape", "Events", "Before all ms", "After all ms", "Speed ratio", "After day ms", "After 3h ms"], [
    [shape, f"{n:,}", ms(read("before", shape, n)["standalone_query"]["all"]["p50_ns"]), ms(read("after", shape, n)["standalone_query"]["all"]["p50_ns"]),
     f'{read("before", shape, n)["standalone_query"]["all"]["p50_ns"] / read("after", shape, n)["standalone_query"]["all"]["p50_ns"]:.2f}×',
     ms(read("after", shape, n)["standalone_query"]["day"]["p50_ns"]), ms(read("after", shape, n)["standalone_query"]["three_hours"]["p50_ns"])]
    for shape in SHAPES for n in COUNTS])
write("The baseline performs a file read, population scan and PIL encoding per event. The revised reader uses a bounded 1 MiB read-ahead buffer (larger only for an oversized frame), copies encoded direct fields and the complete offset/value section, and restores the mask/UUID/shared-prefix spans. Query-local shared-prefix plans are cached up to 4,096 slots. This avoids per-event value materialization/encoding for ordinary time queries. Dynamic predicates still need value inspection. Scattered matches and heavily overlapping runs can diminish read-ahead efficiency; this timing fixture is chronological.")
write()
write("## Same-reader CBOR versus PIL")
write()
write("Both populations use the **same 26-byte index, traversal, read-ahead buffer policy, query bounds and standalone PIL output contract**. CBOR retains numeric field IDs and dictionary ordinals; its direct decoder fills positional values without an intermediate map. `cbor_direct_reuse` is the relevant optimized CBOR control. `pil_reuse` retains a scan-and-encode control with the same buffered reads; `pil` is the new encoded-span assembly path. Trusted PIL population was validated on ingest and its committed CRC checked on reopening; assembly does not repeat full semantic validation. CBOR still must be interpreted to produce PIL.")
write()
table(["Shape", "Events", "CBOR direct/reuse ms", "PIL scan/encode ms", "PIL assemble ms", "CBOR / assembly"], [
    [shape, f"{n:,}"] + [ms(read("codecs", shape, n)["query"]["all"][mode]["p50_ns"]) for mode in ("cbor_direct_reuse", "pil_reuse", "pil")] +
    [f'{read("codecs", shape, n)["query"]["all"]["cbor_direct_reuse"]["p50_ns"] / read("codecs", shape, n)["query"]["all"]["pil"]["p50_ns"]:.2f}×']
    for shape in SHAPES for n in COUNTS])
write("This comparison isolates the new output strategy from the large reduction in per-record file I/O. It does not establish that every PIL primitive decodes faster than every CBOR primitive. Raw results also retain map-based CBOR, fresh direct-CBOR frames, and pure encode/scan controls.")
write()
table(["1M events", "CBOR pop MB", "PIL pop MB", "CBOR direct scan ns/event", "PIL scan ns/event"], [
    [shape, mb(read("codecs", shape, 1000000)["population_bytes"]["cbor"]), mb(read("codecs", shape, 1000000)["population_bytes"]["pil"]),
     *[f'{read("codecs", shape, 1000000)["codec"][mode]["p50_ns"] / read("codecs", shape, 1000000)["codec_batch_events"]:.1f}' for mode in ("cbor_direct_reuse_decode_1024", "pil_reuse_decode_1024")]]
    for shape in SHAPES])
write("The pure scan controls use the first 1,024 projected records and 21 repetitions, excluding disk I/O and output. Population bytes cover the entire dataset. CBOR still has a size advantage for these deliberately unchanged, small-valued U64 fields. The new storage path wins latency without erasing that compression tradeoff.")
write()
write("## Native replication")
write()
table(["1M full events", "Before ms", "After ms", "Before body MB", "After body MB"], [
    [q, ms(read("before", "full", 1000000)["native_collection"][q]["p50_ns"]), ms(read("after", "full", 1000000)["native_collection"][q]["p50_ns"]),
     mb(sum(read("before", "full", 1000000)["native_bytes"][q].values())), mb(sum(read("after", "full", 1000000)["native_bytes"][q].values()))]
    for q in ("day_initial", "three_hours_initial", "day_cached", "three_hours_cached")])
write("A three-hour request intentionally collects the whole day's index and only three hours of population. Population planning now takes contiguous candidate ranges from the time/run index instead of examining every day's row. Cached requests send **zero index/population bytes**; 55 bytes of state body remain in this fixture. Body totals exclude the manifest and future transport framing. Format 6 adds a row number and initial population offset per transmitted index chunk in the manifest (two U64 values); no per-event start is reintroduced. Native chunk tests verify exact stored bytes, chunk boundaries, coverage gaps and late-arrival fills.")
write()
write("## Ingest and reopen")
write()
table(["Shape", "Events", "Before ingest s", "After ingest s", "Before reopen ms", "After reopen ms"], [
    [shape, f"{n:,}", f'{read("before", shape, n)["ingest_ns"] / 1e9:.3f}', f'{read("after", shape, n)["ingest_ns"] / 1e9:.3f}',
     ms(read("before", shape, n)["reopen_ns"]), ms(read("after", shape, n)["reopen_ns"])]
    for shape in SHAPES for n in COUNTS])
write("Ingest includes durable close/sync and statistics; reopen includes committed CRC checks and mapping. Ingest still validates incoming PIL and creates its stored projection. The no-per-event-marshalling claim applies to native collection and the new standalone output path, not to ingestion. No ingest improvement should be inferred from small differences in these single observations.")
write()
write("## Correctness and rollout")
write()
write("Storage and CLI tests pass, including race detection for the cache package. Full and sparse frames ingested/reopened/searched through the CLI match their input bytes and decode successfully with the shared Rust PIL codec. New checks cover 26-byte rows, identity ABSENT/NULL preservation, arbitrary event-field position, arrays/CBOR, extended offset boundaries, literal dictionary escapes, late overlapping runs, mid-run queries, invalid extents, one read-ahead fill for contiguous data, and native index chunks over 1 MiB with cached holes. Existing tests cover deduplication, corruption/recovery, optional object indexing/backfill, retention and aggregates.")
write()
write("Physical predecessor addressing is safe because index/population pairs are committed in identical append order. The 15-minute window remains provisional for coverage; committed rows do not shift. Replicated slices carry their first boundary once. Retention changes generation and invalidates old row addresses. Format 6 rejects format-5 files rather than reading them with the new width. No live data was migrated or service deployed.")
write()
write("Native collection remains the **Go API prototype**. Negotiated HTTP delivery and browser persistence using this new format still require integration; these measurements do not claim that deployment is complete.")
write()
write("## Reproduction and evidence")
write()
write("`before/` and `after/` contain native measurements; `codecs/` contains the matched-reader matrix. The frozen before/after source archives and metadata preserve source hashes and executable hashes. Executables are local build artifacts. Rebuild the archived source with the recorded Go toolchain, then run each fixture sequentially:")
write()
write("```sh\ngo -C go test -c -o /tmp/cache.test ./server/cache\nTRUNK_MEASURE_EVENTS=1000000 TRUNK_MEASURE_SHAPE=full \\\n  TRUNK_MEASURE_OUT=/tmp/native.json /tmp/cache.test \\\n  -test.run '^TestMeasureNativeStorage$' -test.timeout 10m\nTRUNK_MEASURE_EVENTS=1000000 TRUNK_MEASURE_SHAPE=full \\\n  TRUNK_MEASURE_OUT=/tmp/codecs.json /tmp/cache.test \\\n  -test.run '^TestMeasurePopulationCodecs$' -test.timeout 10m\npython3 benchmarks/storage/2026-10-15-native-pil/report.py\n```")
write()
write("Use both `full` and `sparse` at 50,000 / 500,000 / 1,000,000 records. The renderer verifies matching fixtures/results, configuration, widths and cached-byte counts before writing this report. Old report files are unchanged.")
lines[2:2] = ["> **Codec fixture correction (2026-10-16):** The population-codec tables below use wide PIL integers containing small values. They should not be treated as a general size comparison. See the [range-matched and full-U64 measurements](../2026-10-16-integer-ranges/REPORT.md). Historical measurements are retained; the fixed-index and reader before/after results still describe their stated fixtures.", ""]
(ROOT / "REPORT.md").write_text("\n".join(lines) + "\n")
