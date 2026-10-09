# Storage measurements and costs

## Format-3 resident reader (2026-09-29)

Apple M1 Max, release Rust build, 1,000,000 synthetic events across eight trie
owners in hourly mature indexes, one request worker, 256 MiB index-cache budget.
PIL-v2 output crossed the private Unix socket to Go's `io.Discard`. Each case
was warmed once; figures are the mean of three full returns. Ingestion and
fixture creation were excluded. No HTTP, TLS, browser rendering or network egress
is included.

| Scan workers | Full return | Throughput |
| --- | ---: | ---: |
| 1 | 1,013.97 ms | 986,610 events/s |
| 4 | 995.37 ms | 1,005,058 events/s |

The roughly 1.8% difference is small and not evidence of a reliable speedup.
This workload spends most of its return path outside parallel index selection.
These are full PIL-return timings, not nanosecond binary-search measurements.
Cold scans, more skew, higher file counts, historical ranges and RSS still need
separate measurements before production capacity claims.

Reproduce using a release-built `trunk-search` on PATH:

```sh
TRUNK_BENCH_EVENTS=1000000 go test ./server/search -run '^$' \
  -bench '^BenchmarkEventsResident$' -benchtime=3x -count=1
```


## Contiguous byte columns

The follow-up in `column_test.go` loads each timestamp byte position into its own
contiguous column. It does not pack whole UUIDs together:

```text
column 0: row[0][0], row[1][0], row[2][0], ...
column 1: row[0][1], row[1][1], row[2][1], ...
...
column 5: row[0][5], row[1][5], row[2][5], ...
```

All six columns share one allocation, with exactly one byte per row per column.
A time-window lookup needs only these first six UUIDv7 bytes. The remaining ten
bytes and row pointers stay in the original index; a column position identifies
the corresponding row. The prototype materializes all six columns once before
timing lookups and uses no maps or per-row objects.

Same machine and query distributions as the earlier experiment below. Medians
of five 200 ms samples, in nanoseconds per lookup:

| Rows and distribution | Binary, rows | Prefix, rows | Prefix, byte columns | Binary, byte columns |
| --- | ---: | ---: | ---: | ---: |
| 1,000, evenly spaced | 57.0 | 92.8 | 82.9 | 64.1 |
| Existing 10,000-row fixture | 104.3 | 147.8 | 125.4 | 91.5 |
| 10,000, bursts | 90.1 | 114.4 | 97.9 | 75.1 |
| 10,000, one millisecond | 46.8 | 10.0 | 10.5 | 54.8 |
| 10,000, byte rollover | 93.5 | 148.6 | 124.2 | 90.3 |
| 100,000, evenly spaced | 150.2 | 230.0 | 180.5 | 128.3 |
| 1,000,000, evenly spaced | 204.9 | 485.3 | 266.9 | 174.7 |

Every lookup variant allocated zero bytes. Byte columns reduced prefix lookup
time by about 15% on the fixture and 45% on the million-row array. Binary search
on columns was faster than prefix narrowing on columns except in the all-equal
case. The layout does not change either algorithm's asymptotic search cost.

The original row binary search uses `bytes.Compare`; column binary search compares
bytes explicitly. A separate control run using the same explicit byte comparison
on rows measured 100.0 ns for the fixture and 197.6 ns for a million rows, versus
91.5 ns and 174.7 ns on columns. Both layout and comparison implementation affect
the results; the full difference from the original baseline is not solely layout.

Column construction is measured separately, including allocation and extraction
from already-loaded index rows:

| Rows | Column payload | Allocated bytes/op | Build time |
| --- | ---: | ---: | ---: |
| 10,000 | 60,000 B | 65,536 B | 39.9 µs |
| 100,000 | 600,000 B | about 606,208 B | 383 µs |
| 1,000,000 | 6,000,000 B | 6,004,736 B | 3.78 ms |

Each build uses one allocation; Go rounds allocation sizes up. The input rows are
also resident during extraction. Construction does not include file I/O or CRC
validation, and lookup timings assume columns are already built and reused.
For the fixture, construction costs thousands of times the saving from one binary
lookup. The current search command performs two boundary lookups per sorted run,
so rebuilding these columns for each search would add work. Reuse in a resident
search service would need separate measurement and a bounded cache design.
Production search and the on-disk format remain unchanged.

The column algorithms share the existing linear-reference correctness tests,
including repeated timestamps and every timestamp byte rollover. A 10-second
fuzz run completed 238,289 cases without a mismatch. Store/search race tests,
the added row-comparison control tests, and store vet checks passed.

Run from `go`:

```sh
go test ./server/store -run 'TestTimeBound|TestTransposeTimeColumns|FuzzTimeBound' -count=1
go test ./server/store -run '^$' -bench '^Benchmark(TimeBound|TimeColumns)$' -benchmem -benchtime=200ms -count=5
```

## UUIDv7 time-bound lookup experiment

Measured on Apple M1 Max, Go 1.26.4, darwin/arm64. These are medians of five
200 ms benchmark samples, in nanoseconds per lower-bound lookup:

| Rows and timestamp distribution | Binary | Binary with endpoint checks | Byte-prefix narrowing |
| --- | ---: | ---: | ---: |
| Existing 10,000-row index fixture | 103.4 | 108.7 | 147.0 |
| 10,000, bursts with repeated milliseconds | 89.1 | 95.7 | 113.5 |
| 10,000, all in one millisecond | 45.8 | 4.1 | 9.3 |
| 10,000, across a timestamp byte rollover | 94.0 | 99.1 | 147.6 |
| 1,000, evenly spaced over one day | 56.2 | 62.5 | 92.1 |
| 10,000, evenly spaced over one day | 103.8 | 109.1 | 147.3 |
| 100,000, evenly spaced over one day | 148.9 | 155.2 | 231.7 |
| 1,000,000, evenly spaced over one day | 205.2 | 207.7 | 484.3 |

All three algorithms allocate zero bytes per lookup. Binary search was faster
on every nondegenerate distribution measured. Prefix narrowing won against plain
binary search when all timestamps were equal, but checking the first row before
binary search handled that case faster still. Production search is unchanged.

The experiment lives in `lookup_test.go`. Each lookup returns the first row whose
six-byte UUIDv7 timestamp is greater than or equal to the requested millisecond.
Using this boundary for both ends selects a half-open time window, including all
events at the starting millisecond and excluding those at the ending millisecond.
Random UUID suffixes do not participate in time-only lookups.

The prefix algorithm examines the first and last rows of its candidate range. It
skips common timestamp bytes, then binary-searches the lower and upper boundaries
of the next matching byte. It continues within that range. Locating both boundaries
adds searches; visiting at most six byte positions does not bound the number of
row probes independently of the row count.

All variants read identical, already-loaded 36-byte rows and use the same 4,096
deterministic queries: half sampled from row timestamps and half sampled uniformly
between the first and last timestamp. The one-millisecond dataset necessarily
repeats one query and measures an endpoint shortcut. Data generation, loading,
checksum validation, trie reconstruction, CBOR decoding and JSON output are outside
the timed loop. These measurements do not represent disk or complete search latency.
Repeated queries also warm caches; deployment workloads can differ.

The existing fixture is `logs/index-test/1/2026/267/log.idx`. Its single sorted run
starts its rows at byte 20, so row `i` starts at `20 + 36*i`; the last row starts
at `EOF - 36`. The benchmark loads this file once before timing. Other cases use
generated, globally sorted row arrays. The million-row case exceeds the current
16 MiB run limit and is a lookup scaling experiment, not a valid single disk run.
Real index files can contain multiple individually sorted runs, each requiring
its own boundary search.

Correctness checks use a linear reference and cover empty inputs, exact and missing
timestamps, repeated milliseconds, out-of-range queries, and carries across every
timestamp byte boundary. A 10-second fuzz run completed 233,999 cases without a
mismatch. Store/search race tests and store vet checks passed.

Run from `go`:

```sh
go test ./server/store -run 'TestTimeBound|FuzzTimeBound' -count=1
go test ./server/store -run '^$' -fuzz '^FuzzTimeBound$' -fuzztime=10s -parallel=1
go test ./server/store -run '^$' -bench '^BenchmarkTimeBound/' -benchmem -benchtime=200ms -count=5
```

The fixture case skips when the local sandbox index is absent; generated cases
remain available.

## Ingestion

Format 2 sandbox pass (Apple M1 Max, Go 1.26.4, 1,000 records): about
9.93 µs/entry, 1,354 B/op, 32 allocs/op using BenchmarkAppend. This includes
a final durable group commit and excludes network I/O. Compare on deployment
hardware; the older numbers below were collected in a different run.

The shared trie is cached per open version, bounded by open partitions and the
node limit. Population CRCs and fsync are per batch, not per record. Recovery
reads referenced population blocks to rebuild duration aggregates. log.idx stays
fixed-width; index-only replay checks do not read population payloads.

## Earlier prototype measurement

A local Apple M1 Max / Go 1.26.4 run of:

```sh
go test ./server/store -run '^$' -bench BenchmarkAppend -benchtime=1000x -benchmem
```

measured about **13 microseconds per entry, 1,360 B/op and 32 allocs/op**.
The benchmark primes the partition, then includes UUID generation, CBOR encoding,
decoding, insertion/replay checks and a durable final batch flush. Records contain
only id, duration, category and level; all share one millisecond and one trie path.
This deliberately exercises out-of-order random UUID suffixes. It excludes TLS,
networking, authentication, schema installation, diverse payloads and contention.
A short local run is not a production throughput or latency guarantee.

Current costs to measure next:

- Dynamic CBOR maps/raw messages allocate during decode and projection. A compiled
  schema decoder could reduce these allocations without restricting literal fields.
- Sorted active-index insertion shifts row descriptors on out-of-order arrivals.
  Tail scanning handles the last eight rows; binary search bounds comparisons for
  older rows, but movement remains linear in the bounded batch size (default 1024).
- Committed lookup checks candidate runs newest-first; it reads eight tail rows at
  once then binary-searches older uuid columns. An id above the committed maximum
  skips disk lookup. Overlapping run ranges can still make old replay checks costly.
- The manager serializes mutations and group commits. A slow fsync blocks ingestion;
  bounded parallel partition writers or a bounded commit queue need measurements.
- One flat edge map serves all trie nodes. The node cap falls back to population payloads,
  preserving data but reducing compression after saturation. Popular-path uint8
  references and compressed chains remain future work.
- Finalization scans active groups once, writes eligible blocks, then syncs once.
  It does not scan every group once for each of the 288 buckets.
- Startup scans committed runs to recover unfinished aggregates. It uses one day
  partition at a time; sealed-partition metadata could reduce startup work later.

No pools, per-record disk syncs, whole-history uuid cache, unbounded queues,
per-node maps or per-record compression have been introduced.
