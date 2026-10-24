// Run after TestDemoFootprint: node summarize.mjs <50k-or-250k-directory>.
import { readFileSync, writeFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { gzipSync, brotliCompressSync, constants } from 'node:zlib';

const dir = process.argv[2];
if (!dir) throw new Error('A footprint directory is required');
const manifest = JSON.parse(readFileSync(join(dir, 'footprint.json')));
const results = {};
for (const name of ['expanded', 'payload']) {
  const raw = readFileSync(join(dir, `${name}.jsonl`));
  const gz = gzipSync(raw, { level: 6 });
  const br = brotliCompressSync(raw, { params: { [constants.BROTLI_PARAM_QUALITY]: 6 } });
  writeFileSync(join(dir, `${name}.jsonl.gz`), gz);
  writeFileSync(join(dir, `${name}.jsonl.br`), br);
  results[name] = { raw_bytes: raw.length, allocated_bytes: statSync(join(dir, `${name}.jsonl`)).blocks * 512, whole_file_gzip6_bytes: gz.length, whole_file_brotli6_bytes: br.length };
}
const g = manifest.by_component;
results.trie = {
  all_files_bytes: manifest.trie_trunk_bytes,
  excluding_tantivy_bytes: manifest.trie_trunk_bytes - (g.tantivy || 0),
  pac_payload_bytes: g['.pods'],
  allocated_bytes: manifest.trie_trunk_allocated_bytes,
};
results.notes = ['Compressed JSONL is an archive reference, without indexes, summaries, or independently retrievable hours. It uses the exact exported events, ordered by resource/branch/time.'];
writeFileSync(join(dir, 'comparison.json'), JSON.stringify(results, null, 2) + '\n');
console.log(JSON.stringify(results, null, 2));
