# Sample week

Generate seven days of synthetic events for snoot. The generator writes framed
CBOR for the existing ingest endpoint; it does not change logger definitions or
write directly into store files. Every event has `environment=demo` and
`synthetic=true`. Use the dedicated `demo-week` tap.

From the repository root, with `trunk ingest 5410 serve 8443` running:

```sh
sample=$(mktemp -d)
(cd go && go run ./server/ingest/sample -out "$sample/week")
digest=$(node -p "JSON.parse(require('fs').readFileSync('$sample/week/manifest.json')).digest")

curl --fail-with-body --cacert certs/client-ca.pem \
  --cert certs/client.pem --key certs/client-key.pem \
  -H 'Content-Type: application/cbor' \
  -H 'Trunk-Version: 0.2' -H "Trunk-Digest: $digest" \
  -X PUT --data-binary "@$sample/week/schema.cbor" \
  https://localhost:5410/demo-week

curl --fail-with-body --cacert certs/client-ca.pem \
  --cert certs/client.pem --key certs/client-key.pem \
  -H 'Content-Type: application/cbor' \
  -H 'Trunk-Version: 0.2' -H "Trunk-Digest: $digest" \
  --data-binary "@$sample/week/events.cbor" \
  https://localhost:5410/demo-week
```

These commands use the existing sandbox mTLS identity. Token-authenticated
listeners additionally require their normal Authorization header.

The default window ends at the current UTC minute. `-end 2026-09-26T20:48:00Z`
reproduces a fixed window. Generation is deterministic for the same end time;
replaying the same files deduplicates by UUIDv7. A different end time creates a
new dataset, so use a different tap if you want to keep the weeks separate.
The output directory must be new. The server's normal ingestion age limits apply.

Each run produces 3,878 events, including 14 lifecycle events. Traffic is lower
at night and higher from 12:00–18:00 UTC. Four services share dictionary values:
gateway, billing, vault, and worker. Request durations are microseconds. Normal
requests have roughly a 2% error probability; incident requests have elevated
error probability and latency. Exact observed rates depend on the generated
traffic mix.

| Hours from start | Scenario |
| --- | --- |
| 26–27:38 | Billing deployment, restart, high errors, rollback, then recovery |
| 67:40–68:25 | Worker failures before a restart, then recovery |
| 113–114:15 | Vault deployment and restart followed by an elevated error period |
| 153–153:05 | Healthy gateway deployment and restart for comparison |
| 165:55–166:08 | A short billing failure burst, restart, and recovery |

`manifest.json` records the window, counts, revision digest, and milestone offsets.
The schema stays fixed across generated weeks; durations, instance names, release
strings and the synthetic flag are literal CBOR values. Service, level, resource,
operation and event type are dictionary references in shared trie paths. Messages
also use dictionary references, but live in each event’s population (`log.pop`),
not in the trie. Schema version 2 changes this placement without changing event
IDs, field keys, or message dictionary values.

In snoot, choose **Explore sample week** from the initial search screen, or set
**Tap: demo-week**, **Last 7 days**, **5,000 events**. This keeps a complete freshly
generated week within the browser's 5,000-event retention limit. For older data,
use the manifest's exact start/end in the custom UTC window. The CLI equivalent:

```sh
./trunk search now-7d --tap demo-week --tail 5000
./trunk search now-7d --tap demo-week --where 'event_type=="server_restart"'
```

Red segments show error volume. Browser-side signatures in
`client/services/signatures.ts` label deployment, restart, readiness and rollback
events. Click a timeline bubble, then its event to inspect the original data.
These are sample operational scenarios, not measured causal relationships.
