#!/usr/bin/env python3
"""Collect reproducible artifacts; never infer fixed byte costs for Tantivy U64s."""
import json, pathlib, re, shutil, statistics, sys
root = pathlib.Path(sys.argv[1])
out = pathlib.Path(__file__).resolve().parent
results = json.loads((root / 'tantivy.json').read_text())
shutil.copyfile(root / 'tantivy.json', out / 'tantivy.json')
cases = {}
for name in sorted({r['case'] for r in results}):
    folder = root / name
    f = json.loads((folder / 'fixture.json').read_text())
    p = json.loads((folder / 'packed_collection.json').read_text())
    publication = json.loads((folder / 'collection_publication.json').read_text())
    search = {r['mode']: r for r in results if r['case'] == name and r['scope'] == 'broad'}
    cases[name] = {'fixture': f, 'packed_collection': p, 'collection_publication': publication, 'search': search}
    models = {}
    for kind in ('existing_collection', 'repacked_collection', 'no_collection_gzip'):
        totals = {}
        for variant in ('compact_tuple', 'full_direct', 'compact_direct_control'):
            compact = variant.startswith('compact')
            tuple_ref = variant == 'compact_tuple'
            index = f['compact_idx' if compact else 'full_idx']
            tantivy = search['tuple24' if tuple_ref else 'direct']['bytes']
            if kind == 'existing_collection':
                population = f['shared_raw_pac_bytes']
                collection = f['collection_bytes'][0]
            elif kind == 'repacked_collection':
                population = f['shared_raw_pac_bytes']
                # Control retains tuple collection refs while Tantivy uses direct rows.
                width = '7' if compact else '4'
                collection = p[width]['leaf_bytes'] + p[width]['packed_fence_bytes']
            else:
                population = f['shared_gzip_pac_bytes']
                collection = 0
            totals[variant] = index + tantivy + population + collection
        models[kind] = totals
    cases[name]['compared_component_totals'] = models
latency = {}
for line in (out / 'latency.txt').read_text().splitlines():
    m = re.match(r'(BenchmarkAddress\S+)-\d+\s+\d+\s+([\d.]+) ns/op\s+(\d+) B/op\s+(\d+) allocs/op', line)
    if m:
        assert (m[3], m[4]) == ('0', '0'), line
        latency.setdefault(m[1], []).append(float(m[2]))
latency = {k: {'runs_ns': v, 'median_ns': statistics.median(v)} for k, v in latency.items()}
(out / 'summary.json').write_text(json.dumps({'cases': cases, 'latency': latency}, indent=2) + '\n')
for name, case in cases.items():
    print(name)
    for k, v in case['compared_component_totals'].items():
        print(' ', k, {n: round(b / 1e6, 4) for n, b in v.items()})
    a, b = case['search']['tuple24'], case['search']['direct']
    print(' search delta', a['bytes'] - b['bytes'], 'bytes')
