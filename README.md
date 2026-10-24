# Trie-Trunk

An event datastore for logs, object revision replay, temporal search, and analytics.
Ingest typed events once; reuse complete hourly datasets and compact summaries in
the browser instead of repeating every investigation against the server.

[Website](https://trietrunk.com) · [Snoot](https://snoot.trietrunk.com) ·
[PIL Definition Schema](pil/definition.cddl)

Version zero: storage designs and fixtures can change without migration paths.
The sample datasets are disposable. This README is the project's public guide;
agent instructions, design notes, and historical prose reports remain local.

## Architecture

- **Go** manages HTTP connections, authentication, configuration, scheduling,
  sealing, retention, and response assembly.
- **Rust** owns resident PIL buffers, live identity directories, Tantivy indexes,
  and native search execution. Go calls Rust through an in-process C ABI.
- **Snoot** uses Solid and Unovis. A SharedWorker coordinates browser storage,
  PIL/WASM decoding, query execution, and cached complete datasets.
- **PIL** describes typed positional records; **PAC** groups them under a shared
  Definition, with common values and optional compression.

This is not yet a fully native record engine. Go still allocates projection,
query-result, and sealing metadata. Measure native memory and process RSS along
with Go allocations; moving ownership alone does not establish a latency win.
The separate standalone CBOR store/search tooling is not Snoot's canonical path.

### Data Lifecycle

| Stage | Purpose |
| --- | --- |
| `.seed` | Recovery journal and live ingress, with native resident records |
| `.pack` | Hourly preparation after the late-arrival window |
| `.pods` | Sealed server PAC storage; write PACs stay uncompressed for replay |
| `.sods` | Decoded sparse browser storage for complete collected hours |
| Stalks | In-memory analytic columns; summary columns exist, broader event projection remains future work |
| `.sum` | 5-minute columnar summaries, daily compressed packets, and calendar rollups |

Research retrieves complete matching segments. Object history and replay use
separate record-level access. A missing client hour is unknown, not an empty
result. Only complete hours enter durable coverage. Summary requests remain
independent of event paging; paging must not reduce full-range counts.

```text
caches/<cache>/
  registry.cfg
  registry.map
  registry.tri
  <service>/<resource>.v<major>/
    collection.idx                 # optional object/revision lookup
    collection.state               # invalidation state
    <minor>/
      resource.map
      resource.tri
      resource.stats
      read.* / write.* / error.* / debug.* / trace.*
      lost.log
```

Branch time tries use positional UTC year/day/hour navigation. Branch indexes
carry fixed event rows; collection and Tantivy references identify the source,
hour, and record. Keep object identity separate from event identity. `level` and
`access` are independent query fields. A completed request is not automatically
a committed object mutation.

Configuration can inherit through `trunk.cfg`, `cache.cfg`, `service.cfg`,
`resource.cfg`, and branch configuration. Per-resource/branch policy controls
retention and compression. Source implementations live in
[`go/server/cache`](go/server/cache), [`rust/src`](rust/src), and
[`client/services`](client/services).

## Build and Run

Requires Node 22.12+, Go 1.24+, CGO with a C linker, and Rust 1.89+ including
`wasm32-unknown-unknown`. Build Rust before the Go executable.

```sh
rustup target add wasm32-unknown-unknown
make build
./trunk ingest 5410 serve 8443 --http-address 127.0.0.1:3020
```

Runtime configuration, credentials, certificates, and caches are local and
ignored. See [`.env.example`](.env.example) and the CLI implementation under
[`go/cmd/trunk`](go/cmd/trunk) for configuration. Deploy `trunk`, `trunk-search`,
and generated `web/` together. Restart after rebuilding native executables;
frontend-only builds are read from disk without restarting the datastore.

```sh
make web                         # rebuild Snoot
make site                        # build public website into site/dist
node site/preview.mjs             # local website on 127.0.0.1:3030
```

On the development Mac, `trunk-server` is the launcher for the configured snapi
service. It loads local environment configuration and uses `exec` so the server
is supervised directly. Snoot's Tunnel origin is `http://localhost:3020`.
Do not replace that Tunnel when deploying the public website.

### Public Website

The separate Cloudflare Pages project is `trietrunk`, serving `/trunk`, `/snoot`,
`/pil`, and `/logger` at `trietrunk.com`. Build and deploy with:

```sh
node site/build.mjs
cd site
wrangler pages deploy dist --project-name trietrunk --branch main
```

The website uses checked benchmark JSON at build time and generates its
measurement download without depending on local Markdown reports. Shared artwork
uses the front-facing Snoot anteater for all logos and lockups; the side profile
is reserved for profile/avatar icons. The trunk-sniffing composite is not used.
Snoot's logo cycles Trie-Trunk, Light, and Dark themes; saved preferences persist.

## PIL

PIL carries complete objects or partial changes. Definitions specify ordered
fields, primitive types, dictionaries, and integer-encoding options. Definitions
are CBOR; [`definition.cddl`](pil/definition.cddl) describes their structure.
Absent, null, direct, and offset-backed field states remain distinct. `REF16` is
optional. Adaptive integers are configured per Definition.

A PAC groups records under one Definition and may factor shared values. Its
readable compressed wrapper describes the packed body; decoding is owned by the
client library rather than HTTP content decoding. Storage placement and query
execution belong to Trie-Trunk, not the interchange format.

See [`pil/src`](pil/src), [`pil/tests`](pil/tests), and the cross-language fixtures
for the implemented format. Format experiments replace fixtures; old benchmark
snapshots are not compatibility contracts.

## Logger

The Go PIL logger registers a Definition before its first write. It exchanges
identity and digest, sends missing maps, and validates a compatible server layout.
`WriteValues` encodes positional values into the accepted layout; `WriteFrames`
accepts frames under the producer's original Definition. Extensions can use
explicitly enabled `@fields` and be promoted by compatible newer Definitions.

```go
mapBytes, err := os.ReadFile("resource.map")
if err != nil { return err }
logger, err := pil.Open(ctx, pil.Config{
    URL: trunkURL, Cache: "prod", Branch: "write",
    Token: token, Definition: mapBytes,
})
if err != nil { return err }
defer logger.Close()
return logger.WriteFrames(ctx, frames...)
```

Import `trie-trunk/go/logger/pil`. The example assumes application-supplied
context, endpoint, token, and PIL frames. For HTTP instrumentation, use
`client.NewPIL`; see the [runnable example](go/logger/client/examples/http/main.go).

`BatchMilliseconds` optionally batches concurrent producers; zero sends
immediately. HTTP gzip compresses batches before ingress decompression. Revisions
are preserved without coalescing. Calls wait for staging acknowledgement rather
than succeeding on local queue insertion. Observe delivery errors and retain
stable event identities for retries. See [licensing](#licensing) for the current
SDK dependency-boundary caveat.

## Validation

```sh
make test                        # native Rust/PIL and Go suites
cargo test --locked --all-features --manifest-path pil/Cargo.toml
npm --prefix client test         # codec, store, query and UI logic tests
npm --prefix client run check    # TypeScript
node site/build.mjs
```

Browser/OPFS probes under `client/scripts` are separate from the unit suites.
They require a supported browser runtime; a passing CLI build does not establish
browser visual correctness, Safari storage behavior, or production performance.

## Measurements

The current synthetic million-event fixture uses 30 days, five resources, five
branches, repeated values, and optional collection indexing. It cycles through
250 object IDs per resource, not one million independent objects.

| Artifact | Decimal MB |
| --- | ---: |
| Complete Datastore, Including Indexes and Journals | 168.41 |
| Expanded JSONL | 428.60 |
| Whole-File gzip-6 JSONL | 25.44 |

Compressed JSONL has no equivalent indexes, replay lookup, summaries, or
independently retrievable hourly segments. These are storage results, not an
end-to-end throughput comparison. The bulk fixture used an 8 GiB native budget
per resource after its initial 1 GiB run hit backpressure; that is a limit, not
measured RSS. Production results depend on the event distribution and pacing.

Raw accounting and reproduction scripts are in
[`benchmarks/storage/2026-10-24-million-events`](benchmarks/storage/2026-10-24-million-events).
Bulk exports, source snapshots, generated summary packets, and prose reports
remain local; checked result JSON, scripts, and test fixtures stay in Git.

```sh
PATH="$PWD/rust/target/release:$PATH" TRUNK_FOOTPRINT_OUT="$PWD/benchmarks/storage/2026-10-24-million-events" TRUNK_FOOTPRINT_EVENTS=1000000 TRUNK_FOOTPRINT_NATIVE_BYTES=8589934592 go -C go test ./cmd/seed-demo -run '^TestDemoFootprint$' -count=1 -timeout=60m -v
TRUNK_SUMMARY_MATCHED="$PWD/benchmarks/storage/2026-10-24-million-events" go -C go test ./server/cache -run '^TestSummaryMatchedFootprint$' -count=1 -v
python3 benchmarks/storage/2026-10-24-million-events/report.py
```

## Licensing

Copyright 2026 trie-trunk contributors.

This repository uses different licenses for the application and the reusable
codecs and client libraries. These are component licenses, not a choice of
license for the entire repository.

| Component | Paths | SPDX identifier | License text |
| --- | --- | --- | --- |
| trie-trunk server, CLI, search engine, browser store, Snoot, and other repository-owned material outside the exceptions below | Repository default, including `go/server/`, `go/cmd/`, `rust/`, `client/`, and root build scripts | `AGPL-3.0-only` | [GNU AGPL version 3](LICENSE) |
| PIL codec, its WASM wrapper, specification, examples, tests and benchmarks | `pil/` | `Apache-2.0` | [Apache License 2.0](pil/LICENSE) |
| Shared Go encoding, dictionaries, schemas and UUID helpers | `go/codec/` | `Apache-2.0` | [Apache License 2.0](go/codec/LICENSE) |
| Go logger client, outbound transport, definitions, examples and tests | `go/logger/` | `Apache-2.0` | [Apache License 2.0](go/logger/LICENSE) |
| Shared Go error models | `go/models/` | `Apache-2.0` | [Apache License 2.0](go/models/LICENSE) |

Directory licenses apply recursively unless a file or a more specific component
explicitly states otherwise. Rust manifests and Snoot's package metadata identify
their component licenses. Generated code and binaries retain the licensing
obligations of their source; a generated asset's destination directory does not
relicense it. In particular, `pil.wasm` is built from Apache-licensed PIL, while
`trunk-store.wasm` includes the AGPL-licensed browser store.

Both licenses permit commercial use. The AGPL governs covered derivatives and
includes a source-offer obligation to remote users of modified network versions.
It does not prohibit competing forks or require contributions to this repository.
See the license itself for the full terms, including distribution obligations.

Independent clients and wrappers can use separately chosen licenses. Using the
Apache-licensed SDKs or PIL codec does not by itself place an independent
application under the AGPL. Copying or combining AGPL implementation code may
create a covered work; a wrapper or plugin label does not determine that question.
Current implementation caveat: `go/logger/pil` imports the compiler and types
from `go/server/cache`, so that SDK dependency graph currently includes AGPL
server code. Component labels do not remove dependency licensing obligations.

Third-party dependencies retain their own licenses and notices. These component
licenses do not replace third-party terms or remove existing notices. Retain the
applicable license texts and notices when redistributing components.

The [trademark policy](#trademark-policy) addresses product names and logos separately.
It adds no restriction to the software licenses' permission to use, modify,
redistribute, or fork the code.

Source repository: https://github.com/AuthenticityLabs/trie-trunk

## Trademark Policy

The trie-trunk and Snoot names and project logos identify the project and its
official releases maintained by AuthenticityLabs. The software licenses grant
copyright and other expressly stated rights; they do not grant general rights
to use these marks as your own product identity or to imply endorsement.

You may use the names to describe the project accurately, credit its origin,
document compatibility, discuss or review it, or identify an unmodified official
release. For example, "Acme Tools for trie-trunk" and "Compatible with PIL"
are acceptable factual descriptions when they do not imply official sponsorship.
Use of a protocol's name to identify compatibility is not a claim of endorsement.

Independently maintained forks, modified distributions, extensions, wrappers and
hosted products should use their own primary product names and branding. They
may accurately state that they are based on, integrate with, or are compatible
with trie-trunk or Snoot. Clearly distinguish your product and maintainer from
the official project. Do not use the project logos as your own product logo or
describe an independent product as an official release without permission.

Keep required copyright, license and attribution notices when changing branding.
This policy does not prohibit forks, competing implementations, commercial use,
or redistribution allowed by the software licenses. It does not limit fair use,
nominative use, or other rights granted by applicable law, and does not claim
that a mark is registered.

For branding permission beyond the uses above, contact the project maintainers
through https://github.com/AuthenticityLabs/trie-trunk.
