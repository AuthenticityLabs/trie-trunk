# trie-trunk for Go

Go 1.24+, one module (`trie-trunk/go`):

```text
codec/            Shared CBOR, dictionaries, schemas, UUIDv7 and length framing
logger/
  client/         Transaction logging API, field keys and log definitions
  definition/     Semantic definition API design proposal
  out/            Writer and connection lifecycle
models/           Structured error models shared by clients
cmd/trunk/        CLI dispatch, arguments and environment configuration
server/
  ingest/         Listeners, authentication, tap routing and flush scheduling
    cbor/         Negotiated CBOR ingestion
    json/         Unregistered JSONL ingestion
  search/         Window parsing and Rust search process dispatch
  store/          File formats, writers, recovery and aggregates
  serve/          Reserved web server
```

The user interface lives in `../web`. Ingest uses Go storage and its shared codec. Rust owns search file reads and rendering. Runtime server packages do not depend on the CLI.

From `go/`:

```sh
make -C .. build
PATH="$PWD/../rust/target/release:$PATH" go test -race ./...
go vet ./...
```

`make build` creates the Go `trunk` CLI and Rust 1.88+ `trunk-search` executable.
Keep them together; cgo is no longer required. Logger and codec packages remain Go-only.

From the repository root:

```sh
./trunk ingest 5140
./trunk search --window 20260923T00:00,20260924T00:00
```

See [commands and configuration](cmd/trunk/README.md),
[ingestion](server/ingest/README.md), [search](server/search/README.md),
[file formats](server/store/FORMAT.md) and [implementation status](PLAN.md).
