#!/usr/bin/env python3
"""Combine native and Go measurements; run from the repository root."""
import json, re, statistics, sys
from pathlib import Path
out = Path(__file__).resolve().parent
root = Path(sys.argv[1] if len(sys.argv) > 1 else '/tmp/trunk-prefix-tradeoff')
native = json.loads((root / 'object_lookup_native.json').read_text())
(out / 'native.json').write_text(json.dumps(native, indent=2) + '\n')
pattern = re.compile(r'BenchmarkObjectLookupComparison/(\w+)/(\w+)/(\w+)-\d+\s+\d+\s+([\d.]+) ns/op\s+(\d+) B/op\s+(\d+) allocs/op')
groups = {}
for case, kind, engine, ns, size, allocs in pattern.findall((out / 'latency.txt').read_text()):
    key = (case, kind, engine)
    groups.setdefault(key, []).append((float(ns), int(size), int(allocs)))
assert len(groups) == 12 and all(len(v) == 3 for v in groups.values()), 'incomplete benchmark'
go = []
for (case, kind, engine), samples in groups.items():
    go.append(dict(case=case,kind=kind,engine=engine,median_run_mean_us=statistics.median(x[0] for x in samples)/1000,bytes_per_op=statistics.median(x[1] for x in samples),allocs_per_op=statistics.median(x[2] for x in samples),runs=samples))
(out / 'summary.json').write_text(json.dumps(dict(native=native,go=go),indent=2)+'\n')
for case in ['dense50k','dense250k']:
    for kind in ['history','revision7','missing']:
        a = next(x for x in go if x['case']==case and x['kind']==kind and x['engine']=='collection')
        b = next(x for x in go if x['case']==case and x['kind']==kind and x['engine']=='tantivy_rpc')
        n = {x['mode']:x['median_us'] for x in native if x['case']==case and x['kind']==kind}
        print(f"| {case} | {kind} | {a['median_run_mean_us']:.2f} | {n['current']:.2f} | {b['median_run_mean_us']:.2f} | {n['exact_only_control']:.2f} |")
