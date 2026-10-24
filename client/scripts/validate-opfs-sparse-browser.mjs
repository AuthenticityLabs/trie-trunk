import {readFile, writeFile, mkdir, mkdtemp, rm, readdir, stat} from 'node:fs/promises';
import {join, resolve} from 'node:path';
import {tmpdir} from 'node:os';
const pw = await import(process.env.PLAYWRIGHT_MODULE ?? 'playwright-core');
const engine = process.env.OPFS_BROWSER ?? 'chromium';
const quota = Number(process.env.OPFS_QUOTA || 0);
const out = resolve(process.argv[2] ?? 'benchmarks/storage/2026-10-22-opfs-validation');
const worker = await readFile(new URL('./validate-opfs-sparse-worker.mjs', import.meta.url), 'utf8');
const origin = 'http://localhost:9483';
const results = []; let context, profile, version;
async function launch() {
  context = await pw[engine].launchPersistentContext(profile, {headless: true,
    ...(engine === 'chromium' ? {executablePath: process.env.CHROME_PATH} : {})});
  version = context.browser().version();
  await context.route(origin + '/**', route => route.fulfill({
    contentType: route.request().url().endsWith('.mjs') ? 'text/javascript' : 'text/html',
    body: route.request().url().endsWith('.mjs') ? worker : '<!doctype html><title>OPFS Sparse Validation</title>'}));
  const page = await context.newPage();
  if (quota) {
    const cdp = await context.newCDPSession(page);
    await cdp.send('Storage.overrideQuotaForOrigin', {origin, quotaSize: quota});
  }
  await page.goto(origin);
  await page.evaluate(() => { globalThis.probe = new Worker('/probe.mjs', {type: 'module'}); });
  return page;
}
async function call(page, mode, action) {
  return page.evaluate(({mode, action}) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(Error('Worker timeout')), 30000);
    probe.onerror = e => { clearTimeout(timer); reject(Error(e.message)); };
    probe.onmessage = e => { clearTimeout(timer); resolve(e.data); };
    probe.postMessage({mode, action});
  }), {mode, action});
}
async function disk(dir) {
  const found = [];
  for (const e of await readdir(dir, {withFileTypes: true})) {
    const path = join(dir, e.name);
    if (e.isDirectory()) found.push(...await disk(path));
    else if (e.isFile()) {
      const s = await stat(path);
      if (s.size === 64 * 1024 * 1024) found.push({path: path.slice(profile.length + 1), logical: s.size, allocated: s.blocks * 512});
    }
  }
  return found;
}
await mkdir(out, {recursive: true});
try {
  for (const mode of ['seek', 'truncate', 'zero-control', 'keyed-pages']) {
    profile = await mkdtemp(join(tmpdir(), 'trunk-opfs-validation-'));
    let page = await launch();
    const result = {mode, prepare: await call(page, mode, 'prepare')};
    results.push(result);
    if (result.prepare.error) {
      if (!quota || result.prepare.error.name !== 'QuotaExceededError') throw Error(JSON.stringify(result.prepare.error));
    } else {
      await context.close(); context = undefined;
      result.backingFiles = await disk(profile);
      page = await launch();
      for (const action of ['verify', 'fill']) {
        result[action] = await call(page, mode, action);
        if (result[action].error) throw Error(JSON.stringify(result[action].error));
      }
      await context.close(); context = undefined;
      result.backingFilesAfterFill = await disk(profile);
      page = await launch();
      result.secondRestart = await call(page, mode, 'verify-filled');
      if (result.secondRestart.error) throw Error(JSON.stringify(result.secondRestart.error));
    }
    await call(page, mode, 'delete');
    await context.close(); context = undefined;
    await rm(profile, {recursive: true, force: true}); profile = undefined;
    console.log(JSON.stringify(result));
  }
} finally {
  await context?.close();
  if (profile) await rm(profile, {recursive: true, force: true});
  await writeFile(join(out, `${engine}${quota ? '-quota' : ''}.json`), JSON.stringify({engine, version, quotaOverride: quota, platform: process.platform, arch: process.arch, results}, null, 2) + '\n');
}
