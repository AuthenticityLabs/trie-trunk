# Implementation status

Implemented server scope:

- CLI commands in `cmd/trunk`: ingestion and local window search, with `.env`
  profile loading. Port 5140 is the default ingress port.
- Ingestion listeners with local WebSocket, TLS WebSocket and HTTP/3; token/mTLS
  authentication and one-segment `/<tap>` routing.
- Separate CBOR negotiation/framing and unregistered JSONL handlers under
  `server/ingest/cbor` and `server/ingest/json`.
- Immutable dictionary installation, bounded shared tries, sorted index runs,
  replay checks, batch commits, checksums and incomplete-tail recovery.
- Five-minute count/duration accumulators, n-2 finalization, late corrections and
  recovery from index watermarks. Higher-period totals can be published explicitly.
- Read-only window search across taps and versions, derived transaction times,
  pretty JSON/JSONL output and optional presentation field ordering.

Current storage layout:

```text
logs/<tap>/<schema-version-uint32>/
  log.map
  log.tri
  log.y26
  2026/
    log.w52
    log.m12
    log.365
    266/
      log.idx
      log.pop
      log.sum
```

The schema directory is a decimal uint32, with packed 12.20 versions. Each file
inherits its schema from that directory. `log.idx` stores fixed UUIDv7, trie-leaf
and population-range columns; duration and other per-event values live in
`log.pop`. Start/end timestamps are derived during presentation.
See [FORMAT.md](server/store/FORMAT.md) for the exact layouts.

Remaining server work:

1. Semantic registration and schema training from 30-day comparison windows,
   with at least 30 days between learned revisions. Current CBOR negotiation
   still installs an already assigned definition.
2. Measured index/run compaction, popular-path references and compressed trie
   chains. Current deduplication is scoped to tap, schema and day.
3. Automatic higher-period rollups and merging across compatible schemas.
4. Remote search, subscriptions and the web server reserved in `server/serve`.
5. Archive export/compression, retention management and additional aggregation
   timezones. The current store requires explicit UTC.

Keep memory bounded, commit batches, and measure decoding, lookup and disk costs
before introducing queues, caches or more abstractions.
