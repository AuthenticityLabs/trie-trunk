"""Summarize native Go API samples. p95 is nearest-rank, not interpolated."""
import csv
import gzip
import json
import math
import statistics
from pathlib import Path

root = Path(__file__).parent
rows = []
paths = {p.name.removesuffix('.gz'): p for p in root.glob('go-*.json.gz')}
paths.update({p.name: p for p in root.glob('go-*.json')})
for name, path in sorted(paths.items()):
    raw = gzip.decompress(path.read_bytes()) if path.suffix == '.gz' else path.read_bytes()
    data = json.loads(raw)
    for scenario in dict.fromkeys(s['Scenario'] for s in data['samples']):
        samples = [s for s in data['samples'] if s['Scenario'] == scenario]
        totals = sorted(s['TotalMS'] for s in samples)
        identity = {s['PageIdentity'] for s in samples}
        assert len(identity) == 1
        row = dict(run=Path(name).stem, scenario=scenario, origin=data['origin'],
                   http=data['protocol'], concurrency=data['parallel'], overlap=data['overlap'],
                   trials=len(samples), rows=samples[0]['Rows'],
                   median_ms=statistics.median(totals), p95_ms=totals[math.ceil(.95 * len(totals)) - 1],
                   page_identity=next(iter(identity)))
        for key in ['SearchMS', 'SummariesMS', 'PacksMS', 'KnownMetadataMS']:
            row[key] = statistics.median(s[key] for s in samples)
        first = [r for r in samples[0]['Requests'] if r['Stage'] != 'known-metadata']
        assert all(r['Protocol'].startswith(f"HTTP/{data['protocol']}.") for s in samples for r in s['Requests'])
        row.update(requests=len(first), summary_packets=samples[0]['SummaryPackets'],
                   seed_packs=samples[0]['SeedPacks'],
                   wire_body_bytes=statistics.median(sum(r['WireBodyBytes'] for r in s['Requests'] if r['Stage'] != 'known-metadata') for s in samples))
        rows.append(row)
    # Preserve raw measurements efficiently, without discarding them.
    if path.suffix != '.gz':
        with gzip.GzipFile(str(path) + '.gz', 'wb', mtime=0) as target:
            target.write(raw)

full = [r for r in rows if r['scenario'] == 'all-full']
assert len({r['page_identity'] for r in full}) == 1, 'Scheduling runs must return the same page'
(root / 'summary.json').write_text(json.dumps(rows, indent=2) + '\n')
with (root / 'summary.csv').open('w') as out:
    writer = csv.DictWriter(out, fieldnames=rows[0].keys())
    writer.writeheader()
    writer.writerows(rows)
for r in rows:
    print(f"{r['run']:22} {r['scenario']:18} median {r['median_ms']:8.1f} ms p95 {r['p95_ms']:8.1f} ms {r['requests']:3} requests")
