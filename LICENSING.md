# Licensing

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
The Apache components do not depend on this repository's AGPL server code.

Third-party dependencies retain their own licenses and notices. These component
licenses do not replace third-party terms or remove existing notices. Retain the
applicable license texts and notices when redistributing components.

The [trademark policy](TRADEMARKS.md) addresses product names and logos separately.
It adds no restriction to the software licenses' permission to use, modify,
redistribute, or fork the code.

Source repository: https://github.com/AuthenticityLabs/trie-trunk
