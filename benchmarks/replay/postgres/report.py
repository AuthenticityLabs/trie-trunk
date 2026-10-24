#!/usr/bin/env python3
"""Summarize the saved JSON; medians of three separate round percentiles."""
import csv,json,pathlib,statistics,sys
root=pathlib.Path(sys.argv[1]) if len(sys.argv)>1 else pathlib.Path(__file__).resolve().parent.parent/'2026-10-18'
scenarios=['o10000_r1_b256','o10000_r10_b256','o10000_r100_b256','o10000_r100_b4096','o100000_r10_b256','o1000_r1000_b256']
data={}
rows=[]
for variant in ['results','exact-read-probe']:
 for name in scenarios:
  d=json.loads((root/variant/(name+'.json')).read_text()); groups={}
  for r in d['results']:groups.setdefault(r['name'].split('/')[0],[]).append(r)
  stats={k:{m:statistics.median(r[m] for r in rs) for m in rs[0] if m!='name'} for k,rs in groups.items()}
  data[variant,name]=(d,stats)
  for method,metrics in stats.items():rows.append(dict(variant=variant,scenario=name,method=method,**metrics))
with (root/'summary.csv').open('w') as f:
 w=csv.DictWriter(f,fieldnames=list(rows[0]));w.writeheader();w.writerows(rows)
lines=[]
def put(s=''):
 if s.startswith('#') and lines and lines[-1]:lines.append('')
 lines.append(s)
 if s.startswith('#'):lines.append('')
def table(headers,rows):
 put('| '+' | '.join(headers)+' |');put('| '+' | '.join(['---']*len(headers))+' |')
 for row in rows:put('| '+' | '.join(map(str,row))+' |')
 put()
def val(name,method,variant='results',metric='p50_us'):
 return data[variant,name][1][method][metric]
def ms(name,method,variant='results'):
 return f'{val(name,method,variant)/1000:.3f}'
put('# Object replay versus PostgreSQL current-object lookup')
put('\nDate: 2026-10-18. Synthetic local comparison; no production changes or deployment.\n')
put('PostgreSQL currently wins complete-object retrieval in every tested production-reader scenario. Incremental replay reduces response bytes, especially when an unchanged object body is large, but it does not consistently reduce local latency. An isolated exact-payload-read probe demonstrates a fixable implementation cost; it does not make replaying long histories competitive with retrieving one already-materialized row.\n')
put('## Method and fairness')
put('''- Apple M1 Max, 32 GiB RAM, macOS 27.0.1; Go 1.26.4; PostgreSQL 16.14. Same local machine and filesystem, warm filesystem/database caches. PostgreSQL ran in a temporary isolated cluster on port 15439, with 256 MB shared buffers; fsync, synchronous_commit and full_page_writes enabled. Existing application databases were not used.
- Six datasets: 10,000–100,000 objects plus a deep-history case with 1,000 objects; 10,000–1,000,000 events per dataset. Each object has 11 fields (12 in PIL including event_id). One complete creation is followed by sparse changes to two attributes, with event/object/revision metadata on every event. Updates exercise NULL and ABSENT separately. A distinct, deterministic 256-byte or 4,096-byte string remains unchanged after creation. This models small mutations of otherwise stable objects; text-heavy changes would reduce the delta advantage.
- PostgreSQL uses a regular typed table and bigint primary key, not a JSON document scan. pgx caches prepared statements, uses binary decoding where supported, and has eight pre-established connections. The saved EXPLAIN plans confirm primary-key index scans. No artificial delays, joins, or unindexed queries were added.
- Trie-Trunk uses the current public Endpoint.History path, optional nuts.idx, native PIL population storage, cached compiled definition, adaptive integers level=16, and no aggregates. Events are interleaved by revision across objects, rather than artificially placing each object's history together.
- For the primary HTTP comparison, both backends run behind the same Go server, encode the same JSON object, and use the same client JSON decoder. Trie-Trunk reconstruction is a benchmark adapter, not an existing production complete-object endpoint. Direct PostgreSQL timing includes the pgx/SQL loopback exchange and typed decoding; direct Trie-Trunk timing is an in-process storage call plus PIL decoding and patch application. These are separately labeled, not treated as identical transport costs.
- The production /objects/history HTTP path is also measured with its actual TPIL framing, cached Definition-Known, completion trailer, and client-side reconstruction. PostgreSQL-to-full-PIL provides a control so that delta savings are not attributed only to PIL versus JSON.
- Each case has three rounds of 300 randomized object lookups, rotating case order between rounds. Tables show the median of the three per-round p50s/p95s, not pooled percentiles. Every measured output must equal the independently generated complete PostgreSQL fixture. Sequential and eight-concurrent-request cases are separate. Quantiles measure operation latency through decode/reconstruction, excluding validation. Throughput includes validation and harness overhead.
- Go allocation measurements are a separate 50-operation pass. HTTP allocations include both client and server in the benchmark process. PostgreSQL backend allocations are not included, so these are not whole-system memory or CPU cost comparisons. No disk cache purge, WAN, TLS/QUIC, browser worker, OPFS, concurrent writes, or cloud billing was measured.
''')
put('## Complete object: same Go HTTP path and JSON response')
rs=[]
for name in scenarios:
 d,_=data['results',name]
 rs.append([f"{d['objects']:,}",d['revisions_per_object'],d['description_bytes'],ms(name,'postgres_http_current_json'),ms(name,'trunk_http_reconstructed_json'),f"{val(name,'trunk_http_reconstructed_json')/val(name,'postgres_http_current_json'):.1f}×",f"{val(name,'postgres_http_current_json',metric='p95_us')/1000:.3f}",f"{val(name,'trunk_http_reconstructed_json',metric='p95_us')/1000:.3f}"])
table(['Objects','Revisions/object','Text bytes','Postgres p50 ms','Trie replay p50 ms','Replay / PG','PG p95 ms','Replay p95 ms'],rs)
put('PostgreSQL has already materialized the latest object at write time. Trie-Trunk reconstructs it on each request. Both outputs contain the same object, but the underlying work is different; this is the architectural tradeoff being tested.\n')
put('## Direct storage/DB and the isolated read-size probe')
rs=[]
for name in scenarios:
 d,_=data['results',name]
 rs.append([f"{d['objects']:,} / {d['revisions_per_object']} / {d['description_bytes']}",ms(name,'postgres_direct_current'),ms(name,'trunk_native_full_replay'),ms(name,'trunk_native_full_replay','exact-read-probe'),f"{val(name,'trunk_native_full_replay',metric='go_bytes_op')/1024:.1f}",f"{val(name,'trunk_native_full_replay','exact-read-probe','go_bytes_op')/1024:.1f}"])
table(['Objects / revisions / text B','Direct PG p50 ms','Native replay p50 ms','Exact-read probe p50 ms','Replay Go KiB/op','Probe Go KiB/op'],rs)
put('''The production populationReader reads ahead up to 1 MiB on a buffer miss. This helps sequential scans but is wasteful for sparse object references. The probe changes only `through := start + 1<<20` to `through := end` via a Go build overlay, leaving repository production code untouched and reusing the same seeded files and PostgreSQL rows. It is a diagnostic build, **not** a recommendation to disable read-ahead for time scans. A production improvement should choose/coalesce reads according to access pattern.

For 10,000 objects with 100 revisions and a 256-byte description, native replay improves from %.3f to %.3f ms; Go allocation drops from %.1f to %.1f KiB/op. Nevertheless, the probe's same-JSON HTTP result is %.3f ms versus PostgreSQL's %.3f ms in that probe run. With 1,000 revisions, exact reads improve little: per-history work and syscalls remain substantial.
'''%(val('o10000_r100_b256','trunk_native_full_replay')/1000,val('o10000_r100_b256','trunk_native_full_replay','exact-read-probe')/1000,val('o10000_r100_b256','trunk_native_full_replay',metric='go_bytes_op')/1024,val('o10000_r100_b256','trunk_native_full_replay','exact-read-probe','go_bytes_op')/1024,val('o10000_r100_b256','trunk_http_reconstructed_json','exact-read-probe')/1000,val('o10000_r100_b256','postgres_http_current_json','exact-read-probe')/1000))
put('## Cached-object catch-up and response bytes')
rs=[]
for name in scenarios[1:]:
 d,_=data['results',name]
 rs.append([f"{d['objects']:,} / {d['revisions_per_object']} / {d['description_bytes']}",ms(name,'postgres_http_current_json'),ms(name,'trunk_http_delta1_pil'),f"{val(name,'postgres_http_current_json',metric='response_body_bytes'):.0f}",f"{val(name,'postgres_http_current_pil',metric='response_body_bytes'):.0f}",f"{val(name,'trunk_http_delta1_pil',metric='response_body_bytes'):.0f}",f"{val(name,'trunk_http_full_history_pil',metric='response_body_bytes'):.0f}"])
table(['Objects / revisions / text B','Full PG JSON p50 ms','One-change PIL p50 ms','Full JSON B','Full PIL B','One-change TPIL B','Full-history TPIL B'],rs)
put('''A delta run starts from an already-correct object one revision behind, with its precise last event timestamp available. It includes lookup, transfer, decoding and applying the change, but excludes the original cost of acquiring that base. Ten-change and zero-change runs are in summary.csv. Full history has no preexisting base. The standalone full-PIL control has no TPIL stream envelope, making that byte comparison conservative for the replay stream.

For the 4 KiB text case, full JSON is about 4,292 bytes, full PIL about 4,160 bytes, and one-change TPIL about 119 bytes: **97.2% fewer response-body bytes than JSON**, or **97.1% fewer than full PIL**. This is mainly a delta-versus-snapshot benefit. Replaying all 100 revisions transfers about 7,411 bytes, more than either full-object representation.

These are uncompressed response bodies, not packet capture or billable transfer: request/response headers, TLS/QUIC framing, definitions on first contact, producer replication, retry traffic and state-validation requests are additional. Direct-DB body bytes are recorded as zero to indicate “not measured,” not zero PostgreSQL network usage.
''')
put('## Eight concurrent requests')
rs=[]
for name in scenarios[1:]:
 d,_=data['results',name]
 rs.append([f"{d['objects']:,} / {d['revisions_per_object']} / {d['description_bytes']}",ms(name,'postgres_http_current_json_concurrency8'),ms(name,'trunk_http_reconstructed_json_concurrency8'),ms(name,'trunk_http_delta1_pil_concurrency8')])
table(['Objects / revisions / text B','PG full p50 ms','Replay full p50 ms','Replay one-change p50 ms'],rs)
put('These are closed-loop, fixed-concurrency local runs; they are not a load-generator saturation or maximum-throughput test. PostgreSQL scales better here. The history implementation selects/copies postings while holding a shared store-group mutex; payload callbacks themselves run outside that lock.\n')
put('## Storage cost and work not avoided')
rs=[]
for name in scenarios:
 d,_=data['results',name];files=d['trunk_files_bytes']
 rs.append([f"{d['objects']:,} / {d['revisions_per_object']} / {d['description_bytes']}",f"{d['postgresql_table_and_indexes_bytes']/1e6:.2f}",f"{sum(files.values())/1e6:.2f}",f"{files.get('nuts.idx',0)/1e6:.2f}"])
table(['Objects / revisions / text B','PG current table + indexes MB','Trie complete history MB','Included nuts.idx MB'],rs)
put('''Storage counts are intentionally different responsibilities: PostgreSQL stores current objects, while Trie-Trunk stores their retained history and replay index. Do not call their ratio a codec compression comparison. PostgreSQL WAL/cluster overhead, backups, in-memory indexes and existing origin storage are excluded; Trie-Trunk totals include its generated files. Distribution adds the Trie-Trunk storage to the origin's storage; it does not replace it. Seed times are retained as fixture diagnostics but are not comparable ingestion benchmarks: loading current PostgreSQL rows is not performing the corresponding history of transactions.

A cost decision needs measured production rates. Let `R` be incremental origin→Trie-Trunk replication bytes, `Q` the number of full-object origin fetches avoided, and `S` their average response size. Origin-transfer savings require `Q × S > R`; monetary savings must additionally exceed Trie-Trunk compute, storage and operations. For client-facing traffic, a trusted existing base allows each `S`-byte fetch to become a `D`-byte delta, but that does not automatically reduce server CPU. In a zero-per-byte local deployment, avoiding reads on an otherwise idle PostgreSQL instance may not reduce the bill at all.
''')
put('## Correctness and current implementation boundaries')
put('''1. **Only finalized writes are replayed.** The fixture deliberately advances beyond the three-cycle / 15-minute staging boundary. These results do not establish immediate visibility of recent origin writes. The existing endpoint advertises retained history, not an authoritative current-object snapshot.
2. **A complete replay requires a valid base and uninterrupted mutations.** The benchmark supplies an initial full creation and all following changes. Retention removing that creation, revision gaps, errors/invalidation, deletion semantics, multiple definition minors and concurrent updates require additional contracts/tests before treating replay as a current-state API.
3. **Delta selection currently walks all of an object's postings.** objectLocations copies revision-bearing locations before snapshotHistory filters by time. Consequently a one-change or zero-change request gets slower with lifetime history: at 1,000 revisions, the production one-change HTTP p50 is about 0.615 ms, and zero-change about 0.554 ms. Exact payload reads do not remove that cost. The separate /objects/state token endpoint was not benchmarked and is not equivalent to a positive new-write revision cursor.
4. **Materialized local state is a different path.** The local_materialized_object microcase is only an in-process struct/array access lower bound. It is not a measured browser archive, worker round-trip, durable cache, freshness validation or deployed Trie-Trunk snapshot implementation. It must not be advertised as the application's object lookup latency.
5. PostgreSQL tests one indexed table with no joins, row-level authorization work, concurrent mutation or WAN. Real origin queries may be more expensive, but that must be measured against those queries, not assumed.
''')
put('## Historical recommendation — scope corrected')
put('The intended comparison is subscription delivery: a Go publisher sends a full object from PostgreSQL versus Trie-Trunk sending a partial revision to a subscriber that already has a valid base. Complete-object reconstruction inside Trie-Trunk is outside the design and is not an acceptance criterion. Existing measurements are request/response reader tests, not persistent SSE subscription results.\n\n')
put('''Keep PostgreSQL as the default source for an initial complete object and uncertain-state reconciliation. Use Trie-Trunk for retained history and incremental catch-up where a valid local base exists. Repeated local evaluation and one-ingress/many-consumer fanout remain plausible advantages, but this benchmark does not prove total-cost savings or a faster generic replacement for primary-key PostgreSQL reads.

Before making a broader replay-cache claim:

1. Add an object-access read strategy that avoids the 1 MiB sequential buffer for scattered references, retaining read-ahead for time scans. Benchmark the mixed workloads before deployment.
2. Bound object-posting selection by a continuation/time position instead of copying the whole history for every delta or no-change request. Preserve opaque revision semantics, reference validity and invalidation behavior.
3. Benchmark persistent full-object SSE publication from PostgreSQL against partial-revision delivery from Trie-Trunk, including origin replication, durable acknowledgement, fanout and client application. Complete-object checkpoints/reconstruction inside Trie-Trunk are outside scope.

No production storage, APIs or reader code were changed in this work. The exact-read variant exists only as a temporary build overlay. The temporary PostgreSQL instance was stopped after testing.
''')
put('## Artifacts and reproduction')
put('''- `results/*.json`: production-reader measurements, PostgreSQL plans/settings and file sizes.
- `exact-read-probe/*.json`: the same stored fixtures through the isolated point-read build.
- `summary.csv`: medians of round statistics, including allocations and concurrency.
- `environment.txt` and `source-sha256.json`: host and relevant source fingerprints. The repository had existing uncommitted work; these hashes identify the reviewed reader code.
- `../postgres/main.go`: benchmark and explicit full-object reconstruction adapter.
- `../postgres/main_test.go`: complete/sparse/catch-up equivalence, null preservation and full-PIL control tests.
- `../postgres/run.sh`: starts a fresh PostgreSQL cluster, runs both variants and cleans up its own temporary data. Requires PostgreSQL and Go in PATH. Run `benchmarks/replay/postgres/run.sh /absolute/path/to/new-results`; override PGPORT if 15439 is occupied.
- Regenerate this report with `python3 benchmarks/replay/postgres/report.py /absolute/path/to/results`.

Validation: all timed results checked for exact object equality; unit tests passed; the existing replay/invalidation, disabled-index/lossless-ID and history-snapshot tests passed under the exact-read overlay. No claim is made about untested production browser or cross-host behavior.
''')
(root/'REPORT.md').write_text('\n'.join(lines))
