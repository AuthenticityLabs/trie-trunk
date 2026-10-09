# trie-trunk

Transaction logging with dictionary-encoded CBOR, shared trie paths, UUIDv7
indexing and five-minute aggregate rollups. The [Go module](go/README.md)
includes the producer, TLS WebSocket connector, WebSocket/HTTP3 ingestion, and
recoverable storage. No Java checkout is required.

Start with [ingestion setup](go/server/ingest/README.md) and the
[storage format](go/server/store/FORMAT.md). Search runs in Rust, with JSON streamed
through the CLI or [HTTP event server](go/server/serve/README.md). The
[snoot web app](client/README.md) supports event search, live follow, JSONL export,
and summaries of loaded results. Alert evaluation and automatic higher-period
rollups remain to be implemented.

The default store is `./taps`. Definitions live under
`<tap>/<object>.v<major>/<minor>/events.map`, with a tap registry and a 30-day
trie-statistics snapshot per definition. See [object storage](go/server/store/OBJECTS.md)
for tap creation and statistics semantics.

The [partitioned event storage proposal](go/server/store/PARTITIONING-PROPOSAL.md)
now has a [server implementation](go/server/store/FORMAT-3.md): hourly resource
partitions, mature 26-byte indexes (format 4), resident caching, parallel scans and an offline
migration command. Native package replication into the browser remains the next stage.

Build the Solid app, Rust search executable, and Go CLI with `make build`. This
requires Node 22.12+, Go 1.24+, and Rust 1.88+. Deploy `trunk`, `trunk-search`, and
the generated `web` directory together. `make web` rebuilds only the app.
Run the native Rust/PIL and complete Go module tests with `make test`.
Run frontend tests with `npm --prefix client test`.
On the development Mac, prefix either command with
`DEVELOPER_DIR=/Library/Developer/CommandLineTools` if Xcode's linker is unavailable.

## Background server with snapi

The executable `trunk-server` starts
`trunk ingest 5410 serve 8443 --http-address 127.0.0.1:3020` from this
repository directory. Snoot's Cloudflare Tunnel origin is `http://localhost:3020`;
the direct HTTPS listener remains on 8443. It loads the existing `.env` and environment profile.
The launcher uses `exec` so launchd supervises trie-trunk directly.

Set the registration identity in `.env`:

```dotenv
SERVER_NAME=trie-trunk
SERVER_TYPE=app
SERVER_INSTANCE=01
ENVIRONMENT=sand
```

Register the launcher using its absolute path:

```sh
snapi local register "$PWD/trunk-server"
snapi local:trie-trunk.sand.app-01 status
snapi local:trie-trunk.sand.app-01 logs
snapi local:trie-trunk.sand.app-01 restart
snapi local:trie-trunk.sand.app-01 stop
snapi local:trie-trunk.sand.app-01 start
```

On macOS this creates a per-user LaunchAgent that starts at login and restarts
when the process exits. Stop any manually started instance before registering
so the listeners and local socket have one owner. Use `unregister` to remove the
service. After rebuilding `trunk` or `trunk-search`, restart the service; frontend
builds only require reloading the browser.

## License

trie-trunk and Snoot are licensed under [AGPL-3.0-only](LICENSE).
PIL and the Go codecs, logger SDK and shared models use Apache-2.0.
See [component licensing](LICENSING.md) for exact boundaries and
[trademark policy](TRADEMARKS.md) for use of the names and logos.
