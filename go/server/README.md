# Server components

| Package | Responsibility |
| --- | --- |
| `ingest/` | Listeners, authentication, tap routing, connection limits and flush scheduling |
| `ingest/cbor/` | CBOR schema negotiation, HTTP batches and binary WebSocket frames |
| `ingest/json/` | Unregistered JSONL decoding and ingestion |
| `search/` | Window parsing and Rust process dispatch |
| `store/` | File formats, schema validation, trie/index/population data, recovery and durable aggregates |
| `serve/` | Reserved for the future web server; no implementation |

The executable lives in [`../cmd/trunk`](../cmd/trunk/README.md). Command parsing,
`.env` loading and command output belong there. Build from `go/`:

```sh
make -C .. build
```

CBOR and JSON handlers depend on storage, not on each other or the ingest router.
The router selects the tap once per connection or HTTP batch and supplies its
store to the chosen handler. Transport authentication and limits apply to both.

Rust owns search file reads, validation, selection and JSON rendering. The Go
search adapter passes query options and stdout to the Rust executable. Storage
imports neither ingest nor search and does not open listeners or load `.env`.

Accumulators stay with storage because they use trie leaves and committed index
positions. Recovery rebuilds them from durable records. Ingest schedules flushes;
storage commits them. See [file formats](store/FORMAT.md),
[ingestion](ingest/README.md) and [search](search/README.md).
