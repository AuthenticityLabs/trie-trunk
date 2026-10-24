"""Render the matched-footprint report after the two opt-in Go measurements."""
import gzip
import json
from pathlib import Path

root = Path(__file__).resolve().parent
f = json.loads((root / 'footprint.json').read_text())
s = json.loads((root / 'summary-comparison.json').read_text())
c = f['by_component']
assert c['.sum'] == s['new_file_bytes']
expanded = (root / 'expanded.jsonl').read_bytes()
z = gzip.compress(expanded, compresslevel=6, mtime=0)
(root / 'expanded.jsonl.gz').write_bytes(z)
new = c['.sum']
items = [
 ('`.sum` Including Calendar Rollups', new),
 ('Detailed `.pods`', c['.pods']),
 ('Branch `.idx`', c['.idx']),
 ('Optional `collection.idx`', c['collection.idx']),
 ('Time/Resource `.tri`', c['.tri']),
 ('Tantivy', c['tantivy']),
 ('Retained `.journal`', c['.journal']),
 ('Other Files', f['trie_trunk_bytes'] - sum(c[k] for k in ['.sum','.pods','.idx','collection.idx','.tri','tantivy','.journal'])),
 ('Entire Datastore', f['trie_trunk_bytes']),
 ('Expanded JSONL', f['expanded_jsonl_bytes']),
 ('Expanded JSONL + gzip-6', len(z)),
]
rows = '\n'.join(f'| {name} | {n:,} | {n / 1e6:.3f} | {n / new:.2f}× |' for name, n in items)
saving = (1 - new / s['old_file_bytes']) * 100
change = f'{abs(saving):.1f}% ' + ('less' if saving >= 0 else 'more')
report = f'''# Calendar Rollups and Matched Storage Footprint

Implemented and tested; not deployed or reseeded into the running demo. Version-zero format replacement; no compatibility reader.

## Implementation

Daily publication now atomically writes weekly (Monday UTC), monthly, and yearly
rollup buckets into the same `.sum` file. These merge cached daily totals rather
than reading events or rescanning five-minute columns. Count, error count, duration
count/sum/min/max are retained; average is sum/duration-count, not an average of
averages. Error totals carry a statistics-completeness flag. Exact p95, labels,
and arbitrary predicates continue to use distributions/fine-grained data.

Each bucket stores its calendar interval and actual retained/sealed coverage.
Partial periods remain explicit; known empty days contribute zero. Retention
rebuilds parent totals; reopening verifies them against the daily summaries.
The current open day is excluded. Publication uses the existing sealing queue,
atomic replacement, fsync and directory sync, with ingestion continuing outside
the publication lock. Old compressed days are copied unchanged. The daily
replacement still rewrites the retained `.sum` file: this change does not remove
that pre-existing I/O cost.

`Store.CalendarRollups` exposes sealed buckets. Unfiltered `/events` requests
with `PIL-Features: pac-arrays,daily-summaries` can request
`Summary-Resolution: week|month|year`; contained buckets appear in
`analytics.rollups`. Totals use decimal strings for lossless U64 transport.
The summary cache identity includes the resolution. Default requests carry no
unused coarse metadata. Snoot's existing five-minute chart path remains intact;
this is not a new calendar chart selector or a promise to avoid all daily packet
retrieval. Non-UTC boundaries still need fine-grained data. Timezone selection
must not reinterpret UTC totals as complete local calendar periods.

A table has a 24-byte header, 88-byte fixed rows, and the existing 8-byte CRC
record framing. A full leap year has 53 overlapping Monday weeks, 12 months,
and one year: **5,840 bytes per resource/branch**. The matched fixture adds
**{s['calendar_rollup_bytes']:,} bytes for {s['calendar_rollup_rows']} buckets**
across {s['files']} summary files. These rows are uncompressed; daily packets
remain gzip compressed. Earlier explicit AGG1 publication helpers are separate
and are not scheduled by this implementation.

## Matched Storage

50,000 deterministic events across 30 days, five resources and five branches;
three branches per resource aggregate. Optional object indexing is enabled.
Every event is read back, UUID uniqueness and event counts checked, and Tantivy
catch-up completed before measuring. Sizes are logical bytes, decimal MB;
filesystem allocated storage for the entire fixture is
**{f['trie_trunk_allocated_bytes']:,} bytes**. Fixed clock and gzip defaults match
the earlier daily-summary fixture. Retained journals and all metadata are included.

| Artifact | Bytes | MB | Relative to `.sum` |
|---|---:|---:|---:|
{rows}

`.sum` is **{new / f['trie_trunk_bytes'] * 100:.2f}%** of the complete generated datastore.
Aggregates discard individual event fields; their size is not a substitute for
the lossless event store. Whole-file JSONL gzip benefits from cross-event history
and is not indexed or independently addressable like the datastore. It is a
storage reference, not an equivalent query system. Write PACs remain uncompressed;
non-write PACs use the configured default compression.

## Same Summary Values, Before and After

The old all-U64 codec is test-only. We decoded the exact freshly generated
summaries, encoded the same values with that control, decoded them again, and
checked logical equality. No inference from the earlier dense synthetic workload.

| Summary Component | Old All-U64 | Narrow/Based/Byte-Shuffled |
|---|---:|---:|
| Sealed Daily Packets | {s['old_sealed_bytes']:,} | {s['new_sealed_bytes']:,} |
| Decoded Daily Bodies | {s['old_decoded_bytes']:,} | {s['new_decoded_bytes']:,} |
| Open Hour Chunks, Framed | {s['old_open_hour_bytes']:,} | {s['new_open_hour_bytes']:,} |
| Calendar Rollups | 0 | {s['calendar_rollup_bytes']:,} |
| Entire `.sum`, Including File/Record Headers | {s['old_file_bytes']:,} | {new:,} |

Total summary storage changes by **{change}** including the added rollups.
Of the {new-s['old_file_bytes']:,}-byte increase, {s['calendar_rollup_bytes']:,} bytes
are new rollups and {s['new_without_calendar_rollups']-s['old_file_bytes']:,} bytes are
the net numeric-codec change. Decoded daily bodies are
{(1-s['new_decoded_bytes']/s['old_decoded_bytes'])*100:.1f}% smaller.
There are {s['sealed_packets']} sealed daily packets and {s['open_hours']} open-day
hour chunks. One day's sealed packets across all resources/aggregate branches
use min/median/max **{s['whole_day_min_bytes']:,} / {s['whole_day_median_bytes']:,} /
{s['whole_day_max_bytes']:,} bytes**. Sparse first/last days remain part of the sample.

The earlier “68% smaller decoded” result applied to a separate 240,000-event
numeric workload, not the entire datastore. The 20-event packet still grew by
98 compressed bytes; small packets can lose to metadata. This matched measurement
shows their combined effect across an actual generated datastore.

## Validation and Cost

Calendar tests cover leap day, ISO-week year crossing, UTC boundary consistency,
weighted duration totals, missing durations, checked overflow, empty days,
restart, retention through a partial day, and a valid-CRC table containing
incorrect totals. Existing daily-summary tests cover atomic publication failures
and concurrent ingress. HTTP tests check aligned rollup delivery and exclusion
at partial-day query boundaries, filtered-query rejection, and resolution-aware
summary caching. The full cache and seed-demo suites passed; the server transport
suite passed after rerunning with local-listener permissions (the first sandboxed
run could not bind sockets). Summary race tests, six client summary tests, and
TypeScript checking passed. See tests-*.txt for the executed suites.

`BenchmarkCalendarRollupsYear` merges 366 cached daily totals into all three
resolutions in a median **58 microseconds**, allocating 23,952 bytes across
19 allocations per rebuild; see bench.txt. This isolates arithmetic/directory work, excluding
serialization, file copying, fsync, network, decoding, and chart rendering.
Fixture generation time also includes indexing and readback; it is not an ingress
throughput measurement. Safari/browser rendering was not rebenchmarked.

## Reproduce

```sh
PATH="$PWD/rust/target/release:$PATH" GOCACHE=/tmp/trie-trunk-go-bench-cache TRUNK_FOOTPRINT_OUT="$PWD/benchmarks/storage/2026-10-24-calendar-rollups" go -C go test ./cmd/seed-demo -run '^TestDemoFootprint$' -count=1 -v
GOCACHE=/tmp/trie-trunk-go-bench-cache TRUNK_SUMMARY_MATCHED="$PWD/benchmarks/storage/2026-10-24-calendar-rollups" go -C go test ./server/cache -run '^TestSummaryMatchedFootprint$' -count=1 -v
python3 benchmarks/storage/2026-10-24-calendar-rollups/report.py
```
'''
(root / 'REPORT.md').write_text(report)
print(rows)
print('sum reduction',saving,'percent')
