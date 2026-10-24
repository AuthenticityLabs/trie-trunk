"""Refresh the footprint section after both opt-in measurements finish."""
import json
import pathlib
import re

root = pathlib.Path(__file__).resolve().parent
fixtures = [json.loads((root / size / "footprint.json").read_text()) for size in ("50k", "250k")]
archives = [json.loads((root / size / "comparison.json").read_text()) for size in ("50k", "250k")]

def mb(value):
    return f"{value / 1_000_000:.3f} MB"

rows = [
    ("Expanded JSONL", lambda f: f["expanded_jsonl_bytes"]),
    ("Payload-only JSONL", lambda f: f["payload_only_jsonl_bytes"]),
    ("PAC payloads (`.pods`) only", lambda f: f["by_component"][".pods"]),
    ("All trie-trunk files excluding Tantivy", lambda f: f["trie_trunk_bytes"] - f["by_component"]["tantivy"]),
    ("**All trie-trunk files including Tantivy**", lambda f: f["trie_trunk_bytes"]),
    ("Filesystem allocated bytes, all files", lambda f: f["trie_trunk_allocated_bytes"]),
]
text = "| Representation | 50,000 Events | 250,000 Events |\n|---|---:|---:|\n"
for name, value in rows:
    text += f"| {name} | " + " | ".join(mb(value(f)) for f in fixtures) + " |\n"
text += "| Expanded JSONL filesystem allocated bytes | " + " | ".join(mb(a['expanded']['allocated_bytes']) for a in archives) + " |\n"
text += "\n"
for f in fixtures:
    total, expanded, payload = f["trie_trunk_bytes"], f["expanded_jsonl_bytes"], f["payload_only_jsonl_bytes"]
    core = total - f["by_component"]["tantivy"]
    text += (f"- **{f['events']:,} events:** all files are **{(1-total/expanded)*100:.1f}% smaller** than expanded JSONL. "
             f"Excluding Tantivy's files, the reduction is **{(1-core/expanded)*100:.1f}%**. "
             f"All files cost **{total/f['events']:.1f} bytes/event**, versus {expanded/f['events']:.1f} for expanded JSONL. "
             f"Against payload-only JSONL, all files are **{abs(total/payload-1)*100:.1f}% {'larger' if total>payload else 'smaller'}**.\n")
text += "\nFile-by-file component totals:\n\n| Component | 50,000 Events | 250,000 Events |\n|---|---:|---:|\n"
components = [
    ("PAC payloads", [".pods"]),
    ("Branch event indexes", [".idx"]),
    ("Time/resource/registry tries", [".tri"]),
    ("Recovery commit journals", [".journal"]),
    ("Summaries", [".sum"]),
    ("Optional collection index + state", ["collection.idx", "collection.state"]),
    ("Tantivy", ["tantivy"]),
]
used = {k for _, keys in components for k in keys}
for name, keys in components:
    text += f"| {name} | " + " | ".join(mb(sum(f['by_component'].get(k, 0) for k in keys)) for f in fixtures) + " |\n"
text += "| Maps, stats, seed headers, config, locks | " + " | ".join(mb(sum(v for k,v in f['by_component'].items() if k not in used)) for f in fixtures) + " |\n"
text += "\nWhole-file archive references for the exact expanded JSONL:\n\n| Encoding | 50,000 Events | 250,000 Events |\n|---|---:|---:|\n"
for name, key in [("gzip level 6", "whole_file_gzip6_bytes"), ("Brotli quality 6", "whole_file_brotli6_bytes")]:
    text += f"| {name} | " + " | ".join(mb(a['expanded'][key]) for a in archives) + " |\n"
text += "\nTantivy-excluded totals are file-accounting subtotals, not a separately measured search-disabled build.\n"

report = root / "REPORT.md"
source = report.read_text()
block = "<!-- FOOTPRINT_TABLES -->\n\n" + text + "\n<!-- END_FOOTPRINT_TABLES -->"
source = re.sub(r"<!-- FOOTPRINT_TABLES -->(?:.*?<!-- END_FOOTPRINT_TABLES -->)?", lambda _: block, source, count=1, flags=re.S)
report.write_text(source)
