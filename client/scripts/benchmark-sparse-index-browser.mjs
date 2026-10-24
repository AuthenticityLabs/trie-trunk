import {readFile, writeFile, mkdir, mkdtemp, rm, readdir, stat} from "node:fs/promises";
import {join, resolve} from "node:path";
import {tmpdir} from "node:os";
const {chromium} = await import(process.env.PLAYWRIGHT_MODULE ?? "playwright-core");
const output = resolve(process.argv[2] ?? "../benchmarks/storage/2026-10-20-sparse-index");
await mkdir(output, {recursive: true});
const workerSource = await readFile(new URL("./benchmark-sparse-index-worker.mjs", import.meta.url), "utf8");
let profile;
const origin = "http://localhost:9479";
let context;
async function diskFiles(path) {
  const result = [];
  for (const entry of await readdir(path, {withFileTypes: true})) {
    const name = join(path, entry.name);
    if (entry.isDirectory()) result.push(...await diskFiles(name));
    else if (entry.isFile()) {
      const s = await stat(name);
      result.push({path: name.slice(profile.length + 1), bytes: s.size, allocatedBytes: s.blocks * 512});
    }
  }
  return result;
}
async function launch() {
  context = await chromium.launchPersistentContext(profile, {
    executablePath: process.env.CHROME_PATH, headless: true
  });
  await context.route(origin + "/**", route => route.fulfill({
    contentType: route.request().url().endsWith("worker.mjs") ? "text/javascript" : "text/html",
    body: route.request().url().endsWith("worker.mjs") ? workerSource : "<!doctype html><title>Isolated sparse index test</title>"
  }));
  const page = await context.newPage();
  await page.goto(origin);
  await page.evaluate(() => { globalThis.worker = new Worker("/worker.mjs", {type: "module"}); });
  return page;
}
async function call(page, args) {
  return page.evaluate(args => new Promise((resolve, reject) => {
    worker.onmessage = ({data}) => data.error ? reject(Error(data.error)) : resolve(data.result);
    worker.onerror = e => reject(Error(e.message));
    worker.postMessage(args);
  }), args);
}
const results = [];
try {
  let browser;
  for (const total of [1_000_000, 10_000_000]) for (let round = 0; round < 3; round++) {
    for (const mode of round % 2 ? ["padded", "sparse"] : ["sparse", "padded"]) {
      profile = await mkdtemp(join(tmpdir(), "trie-sparse-browser-"));
      let page = await launch();
      browser = await context.browser().version();
      const name = `${mode}-${total}-${round}`;
      const measurement = await call(page, {kind: "prepare", total, mode, name});
      // Close the whole browser: verify persistence across a real restart.
      await context.close(); context = undefined;
      const files = await diskFiles(profile);
      const indexFiles = files.filter(f => f.bytes === total * 26 && f.path.includes("File System"));
      if (indexFiles.length !== 1) throw Error(`Cannot identify OPFS index allocation: ${JSON.stringify(indexFiles)}`);
      page = await launch();
      const reopened = await call(page, {kind: "reopen", total, name});
      const result = {...measurement, round, disk: indexFiles[0], restartRecordsVerified: reopened.recordsVerified,
        restartQuotaUsage: reopened.quotaUsage, restartQuotaBeforeOpen: reopened.quotaBeforeOpen,
        restartQuotaWhileOpen: reopened.quotaWhileOpen};
      results.push(result);
      console.log(JSON.stringify(result));
      await call(page, {kind: "delete"});
      await context.close(); context = undefined;
      await rm(profile, {recursive: true, force: true}); profile = undefined;
    }
  }
  await writeFile(join(output, "browser.json"), JSON.stringify({
    browser, platform: process.platform, arch: process.arch, date: new Date().toISOString(),
    description: "Isolated OPFS fixed-position index prototype; deterministic byte payloads, not PIL codec benchmark.",
    results
  }, null, 2) + "\n");
} finally {
  await context?.close();
  if (profile) await rm(profile, {recursive: true, force: true});
}
