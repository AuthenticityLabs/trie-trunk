import {openSync, closeSync, writeSync, readSync, ftruncateSync, fsyncSync, fstatSync, statSync, readFileSync, readdirSync} from 'node:fs';
import {mkdtemp, rm, mkdir, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';
import {cases, operate, verify} from './probe-revision-pages-core.mjs';
const engine = process.env.REVISION_BROWSER ?? 'native';
const filter = process.env.REVISION_CASE;
const out = resolve(process.argv[2] ?? 'benchmarks/storage/2026-10-23-revision-pages');
await mkdir(out, {recursive: true});
const results = []; let context, profile, version, pw;
const origin = 'http://localhost:9487';
const actions = ['prepare', 'fill', 'clear-records', 'zero-pages', 'truncate-half'];
function inspect(path) { const s = statSync(path); return {logical: s.size, allocated: s.blocks * 512}; }
function findBacking(dir, id) {
  for (const e of readdirSync(dir, {withFileTypes: true})) {
    const p = join(dir, e.name);
    if (e.isDirectory()) { const r = findBacking(p, id); if (r) return r; }
    else if (e.isFile() && statSync(p).size >= 65536) {
      const fd = openSync(p, 'r'), b = Buffer.alloc(8);
      try { readSync(fd, b, 0, 8, 0); } finally { closeSync(fd); }
      if (b.toString('ascii', 0, 4) === 'RVP0' && b.readUInt32LE(4) === id) return p;
    }
  }
}
async function launch() {
  context = await pw[engine].launchPersistentContext(profile, {headless: true,
    ...(engine === 'chromium' ? {executablePath: process.env.CHROME_PATH ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'} : {})});
  version = context.browser().version();
  await context.route(origin + '/**', route => {
    const name = new URL(route.request().url()).pathname.slice(1);
    return route.fulfill({contentType: name.endsWith('.mjs') ? 'text/javascript' : 'text/html',
      body: name.endsWith('.mjs') ? readFileSync(new URL('./' + name, import.meta.url), 'utf8') : '<!doctype html><title>Revision Page Allocation Probe</title>'});
  });
  const page = await context.newPage(); await page.goto(origin);
  await page.evaluate(() => { globalThis.probe = new Worker('/probe-revision-pages-worker.mjs', {type: 'module'}); });
  return page;
}
async function call(page, c, action, stage) {
  return page.evaluate(({c, action, stage}) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(Error('Probe timeout')), 120000);
    probe.onerror = e => {clearTimeout(timer); reject(Error(e.message));};
    probe.onmessage = ({data}) => {clearTimeout(timer); data.error ? reject(Error(data.error)) : resolve(data);};
    probe.postMessage({c, action, stage});
  }), {c, action, stage});
}
try {
  if (engine !== 'native') pw = await import(process.env.PLAYWRIGHT_MODULE ?? 'playwright-core');
  for (const c of cases) {
    if (filter && !c.name.includes(filter)) continue;
    profile = await mkdtemp(join(tmpdir(), 'trunk-revision-pages-'));
    const result = {case: c, stages: []}; results.push(result);
    let backing;
    for (const action of c.packed ? ['prepare'] : actions) {
      let measurement, checked;
      if (engine === 'native') {
        backing = join(profile, 'probe.idx');
        let fd = openSync(backing, action === 'prepare' ? 'w+' : 'r+');
        const file = {write: (a, at) => writeSync(fd, a, 0, a.length, at), read: (a, at) => readSync(fd, a, 0, a.length, at),
          truncate: n => ftruncateSync(fd, n), flush: () => fsyncSync(fd), size: () => fstatSync(fd).size};
        try {measurement = operate(file, c, action);} finally {closeSync(fd);}
        if (action !== 'truncate-half') {
          fd = openSync(backing, 'r'); try {checked = verify(file, c, action);} finally {closeSync(fd);}
        }
      } else {
        let page = await launch(); measurement = await call(page, c, action);
        await context.close(); context = undefined;
        if (action === 'prepare') backing = findBacking(profile, c.id);
        if (action !== 'truncate-half') {
          page = await launch(); checked = await call(page, c, 'verify', action);
          await context.close(); context = undefined;
        }
      }
      result.stages.push({action, measurement, checked, physical: backing ? inspect(backing) : null});
    }
    if (engine !== 'native') {const page = await launch(); await call(page, c, 'delete'); await context.close(); context = undefined;}
    await rm(profile, {recursive: true, force: true}); profile = undefined;
    console.log(JSON.stringify({case: c.name, stages: result.stages.map(s => ({action: s.action, physical: s.physical, checked: !!s.checked}))}));
    await writeFile(join(out, engine + (filter ? '-' + filter : '') + '.json'), JSON.stringify({engine, version, platform: process.platform, arch: process.arch, results}, null, 2) + '\n');
  }
} finally {
  await context?.close(); if (profile) await rm(profile, {recursive: true, force: true});
}
