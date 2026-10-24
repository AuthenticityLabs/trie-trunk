#!/usr/bin/env python3
"""Summarize paired reader measurements; medians of the per-round percentiles."""
import csv,json,pathlib,statistics,sys
root=pathlib.Path(sys.argv[1])
names=['o10000_r10_b256','o10000_r100_b256','o1000_r1000_b256','o10000_r100_b4096']
data={};rows=[]
for variant in ['before','resident','live','cold']:
 for name in names:
  d=json.loads((root/variant/(name+'.json')).read_text());groups={}
  for r in d['results']:groups.setdefault(r['name'].split('/')[0],[]).append(r)
  metrics={k:{m:statistics.median(r[m] for r in rs) for m in rs[0] if m!='name'} for k,rs in groups.items()}
  data[variant,name]=(d,metrics)
  for method,m in metrics.items():rows.append(dict(variant=variant,scenario=name,method=method,**m))
with (root/'summary.csv').open('w') as f:
 w=csv.DictWriter(f,fieldnames=list(rows[0]));w.writeheader();w.writerows(rows)
lines=[]
def put(s=''):lines.append(s)
def table(headers,rs):
 put('| '+' | '.join(headers)+' |');put('| '+' | '.join(['---']*len(headers))+' |')
 for r in rs:put('| '+' | '.join(map(str,r))+' |')
 put()
def metric(v,n,m,k='p50_us'):return data[v,n][1][m][k]
def ms(v,n,m):return f'{metric(v,n,m)/1000:.3f}'
def label(n):
 d=data['resident',n][0];return f"{d['objects']:,} / {d['revisions_per_object']} / {d['description_bytes']:,}"
put('# Live replay and the rolling 24-hour memory window\n')
put('Date: 2026-10-18. Implementation and local measurements; no production deployment.\n')
put('The 15–20 minute staging gate now affects permanent-file promotion, not acknowledged-event visibility. The audit found that object history and paged views had excluded pending events, and finalized payloads relied on the OS page cache. Both gaps are corrected. The new resident reader substantially improves partial-revision retrieval. Complete-object reconstruction was a benchmark-only experiment outside the intended design.\n')
put('The intended comparison is subscription delivery: a Go publisher sends a full object from PostgreSQL versus Trie-Trunk sending a partial revision to a subscriber that already has a valid base. Complete-object reconstruction inside Trie-Trunk is outside the design and is not an acceptance criterion. Existing measurements are request/response reader tests, not persistent SSE subscription results.\n\n')
put('## Changes\n')
put('''- Full encoded PIL frames remain in a rolling 24-hour process-memory cache, shared with the pending tail and retained after finalization. Restart rebuilds it from durable files and swap. No intermediate JSON/object cache is introduced.
- Object history merges committed nuts.idx postings with a per-object pending directory. Time-bounded lookups binary-search sorted per-source postings; they no longer copy every revision before filtering. An already sorted pending tail is not sorted again for each read.
- Older scattered object payloads use exact-range reads. Sequential scans retain their buffered read-ahead.
- Paged search, counts and statistics include pending events. A continuation pins its original pending set across disk promotion and new arrivals. Provisional references do not certify disk/bucket completeness.
- The 26-byte canonical index, optional nuts.idx, PIL frame encoding and stored payloads are unchanged. View metadata adds provisional references and live-snapshot summary status. Pending history callbacks use a non-resolvable ProvisionalRow sentinel; event UUID remains canonical.
''')
put('## Out-of-scope diagnostic: complete-object reconstruction\n')
put('Milliseconds; median of three per-round p50s. Both adapters return the same JSON object through the same Go HTTP server and client decoder. PostgreSQL values shown alongside resident replay are from that same run.\n')
method='trunk_http_reconstructed_json'
table(['Objects / revisions / text bytes','Before replay','Resident replay','Live-tail replay','Older disk reader','PostgreSQL current'],[[label(n),ms('before',n,method),ms('resident',n,method),ms('live',n,method),ms('cold',n,method),ms('resident',n,'postgres_http_current_json')] for n in names])
put('Live-tail replay can be faster than finalized resident replay because its per-object pending directory already holds frame references. Finalized history still resolves nuts.idx rows through the canonical event index. Both return the complete expected object. “Older disk reader” means outside the application memory window with warm OS caches, not physical-disk-cold I/O.\n')
put('## Incremental replay from a trusted existing object\n')
method='trunk_http_delta1_pil'
table(['Objects / revisions / text bytes','Before one change','Resident one change','Live one change','Older one change','Resident p95'],[[label(n),ms('before',n,method),ms('resident',n,method),ms('live',n,method),ms('cold',n,method),f"{metric('resident',n,method,'p95_us')/1000:.3f}"] for n in names])
put('One-change timings include the actual TPIL history response and applying the patch to the trusted base. They are not equivalent to fetching a complete object without a base. The fixture uses sparse two-attribute changes and retains an unchanged description.\n')
table(['Text bytes / revisions','Full JSON bytes','Full PIL bytes','One-change TPIL bytes','No-change TPIL bytes'],[[f"{data['resident',n][0]['description_bytes']} / {data['resident',n][0]['revisions_per_object']}",round(metric('resident',n,'postgres_http_current_json','response_body_bytes')),round(metric('resident',n,'postgres_http_current_pil','response_body_bytes')),round(metric('resident',n,'trunk_http_delta1_pil','response_body_bytes')),round(metric('resident',n,'trunk_http_no_changes_pil','response_body_bytes'))] for n in names])
put('These are response bodies, not billable network totals. The changes save reader work, not additional wire bytes.\n')
put('## Direct reader and Go allocation cost\n')
method='trunk_native_full_replay'
table(['Objects / revisions / text bytes','Before p50 µs','Resident p50 µs','Older p50 µs','Before KiB/op','Resident KiB/op','Older KiB/op'],[[label(n),f"{metric('before',n,method):.1f}",f"{metric('resident',n,method):.1f}",f"{metric('cold',n,method):.1f}",*[f"{metric(v,n,method,'go_bytes_op')/1024:.1f}" for v in ['before','resident','cold']]] for n in names])
put('The direct reader includes PIL decoding and reconstruction in-process. It is not directly equivalent to the SQL driver exchange, so the primary comparison above uses the same HTTP boundary.\n')
put('## Memory, startup and storage\n')
table(['Objects / revisions / text bytes','Before open heap MB','Live-tail open heap MB','Older open heap MB','Before open s','Live-tail open s'],[[label(n),*[f"{data[v,n][0]['trunk_open_heap_bytes']/1e6:.1f}" for v in ['before','live','cold']],f"{data['before',n][0]['trunk_open_seconds']:.2f}",f"{data['live',n][0]['trunk_open_seconds']:.2f}"] for n in names])
put('''Heap figures are net Go HeapAlloc growth across endpoint open after GC, not RSS, locked RAM, or isolated payload-cache bytes. They include indexes and restored pending directories, and exclude mmap residency and PostgreSQL's separate process. The live run restores an entirely unfinalized fixture from swap. The resident run initially reopens that same swap and finalizes during untimed warmup, so its recorded open heap is deliberately not presented as finalized steady-state memory. A broader steady-state/ingestion memory profile remains needed.

Keeping 24 hours resident trades RAM and restart work for lower read latency. There is no fixed byte eviction cap that silently violates that window; capacity must match event volume. Idle query snapshots may retain shared frames temporarily: at most 64 live snapshots per branch, with 15-minute expiry and explicit restart on eviction/expiry. Reference arrays still consume memory per distinct snapshot.
''')
table(['Objects / revisions / text bytes','Before finalized files MB','After finalized files MB','PostgreSQL current table MB'],[[label(n),f"{sum(data['before',n][0]['trunk_files_bytes'].values())/1e6:.2f}",f"{sum(data['cold',n][0]['trunk_files_bytes'].values())/1e6:.2f}",f"{data['resident',n][0]['postgresql_table_and_indexes_bytes']/1e6:.2f}"] for n in names])
put('History storage and a current-object table have different responsibilities. These totals are not a codec compression ratio and exclude PostgreSQL WAL/cluster overhead.\n')
put('## Method, verification and limits\n')
put('''- Apple M1 Max / 32 GiB, Go 1.26.4, PostgreSQL 16.14, isolated temporary PostgreSQL cluster with durable settings and 256 MB shared buffers. No application database or production server was changed.
- Four datasets, 100,000–1,000,000 events. Each object starts complete, then receives sparse two-attribute changes. Events are interleaved across objects. Every measured result must equal the complete expected object.
- Three rounds of 300 randomized requests per method, plus concurrency-eight measurements in the raw results. Case order rotates. No test suite ran alongside the recorded final matrix. Numbers are local warm-cache measurements, not cloud/edge latency, saturation throughput or billed CPU.
- The before reader is built from saved pre-change sources using before.patch and Go's build overlay. Both readers use the same updated harness, PostgreSQL table and event dataset. The before reader runs after finalization because it cannot return the live tail correctly. Binary hashes are recorded.
- Logical clocks: live = one minute after the fixture; resident = one hour; older = 48 hours. Fixture duration is at most about 17 minutes, so the live fixture remains entirely in staging. Resident reads finalize during untimed warmup, retaining the same in-memory frames. Older reads bypass the application payload cache.
- Unit/integration checks cover immediate object replay, out-of-order pending arrival, same-millisecond ordering, object/time exclusion, disk promotion, restart warming, 24-hour expiry, SIGKILL durability, callback buffer isolation, pagination across finalization, snapshot expiry, and exclusion of provisional rows from completeness. The full cache suite passes the race detector; server HTTP/2, HTTP/3 and socket tests pass.
- No complete-object materialization endpoint was added: reconstruction is the benchmark adapter. Production replay still requires a valid base, uninterrupted mutations and error-invalidation handling. Delete/gap/concurrent-origin-mutation semantics are not performance-fixture claims.
- These reader timings do not establish persistent subscription latency or fanout cost. The relevant next comparison is full-object publication from PostgreSQL against partial-revision publication from Trie-Trunk, followed by client-side application. Do not use full-history reconstruction to judge that design.
''')
put('## Subscription benchmark scope (not yet measured)\n')
put('- Start both subscribers with the same valid object and deliver the same committed changes, preserving revision order. Full replacements and partial application must produce identical client state.\n- Use persistent connections and the same subscriber counts (1, 10, 100). Measure commit-to-client-application p50/p95/p99, CPU/allocations, wire bytes, reconnect catch-up, and slow-subscriber behavior.\n- Give the PostgreSQL publisher a fair fanout path: obtain/encode each changed object once and share it among subscribers; do not assume one SQL query per subscriber. State whether capture uses UPDATE RETURNING, CDC, or notification plus read.\n- Include origin-to-Trie-Trunk replication, durable acknowledgement and fanout work. Separate upstream replication bytes from downstream delivery bytes.\n- Use equivalent framing/compression controls. Literal SSE is text-framed, so any binary PIL encoding overhead must be included. The existing TPIL request/response bodies are not measured SSE message sizes.\n- No complete-object checkpoint or server-side object reconstruction feature is proposed for Trie-Trunk.\n\n')
put('## Reproduce\n')
put('```sh\nBASELINE_PATCH=benchmarks/replay/2026-10-18-live-replay/before.patch \\\n  benchmarks/replay/postgres/run-live.sh /absolute/path/to/new-results\npython3 benchmarks/replay/postgres/report-live.py /absolute/path/to/new-results\n```\n')
put('The reverse patch applies to this implementation snapshot. If later reader changes prevent application, use this source snapshot; do not silently substitute a different baseline. Temporary databases and fixtures are removed by the runner. Raw measurements and summary.csv retain per-case p95/p99, allocation and concurrency results.')
(root/'REPORT.md').write_text('\n'.join(lines)+'\n')
