// Regular installed Safari, not an ephemeral safaridriver automation session.
// Open the printed URL. Phase 0 prepares data; subsequent visits verify it.
// Caller must record whether each visit follows reload or actual Safari restart.
import {createServer} from 'node:http';
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {resolve, join} from 'node:path';
const out = resolve(process.argv[2] ?? 'benchmarks/storage/2026-10-22-opfs-validation');
await mkdir(out, {recursive: true});
const worker = await readFile(new URL('./validate-opfs-sparse-worker.mjs', import.meta.url), 'utf8');
const run = {engine: 'installed-safari', platform: process.platform, arch: process.arch, visits: [], restartEvidence: []};
let phase = 0;
const html = `<!doctype html><meta charset="utf-8"><title>Safari OPFS Validation</title>
<h1>Safari OPFS Validation</h1><pre id="status">Running…</pre>
<script type="module">
const status = document.querySelector('#status');
const phase = await fetch('/phase').then(r => r.json());
const modes = phase >= 3 ? [] : ['seek', 'truncate', 'zero-control', 'keyed-pages'];
const results = [];
async function call(mode, action) {
 const worker = new Worker('/probe.mjs', {type: 'module'});
 try { return await new Promise((resolve, reject) => {
  const timer = setTimeout(() => {worker.terminate(); reject(Error('Worker timeout'));}, 30000);
  worker.onerror = e => {clearTimeout(timer); reject(Error(e.message));};
  worker.onmessage = e => {clearTimeout(timer); e.data.error ? reject(Error(JSON.stringify(e.data.error))) : resolve(e.data.result);};
  worker.postMessage({mode, action, isolateModes: true});
 }); } finally {worker.terminate();}
}
try {
 for (const mode of modes) {
  const actions = phase === 0 ? ['prepare'] : phase === 1 ? ['verify', 'fill'] : ['verify-filled', 'delete'];
  for (const action of actions) {
   status.textContent = mode + ': ' + action;
   results.push({mode, action, result: await call(mode, action)});
  }
 }
 if (phase < 3) await fetch('/result', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({phase, userAgent: navigator.userAgent, results})});
 status.textContent = phase < 2 ? 'Phase ' + phase + ' Passed. Quit and reopen Safari, then revisit this exact URL to continue. Do not use Reload as a substitute for a restart.' : 'All Phases Passed. Test files deleted.';
} catch (e) {
 status.textContent = e.stack;
 await fetch('/result', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({phase, userAgent: navigator.userAgent, results, error: e.stack})});
}
</script>`;
const server = createServer(async (req, res) => {
  try {
    res.setHeader('Cache-Control', 'no-store');
    if (req.url === '/phase') {res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(phase));}
    else if (req.url === '/probe.mjs') {res.setHeader('Content-Type', 'text/javascript'); res.end(worker);}
    else if (req.url === '/result' && req.method === 'POST') {
      let text = ''; for await (const part of req) {text += part; if (text.length > 100000) throw Error('Oversize result');}
      const result = JSON.parse(text); run.visits.push({...result, receivedAt: new Date().toISOString()});
      if (!result.error && result.phase === phase) phase = Math.min(3, phase + 1);
      await writeFile(join(out, 'safari.json'), JSON.stringify(run, null, 2) + '\n');
      console.log(JSON.stringify(result)); res.end('saved');
    } else if (req.url === '/') {res.setHeader('Content-Type', 'text/html'); res.end(html);}
    else {res.writeHead(404); res.end();}
  } catch (e) {res.writeHead(500); res.end(String(e)); console.error(e);}
});
server.listen(9485, '127.0.0.1', () => console.log('Open http://127.0.0.1:9485/ in installed Safari.'));
