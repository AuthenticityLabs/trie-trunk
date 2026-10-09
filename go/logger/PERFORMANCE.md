# Performance decisions and measurements

## Direct Entry encoding

`wireEntry`, `wireCheckpoint`, and their conversion are removed. Compact JSON
and CBOR now append from Entry directly. Nested errors use their getters and
checkpoints are traversed in place. Dynamic extension values still use the
existing codec. No pooling, unsafe code or additional transaction model is used.
The small CBOR writer emits canonical integer/map lengths; an independent
reference codec checks byte-for-byte CBOR equivalence and semantic JSON
equivalence in fuzz tests. Maintaining this writer is the explicit tradeoff for
removing reflective projections from the fixed record path.

Apple M1 Max, Go 1.26.4, 100 ms samples with discard sinks:

| Transaction | Before time → after | Before allocated bytes → after | Allocations |
| --- | ---: | ---: | ---: |
| Fixed | 947 → 446 ns | 816 → 608 | 5 → 4 |
| One field + checkpoint | 1,445 → 892 ns | 1,301 → 1,045 | 12 → 11 |
| 32 distinct checkpoints | 5,027 → 2,300 ns | 4,994 → 4,096 | 12 → 12 |
| Known error | 1,377 → 681 ns | 1,304 → 1,000 | 10 → 8 |

These are local microbenchmarks, not network throughput guarantees. Growing the
output buffer can still allocate; there is no separate checkpoint projection.
Numbers and descriptions below document earlier implementations.

## Previous cleanup measurements


Apple M1 Max, Go 1.26.4, two 200 ms samples per case, before/after this cleanup.
These isolate producer work with discard sinks; they exclude network and disk.
Older measurements below remain historical comparisons.

| Case | Allocated bytes before → after | Allocations before → after |
| --- | ---: | ---: |
| Fixed CBOR transaction | 848 → 816 | 5 → 5 |
| 32 distinct checkpoints | 5,715 → 4,994 | 12 → 12 |
| HTTP success | 1,360 → 1,328 | 12 → 12 |
| HTTP error | 2,890 → 2,594 | 41 → 39 |

HTTP success measured 1.31–1.33 µs before and 1.28–1.33 µs after;
HTTP error measured 3.16 µs before and 2.90–2.99 µs after. Small timing
changes are noisy; fixed transaction CPU was effectively unchanged (~0.94 µs).
Run `go test ./logger/client -run '^$' -bench 'BenchmarkHTTP|BenchmarkTransaction' -benchmem`.

Reordering private Symbol fields removes padding: Symbol 32 → 24 bytes,
Entry 360 → 320, state 448 → 408, Checkpoint 48 → 40 (64-bit sizes).
Allocator size classes account for differences from allocated bytes per operation.
The wire format is unchanged. Request and response metadata each take one
builder lock. HTTP error normalization now runs once, preserving the untruncated
public response and applying text limits only to the log. Sanitization returns
its newly owned containers directly instead of copying them again; the existing
small nested-object benchmark did not show an allocation-count reduction.

Construction shares one initialization function for local and remote loggers;
remote construction no longer installs a temporary discard sink. Config.OnError
supplies HTTP defaults once. The HTTP adapter does not start a server.

Retained transport costs include one control-reader goroutine per WebSocket,
a timeout context per write, a frame copy, and serialized synchronous I/O.
No network speedup is claimed here. Checkpoint slice growth/projection, nested
field copies, and readable JSON remain the largest optional producer costs.


Measured locally on Apple M1 Max, Go 1.26.4, using the repository benchmarks.
Run the command below from the `go` module root.
These are microbenchmarks, not a promise of HTTP throughput or disk/network
latency. Run them on the deployment hardware before setting budgets.

```sh
go test ./logger/client -run TestEncodingSizes -v -bench . -benchmem -benchtime=300ms
```

Representative fixed entry: service/resource/operation references, service
version, UUIDv7 entry Id,
duration, method, status, response byte count, and standard message.

| Encoding | Raw bytes/record | Time/op | Allocated bytes/op | Allocations/op |
| --- | ---: | ---: | ---: | ---: |
| Readable JSON | 272 | 3.49 µs | 2,905 | 52 |
| Compact JSON | 128 | 0.91 µs | 464 | 3 |
| CBOR | 50 | 0.64 µs | 352 | 2 |

Transport framing is excluded from the table: JSON adds a newline; remote CBOR
adds a two-byte length prefix, making the 50-byte example a 52-byte frame.
Readable JSON expands an
actual application dictionary. The fixed CBOR `Begin` → typed setters → `Finish`
benchmark uses `io.Discard` and measured approximately **0.97 µs, 848 bytes,
5 allocations per transaction**. Adding one extension field and one checkpoint
measured approximately **1.53 µs, 1,349 bytes, 13 allocations**. These transaction
benchmarks exclude HTTP wrapping, real request data, and real sink latency. They
include UUIDv7 generation, context creation, and finalization.
The length-prefixed stream path measured **0.95 µs, 848 bytes, 5 allocations** against
a discard connection stub, with initialization excluded. No transport I/O is
simulated by that benchmark. Session synchronization adds no per-record
allocation; its lock replaces the local writer lock rather than adding a second.
Framing reuses one buffer under that lock, so it adds a payload copy but no
steady-state allocation for small records. Retained capacity is capped at 64 KiB;
larger frames use a temporary buffer. Payloads over 65,535 bytes are rejected
before transmission, including schema uploads. No silent truncation or chunking
is implemented. Receivers check the length before allocating payload memory.

The deterministic synthetic 1,000-record gzip batch varies correlation values,
timestamps, and durations. It measured ~20.94 bytes/record for readable JSON,
~20.07 for compact JSON, and ~17.25 for CBOR. That workload compresses repeated
field names extremely well: **raw size savings do not translate directly into
compressed storage savings**. CBOR's measured advantage here is raw transport
size and producer encoding cost. Compression CPU was not benchmarked; this
library does not compress or batch internally.

## Costs found and changes made

### Isolated feature costs

`go test ./logger/client -run TestModelMemorySizes -v -bench '^BenchmarkFeatureCost$' -benchmem`
isolates features that the original Extensions benchmark combined. Measured on
the same machine; times below include the complete CBOR transaction lifecycle,
UUIDv7 generation, and an `io.Discard` sink. Add two framing bytes for remote
records. These are producer costs, not network latency or resident memory.

| Feature | Time/op | Allocated bytes/op | Allocations/op | CBOR payload bytes |
| --- | ---: | ---: | ---: | ---: |
| Baseline | 1.03 µs | 848 | 5 | 50 |
| One numeric checkpoint | 1.16 µs | 944 | 7 | 61 |
| Eight distinct checkpoints | 2.05 µs | 1,904 | 10 | 124 |
| 32 distinct checkpoints | 5.98 µs | 5,715 | 12 | 350 |
| Same checkpoint updated 32 times | 1.60 µs | 944 | 7 | 62 |
| One stop-function checkpoint | 1.25 µs | 1,040 | 9 | 59 |
| One scalar extension field | 1.35 µs | 1,253 | 11 | 66 |
| Three-value nested extension object | 2.47 µs | 2,129 | 23 | 94 |
| Known validation error | 1.40 µs | 1,432 | 10 | 64 |
| 512-byte literal message | 1.06 µs | 1,392 | 6 | 564 |

Numeric checkpoint examples use one millisecond per observation. Stop-function
duration depends on execution timing and was under 24 microseconds in this run,
so its encoded size is not directly comparable to the fixed-duration example.
All sample entries use the same total duration for wire-size comparison.

Distinct checkpoints pay for slice growth and another slice built by the encoding
projection. Lookup is linear, so inserting n distinct checkpoints performs
O(n²) total name comparisons, bounded by the configured maximum. Repeated names
aggregate instead of expanding the record. Nested extension objects incur map
construction, immutable copying, sanitization, and encoding callbacks. Long
strings mainly hurt transport/storage size rather than producer CPU.

The baseline itself still has worthwhile optimization targets: on this machine
Entry is 360 bytes, transaction state is 448 bytes, Symbol is 32 bytes,
Checkpoint is 48 bytes, and the compact wire projection is 288 bytes, excluding
backing allocations. Symbol reserves space for literal strings even when a small
numeric reference is all that is needed. These layout and projection costs are
better baseline optimization candidates than removing a handful of checkpoints.
No runtime behavior was changed for these measurements.

### Previously applied changes

- **Per-record schema map construction:** removed. Schemas are built once for
  readable expansion. Public `BuildSchema()` returns a fresh schema for inspection,
  but the producer does not call it per request.
- **Intermediate maps for fixed fields:** avoided for compact JSON and CBOR.
  Struct tags define permanent numeric keys. Fixed fields remain typed in memory.
- **Per-enum marshal callbacks:** removed from compact entry projections. Those
  projections hold only primitive scalars in interface slots. Numeric values
  need no per-enum recursive encoding call. This reduced the measured fixed
  transaction from 16 allocations/~1.4 KB to 5 allocations/880 bytes.
- **Copying the frozen entry's containers:** removed from finalization. Once
  setters are sealed, ownership passes to the encoder. Explicit `Snapshot`
  remains a defensive copy and should be kept off the hot path.
- **Sorting oversized extension objects to select survivors:** dropped. Such
  objects become a truncation marker. The library does not pay O(n log n) over
  an arbitrarily large set just to retain a few keys.
- **Checkpoint stop closures:** optional convenience only. `Checkpoint(id,
  duration)` avoids the closure and its once-guard. Aggregation uses a bounded
  slice (default 32), avoiding a checkpoint map and per-step string names.
- **No-op chains:** lazy callbacks avoid computing disabled detail. Disabled
  chains never allocate extension maps.

## Retained costs and boundaries

- One mutex protects builder mutation. It buys safe enrichment by request-owned
  goroutines; a fluent chain takes one lock per setter. A future grouped setter
  should be justified by contention benchmarks, not added speculatively.
- Finalization has a `sync.Once` guard; a separate logger mutex protects one
  writer call. Synchronous delivery adds sink latency but has no internal queue,
  worker goroutine, unbounded backlog, or hidden drop policy.
- The CBOR library caches reflection metadata for fixed structs. The first call
  has setup cost; these steady-state benchmarks do not measure cold startup.
  We retain a maintained encoder instead of writing a custom CBOR parser.
- Extension containers intentionally allocate. They are immutable copies, then
  recursively sanitized. Prefer fixed fields, small scalar extensions, and
  predefined references. Nested limits are per container, not a total payload
  quota; accepting arbitrary large application-built trees is not a low-memory
  use case.
- Deterministic CBOR may sort extension map keys. Do not use extensions for
  fields that could be fixed fields. JSON encoders also have map ordering costs.
- Schema/catalog metadata now belongs to connection initialization. Removing it
  reduced the representative CBOR record from 93 to 77 bytes. Preparing and
  hashing the definition happens once per logger; reconnect checks its cached
  revision and transfers a copy of the definition only if the peer is out of
  sync. Receivers must preserve the connection-to-schema association in storage.
  There is no per-record hashing, negotiation, runtime string interning, or
  dynamically growing dictionary.
- UUIDv7 replaces the old textual transaction identifier and separate timestamp.
  That further reduced the CBOR example from 77 to 50 bytes. The producer holds
  the Id as 16 bytes, passes that byte array directly to CBOR, and formats text
  only for JSON/header output. No start/end-time fields or derived-time methods
  exist on Entry. Interpretation belongs to the log-view client.
- Generating an Id adds cryptographic randomness work for each transaction. It
  requires no global generator mutex or counter; CBOR transaction allocations
  remain at five in the benchmark. No application randomness pool was added.
- The HTTP adapter allocates context/wrapper/correlation data. It preserves
  Flush/Hijack/Push semantics; it deliberately does not optimize large-file
  `io.ReaderFrom`/sendfile paths. Benchmark actual large response workloads before
  adding that separate path.

## Features intentionally not reproduced

Automatic stack traces, regex/substring error classification, exception-class
discovery, reflection over arbitrary application objects, per-call log writes,
thread-local/MDC state, derived qualified-name strings, per-event pretty printing,
request/response body capture, and automatically trusted forwarded-IP headers.

There is no internal async queue, compression worker, pooling, or unsafe code.
Pooling request builders would create stale-context/reuse hazards; immutable
snapshots and ownership are more valuable than speculative allocation savings.
Future optimizations must retain tests for races, correlation, wire compatibility,
and single-write finalization and show a measured benefit on representative data.
