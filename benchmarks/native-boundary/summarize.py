#!/usr/bin/env python3
import collections, json, pathlib, statistics, sys
source=pathlib.Path(sys.argv[1]);rows=[json.loads(x) for x in source.read_text().splitlines()]
envfile=source.with_suffix('.environment.json')
env=json.loads(envfile.read_text()) if envfile.exists() else {}
groups=collections.defaultdict(list)
for r in rows:
    c=r['config'];groups[(c['TLS'],c['Bytes'],c['Streams'],c['Mode'])].append(r)
lines=['# Native Boundary Probe — Initial Results','',
'Ownership/transport experiment only; not a production datastore comparison.', '',
f'Environment: {env.get("platform","not recorded")}; {env.get("go","")}; {env.get("rust","")}.',
f'Repository HEAD `{env.get("head","unknown")}`; dirty worktree: {env.get("dirty","unknown")}.',
f'{len(rows)} fresh-process samples: 64 KiB chunks, {env.get("repeats","unknown")} repetitions per case, sequential',
'runs with rotating mode order. HTTP/1.1 and Go TLS use loopback networking.',
'Each request retains all received bytes until the memory measurement.', '',
'All numbers below are medians. RSS includes the client, server and both runtimes.',
'Native payload columns exclude native allocator/directory overhead; RSS includes it.',
'Heap deltas are measured after a forced GC with payloads still reachable. The',
'forced GC is excluded from ingest time and ingest GC counts.', '',
'| TLS | MiB Per Request | Requests | Mode | Ingest ms | Go Live Δ MiB | Native Payload MiB | Go Allocated MiB | Peak RSS MiB | Ingest GCs |',
'| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |']
for key,rs in sorted(groups.items()):
    tls,n,streams,mode=key
    med=lambda k:statistics.median(r[k] for r in rs)
    lines.append(f'| {tls} | {n/(1<<20):g} | {streams} | {mode} | {med("ingest_ms"):.2f} | {med("go_live_heap_delta_bytes")/(1<<20):.3f} | {med("retained_native_payload_bytes")/(1<<20):g} | {med("go_total_alloc_bytes")/(1<<20):.3f} | {med("process_peak_rss_bytes")/(1<<20):.2f} | {med("go_gc_cycles_during_ingest"):g} |')
lines+=['','## Interpretation','',
'- Native ownership removes payload-proportional Go heap growth in this probe.',
'  It does not remove the payload from process memory: native-only and Go-only',
'  retention have broadly similar total RSS. Deliberate duplicate retention',
'  adds approximately another payload-sized resident allocation.',
'- `copy` explicitly copies every byte into Rust but retains only a reusable Go',
'  scratch buffer. `direct` avoids that application copy and its scratch allocation.',
'  Direct reads are not consistently faster in these short samples; do not infer',
'  that the avoided copy alone establishes a production throughput win.',
'- Single-stream native retention caused no automatic ingest GC in these samples.',
'  Eight concurrent TLS requests still triggered Go GC. Connection, TLS and HTTP',
'  allocations remain even when resident records belong to Rust.',
'- The experiment includes native buffer allocation and one checksum call per',
'  chunk, not one crossing per record. It does not parse/decompress PIL, fsync,',
'  maintain indexes, seal hours or replay. All-Rust transport is unmeasured.',
'- Three samples per case are a smoke comparison, not confidence intervals or',
'  production tail-latency evidence. Use sustained matched workloads for the engine port.', '',
'## Reproduce','',
'```sh',
'sh benchmarks/native-boundary/build.sh',
'python3 benchmarks/native-boundary/run.py --output /tmp/native-boundary.jsonl',
'python3 benchmarks/native-boundary/summarize.py /tmp/native-boundary.jsonl',
'```','',
'The summarizer writes REPORT.md beside the sample file. The runner captures',
'environment metadata and probe source hashes beside samples. Raw samples retain',
'copy counts, cgo call counts, heap object counts, checksums and GC pause time.', '',
'See [the engine plan](../../../NATIVE-ENGINE-PLAN.md) for migration gates and',
'[the probe](../../native-boundary/README.md) for limitations.']
(source.parent/'REPORT.md').write_text('\n'.join(lines)+'\n')
