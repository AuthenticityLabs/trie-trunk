"""Publish matched million-event storage measurements after the opt-in tests."""
import gzip
import hashlib
import json
from pathlib import Path

root = Path(__file__).resolve().parent
f = json.loads((root / 'footprint.json').read_text())
s = json.loads((root / 'summary-comparison.json').read_text())
c = f['by_component']
assert f['events'] == 1_000_000
assert c['.sum'] == s['new_file_bytes']
digest = hashlib.sha256()
source_bytes = 0
with (root / 'expanded.jsonl').open('rb') as source, (root / 'expanded.jsonl.gz').open('wb') as target:
    with gzip.GzipFile(filename='', mode='wb', fileobj=target, compresslevel=6, mtime=0) as zipped:
        while chunk := source.read(1024 * 1024):
            digest.update(chunk)
            source_bytes += len(chunk)
            zipped.write(chunk)
assert source_bytes == f['expanded_jsonl_bytes']
assert digest.hexdigest() == f['expanded_jsonl_sha256']
archive = {'source_bytes': source_bytes, 'source_sha256': digest.hexdigest(),
           'gzip6_bytes': (root / 'expanded.jsonl.gz').stat().st_size}
(root / 'archive.json').write_text(json.dumps(archive, indent=2) + '\n')
items = [
    ('Event Pods', c['.pods']), ('Tantivy', c['tantivy']),
    ('Time/Resource/Registry Tries', c['.tri']), ('Branch Indexes', c['.idx']),
    ('Optional Collection Index', c['collection.idx']), ('Summaries', c['.sum']),
    ('Retained Recovery Journals', c['.journal']),
]
items.append(('Other Files', f['trie_trunk_bytes'] - sum(n for _, n in items)))
items.extend([('Entire Datastore', f['trie_trunk_bytes']),
              ('Filesystem Allocated Datastore', f['trie_trunk_allocated_bytes']),
              ('Expanded JSONL', source_bytes),
              ('Expanded JSONL + gzip-6', archive['gzip6_bytes'])])
rows = '\n'.join(f'| {label} | {n:,} | {n / 1e6:.3f} |' for label, n in items)
report = f'''# One Million Events: Matched Storage Footprint

Measured with the current version-zero implementation, using the existing
seed-demo footprint fixture at 1,000,000 events. No extrapolation from 50,000.
This run uses an isolated temporary datastore; it does not reseed the running app.

## Scope

Thirty days, five resources, five event branches, fifteen Definition fields,
adaptive integers level 16, and optional collection indexing enabled. The
fixture cycles through 250 object identities per resource. This measures one
million events, not one million distinct objects. Data is synthetic and contains
repeated messages and mapped values; compression depends on this distribution.

Every event is read back and unique UUIDs and counts are verified. Tantivy
catch-up completes before storage accounting. All regular datastore files,
including recovery journals and metadata, are counted after stores close.
Write PACs remain uncompressed; other PACs use default gzip compression.
MB is decimal. Logical sizes and filesystem allocation are distinct.

The native-memory budget override is {f['native_memory_budget_override']:,} bytes
per resource for this bulk historical ingestion fixture. The initial run at the
default 1 GiB limit failed with staging backpressure; its log is preserved as
`footprint-default-budget.log`. This is not a claim that the default budget can
ingest this fixture without pacing. The override is a limit, not measured RSS.

## Storage

| Component | Bytes | MB |
|---|---:|---:|
{rows}

The full datastore is {(1-f['trie_trunk_bytes']/source_bytes)*100:.1f}% smaller
than expanded JSONL in this fixture. The gzip JSONL reference compresses a whole
file ordered by resource, branch, and time; it has no equivalent indexes,
summaries, replay access, or independently retrievable hourly segments.
It is not an equivalent query system.

## Summary Replication

The exact measured summaries were decoded and compared against the test-only
all-U64 control with logical equality checks. Across all resources and aggregate
branches, one day's sealed packets total min/median/max
{s['whole_day_min_bytes']:,} / {s['whole_day_median_bytes']:,} / {s['whole_day_max_bytes']:,} bytes.
Partial first/last days are included. There are {s['sealed_packets']} sealed daily
packets and {s['open_hours']} open-day hour chunks. Summaries represent
{c['.sum']/f['trie_trunk_bytes']*100:.2f}% of the datastore. They aggregate data and
cannot replace the detailed journal.

## Evidence and Reproduction

`footprint.json` contains component and individual-file accounting, fixture
dates, exported JSONL hashes, and validation notes. `archive.json` verifies the
same JSONL bytes for gzip. `summary-comparison.json` records matched summary
measurements. Test output is in `footprint.log` and `summary-comparison.log`.
Fixture generation includes ingestion, sealing, indexing, and verification;
its elapsed time is not an ingestion or query latency benchmark.

```sh
PATH="$PWD/rust/target/release:$PATH" GOCACHE=/tmp/trie-trunk-go-bench-cache TRUNK_FOOTPRINT_OUT="$PWD/benchmarks/storage/2026-10-24-million-events" TRUNK_FOOTPRINT_EVENTS=1000000 TRUNK_FOOTPRINT_NATIVE_BYTES=8589934592 go -C go test ./cmd/seed-demo -run '^TestDemoFootprint$' -count=1 -timeout=60m -v
GOCACHE=/tmp/trie-trunk-go-bench-cache TRUNK_SUMMARY_MATCHED="$PWD/benchmarks/storage/2026-10-24-million-events" go -C go test ./server/cache -run '^TestSummaryMatchedFootprint$' -count=1 -v
python3 benchmarks/storage/2026-10-24-million-events/report.py
```
'''
(root / 'REPORT.md').write_text(report)
print(rows)
