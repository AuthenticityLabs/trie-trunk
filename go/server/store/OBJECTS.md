# Named objects and rolling trie statistics

The storage root is `./taps`. A tap is a source; it can supply multiple logical
objects. The immutable definition names its object and carries its version:

```text
taps/
  demo/
    registry.json
    .writer.lock
    logs.v1/
      0/
        events.map
        events.tri
        statistics
        2026/272/130000-0400/<owner-node>/
          events.idx
          events.pop
          events.sum
```

`logs.v1/0` means object `logs`, compatibility major `1`, definition minor `0`.
The version on the wire remains the existing packed unsigned 32-bit value:
`major << 20 | minor`. Thus version 1.0 is 1048576, not zero. No PIL protocol
version or per-event envelope change is needed.

Definition CBOR key `8` (JSON `object`) holds the object name. Names follow the
same path-safe rules as tap names. Named objects require storage format 3.
`events.map` is authoritative; its declared identity must match its directory.
The tap's `registry.json` lists object names, major versions, immutable minors,
definition digests and relative map paths. It is published atomically and rebuilt
from definitions on opening the writer.

Two objects can each have version 1.0. Negotiation and writer caches therefore
resolve the full revision (version **and digest**), never just the version.
Installing different bytes under the same object/major/minor is a conflict.
`Register` reuses an identical definition or assigns the next minor within that
object major. The JSON fallback registers as `logs`, major 1. Explicitly assigned
CBOR definitions supply their own identity.

Legacy anonymous numeric directories and `log.*` files remain readable for
older stores and offline migration. New named objects use `events.*` exclusively.
This change does not migrate or delete a store implicitly.

## Creating a tap

```sh
# Export the existing demo definition, without generating any events.
(cd go && go run ./server/ingest/sample --definition /tmp/demo-definition.json)
./trunk tap create demo --dir ./taps --definition /tmp/demo-definition.json
```

`tap create` without a definition creates an empty tap registry. A supplied JSON
definition must name an object and assign a nonzero major version. Creating a
named definition initializes its map, empty trie, and statistics snapshot. Index
and population files appear only when events are committed. Stop the writer
before installing a definition into a tap it currently owns.

## Statistics

`statistics` is an atomic JSON snapshot for **trie organization**, not a log
metrics file. It contains:

- An exact `[from_inclusive, through_exclusive)` 30-day window based on UUIDv7
  event timestamps, at millisecond resolution.
- Event count, events without a trie path, stored/active node counts and mean
  path length.
- Per-node visits and terminations, with parent, field key and dictionary value
  reference. Zero-usage historical nodes are excluded from the active list.
- Per-field value frequencies, observed cardinality, active node count,
  repeated-value node count, entropy and entropy conditional on the existing
  prefix. Entropies are in bits, conditioned on that field appearing in the trie.
- Hourly leaf-frequency buckets clipped to the snapshot's window. These retain
  joint lineage evidence for evaluating alternative orders later.

This works without a level, message, error, or duration field. Named definitions
can omit duration and published duration aggregates. Existing log duration
aggregates remain separate in `events.sum`; these statistics do not use them.
Literal values or fields retained in population (including leaf-zero fallback)
are not inspected and do not enter trie-field cardinalities. Dictionary size is
not observed cardinality. Higher entropy or lower cardinality alone is not proof
that a reordered trie will be smaller; that decision must account for joint
frequencies, missing fields, path lengths and node cost.

Only committed index rows enter the accumulator. Repeated UUIDs do not add
samples. Population-end watermarks avoid recounting rows when descriptors reopen
or indexes mature from 36-byte to 28-byte rows. Startup reconstructs statistics
from the committed indexes during normal recovery, so a missing or stale
statistics snapshot cannot corrupt the counts.

Complete hours use in-memory leaf frequencies. When a window boundary splits an
hour's observations, only that boundary's indexes are reread; population bytes
are not needed for this calculation. Snapshots publish on a dirty flush and at
most once per minute while idle. Their timestamps state exactly which window
they represent. The ingestion loop opens existing taps on startup so idle data
ages out too. A stopped server naturally leaves its last dated snapshot on disk.

No trie order changes automatically. Any future rebalance needs a new immutable
definition minor and a separate policy for evidence and significance.
