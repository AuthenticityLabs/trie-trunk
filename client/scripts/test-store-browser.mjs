// Run against the bundled test worker (see rust/browser/README.md).
import assert from "node:assert/strict";
import { readFile, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { build } from "vite";
import { fileURLToPath } from "node:url";
import { fixture, bytes } from "../services/pil-fixtures.mjs";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE ?? "playwright-core"
);
const root = fileURLToPath(new URL("../../", import.meta.url));
const output = await mkdtemp(join(tmpdir(), "snoot-index-test-"));
await build({
  root: root + "client",
  configFile: false,
  logLevel: "warn",
  build: {
    outDir: output,
    emptyOutDir: true,
    rolldownOptions: {
      input: root + "client/services/store/browser-harness.ts",
      output: { entryFileNames: "harness.js" },
    },
  },
});
const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH,
  headless: true,
});
const context = await browser.newContext();
const origin = "http://localhost:9471";
await context.route(origin + "/**", async (route) => {
  const path = new URL(route.request().url()).pathname;
  if (path === "/")
    return route.fulfill({
      contentType: "text/html",
      body: "<!doctype html><title>Event index tests</title>",
    });
  const name = path === "/harness.js" ? "harness.js" : path.slice(1);
  try {
    await route.fulfill({
      contentType: name.endsWith(".wasm")
        ? "application/wasm"
        : "text/javascript",
      body: await readFile(output + "/" + name),
    });
  } catch {
    await route.abort();
  }
});
const page = await context.newPage();
let sequence = 0;
async function raw(kind, args = {}, worker = 0) {
  return page.evaluate(
    ({ id, kind, args, worker }) =>
      new Promise((resolve, reject) => {
        const w = globalThis.workers[worker];
        const receive = ({ data }) => {
          if (data.id !== id) return;
          w.removeEventListener("message", receive);
          data.error ? reject(Error(data.error)) : resolve(data.result);
        };
        w.addEventListener("message", receive);
        w.postMessage({ id, kind, ...args });
      }),
    { id: ++sequence, kind, args, worker },
  );
}
// Playwright serialization uses JSON, so convert fixture arrays in the browser.
async function cmd(command, namespace = "test", worker = 0) {
  return page.evaluate(
    ({ id, command, namespace, worker }) =>
      new Promise((resolve, reject) => {
        if (command.batch)
          command.batch = {
            bytes: Uint8Array.from(command.batch.bytes).buffer,
            rows: Uint32Array.from(command.batch.rows),
          };
        if (command.definitions)
          command.definitions = command.definitions.map((d) => ({
            ...d,
            store: Uint8Array.from(d.store),
            pil: Uint8Array.from(d.pil),
          }));
        const w = globalThis.workers[worker];
        const receive = ({ data }) => {
          if (data.id !== id) return;
          w.removeEventListener("message", receive);
          if (data.error) return reject(Error(data.error));
          const r = data.result;
          if (r.batch)
            r.batch = {
              bytes: Array.from(new Uint8Array(r.batch.bytes)),
              rows: Array.from(r.batch.rows),
            };
          resolve(r);
        };
        w.addEventListener("message", receive);
        w.postMessage({ id, namespace, command });
      }),
    { id: ++sequence, command, namespace, worker },
  );
}
const f = fixture(),
  millis = Number(
    BigInt("0x" + Buffer.from(f.uuid.subarray(0, 6)).toString("hex")),
  );
const selection = {
  tap: "test",
  from: new Date(millis - 1000).toISOString(),
  to: new Date(millis + 1000).toISOString(),
  where: [],
  limit: null,
  tail: false,
  follow: false,
  pretty: false,
};
const collection = (id, s = selection) => ({
  id,
  created: Date.now(),
  selection: s,
  parameters: {},
  range: "custom",
  from: s.from,
  to: s.to,
});
const row = (i, billing = false) => {
  const r = f.row();
  r[18] = i >> 8;
  r[19] = i & 255;
  if (billing) r[3] = 1;
  return r;
};
const save = (id, frames, fixture = f, tap = "test") => ({
  kind: "save",
  collection: id,
  definitions: [
    {
      digest: fixture.digest,
      store: [...fixture.store],
      pil: [...fixture.pil],
    },
  ],
  bindings: [{ stream: 0, digest: fixture.digest, tap }],
  batch: {
    bytes: [...bytes(...frames)],
    rows: frames.flatMap((r, i) => [
      0,
      frames.slice(0, i).reduce((n, b) => n + b.length, 0),
      frames.slice(0, i + 1).reduce((n, b) => n + b.length, 0),
    ]),
  },
});
async function drain(open, namespace = "test") {
  const rows = [];
  for (;;) {
    const n = await cmd({ kind: "next", cursor: open.cursor }, namespace);
    for (let i = 0; i < n.batch.rows.length; i += 3)
      rows.push(n.batch.bytes.slice(n.batch.rows[i + 1], n.batch.rows[i + 2]));
    if (n.done) break;
  }
  return rows;
}
function nativeFiles(inv) {
  return Object.fromEntries(
    Object.entries(inv).filter(
      ([n]) =>
        n.startsWith("/trie-trunk-v2/test/") && /log\.(tri|pop)$/.test(n),
    ),
  );
}
try {
  await page.goto(origin);
  await page.evaluate(() => {
    globalThis.workers = [
      new Worker("/harness.js", { type: "module" }),
      new Worker("/harness.js", { type: "module" }),
    ];
  });
  await cmd({ kind: "begin", collection: collection("first") });
  const initial = Array.from({ length: 1200 }, (_, i) => row(1200 - i));
  await cmd(save("first", initial.slice(0, 600)));
  await cmd(save("first", initial.slice(600)));
  const inv = await raw("inventory");
  assert.equal(
    Object.keys(inv).some((k) => k.endsWith("log.idx")),
    false,
  );
  const before = nativeFiles(inv);
  await cmd(save("first", [row(1), row(1), row(600), row(1200)]));
  assert.deepEqual(nativeFiles(await raw("inventory")), before);
  await raw("reload");
  const loaded = await drain(
    await cmd({ kind: "restore", collection: "first" }),
  );
  assert.equal(loaded.length, 1200);
  assert.deepEqual(
    loaded.map((b) => (b[18] << 8) | b[19]),
    Array.from({ length: 1200 }, (_, i) => i + 1),
  );
  assert.deepEqual(loaded[599], [...row(600)]);
  await cmd({
    kind: "finish",
    collection: "first",
    selection,
    started: millis + 3_600_000,
  });
  await cmd({ kind: "begin", collection: collection("repeat") });
  const cached = await cmd({
    kind: "lookup",
    collection: "repeat",
    selection,
    started: millis + 3_600_000,
  });
  assert.deepEqual(cached.gaps, []);
  assert.equal((await drain(cached)).length, 1200);
  const wider = {
    ...selection,
    from: new Date(millis - 2000).toISOString(),
    to: new Date(millis + 2000).toISOString(),
  };
  const expanded = await cmd({
    kind: "lookup",
    collection: "repeat",
    selection: wider,
    started: millis + 3_600_000,
  });
  assert.deepEqual(expanded.gaps, [
    { from: millis - 2000, to: millis - 1000 },
    { from: millis + 1000, to: millis + 2000 },
  ]);
  await cmd({ kind: "release", cursor: expanded.cursor });
  const recent = await cmd({
    kind: "lookup",
    collection: "repeat",
    selection,
    started: millis + 500,
  });
  assert.deepEqual(recent.gaps, [{ from: millis - 1000, to: millis + 1000 }]);
  await cmd({ kind: "release", cursor: recent.cursor });
  const filtered = { ...selection, where: ['service=="billing"'] };
  await cmd({ kind: "begin", collection: collection("filtered", filtered) });
  const narrow = await cmd({
    kind: "lookup",
    collection: "filtered",
    selection: filtered,
    started: millis + 3_600_000,
  });
  assert.deepEqual(narrow.gaps, [{ from: millis - 1000, to: millis + 1000 }]);
  assert.equal((await drain(narrow)).length, 0);
  await raw("fail-publication");
  await assert.rejects(
    cmd(save("first", [row(1201, true)])),
    /simulated IDB abort/,
  );
  assert.equal(
    (await raw("inspect", { namespace: "test" })).events.length,
    1200,
  );
  await raw("reload");
  assert.equal(
    (await drain(await cmd({ kind: "restore", collection: "first" }))).length,
    1200,
  );
  assert.deepEqual(nativeFiles(await raw("inventory")), before);
  await Promise.all([
    cmd(save("first", [row(1201, true)]), "test", 0),
    cmd(save("first", [row(1201, true)]), "test", 1),
  ]);
  assert.equal(
    (await raw("inspect", { namespace: "test" })).events.length,
    1201,
  );
  const match = await cmd({
    kind: "lookup",
    collection: "filtered",
    selection: filtered,
    started: millis + 3_600_000,
  });
  assert.deepEqual(await drain(match), [[...row(1201, true)]]);
  // UUID uniqueness spans schema versions but is scoped by tap and credential.
  await cmd(save("first", [row(1)], fixture(43)));
  assert.equal(
    (await raw("inspect", { namespace: "test" })).events.length,
    1201,
  );
  await cmd(save("first", [row(1)], f, "other"));
  assert.equal(
    (await raw("inspect", { namespace: "test" })).events.length,
    1202,
  );
  await cmd(
    { kind: "begin", collection: collection("different") },
    "different",
  );
  await cmd(save("different", [row(1)]), "different");
  assert.equal(
    (await raw("inspect", { namespace: "different" })).events.length,
    1,
  );
  assert.deepEqual(await raw("abort-transaction", { namespace: "test" }), {
    aborted: true,
  });
  // Cross midnight, with two different UUID suffixes in the same millisecond.
  const midnight = Math.floor(millis / 86400000) * 86400000;
  const timed = (suffix, time) => {
    const b = row(suffix);
    for (let i = 9; i >= 4; i--) {
      b[i] = time % 256;
      time = Math.floor(time / 256);
    }
    return b;
  };
  const dates = [
    timed(5, midnight + 86400001),
    timed(3, midnight),
    timed(1, midnight - 1),
    timed(4, midnight + 1),
    timed(2, midnight),
  ];
  const wide = {
    ...selection,
    from: new Date(midnight - 2).toISOString(),
    to: new Date(midnight + 86400002).toISOString(),
  };
  await cmd({ kind: "begin", collection: collection("dates", wide) }, "dates");
  await cmd(save("dates", dates), "dates");
  const exact = {
    ...wide,
    from: new Date(midnight).toISOString(),
    to: new Date(midnight + 1).toISOString(),
  };
  const atMidnight = await cmd(
    {
      kind: "lookup",
      collection: "dates",
      selection: exact,
      started: millis + 864000000,
    },
    "dates",
  );
  assert.deepEqual(
    (await drain(atMidnight, "dates")).map((b) => b[19]),
    [2, 3],
  );
  console.log(
    "PASS: cross-day insertion, same-millisecond UUID distinction, inclusive start/exclusive end",
  );
  const legacyFiles = JSON.parse(
    await readFile(root + "client/services/store/legacy.fixture.json", "utf8"),
  );
  await raw("seed", { files: legacyFiles });
  const legacyBefore = Object.fromEntries(
    Object.entries(await raw("inventory")).filter(([p]) =>
      p.startsWith("/trie-trunk-v1/"),
    ),
  );
  await raw("fail-publication");
  await assert.rejects(
    cmd({ kind: "restore", collection: "legacy" }, "legacy"),
    /simulated IDB abort/,
  );
  const migrated = await cmd(
    { kind: "restore", collection: "legacy" },
    "legacy",
  );
  assert.deepEqual(migrated.coverage, [
    { from: millis - 1000, to: millis + 1000 },
  ]);
  assert.deepEqual(
    (await drain(migrated, "legacy")).map((b) => b[19]),
    [1, 2, 3],
  );
  const legacyAfter = Object.fromEntries(
    Object.entries(await raw("inventory")).filter(([p]) =>
      p.startsWith("/trie-trunk-v1/"),
    ),
  );
  assert.deepEqual(legacyAfter, legacyBefore);
  assert.equal(
    Object.keys(await raw("inventory")).some(
      (p) => p.startsWith("/trie-trunk-v2/") && p.endsWith("log.idx"),
    ),
    false,
  );
  console.log(
    "PASS: restartable legacy migration, sparse index runs, orphan tails ignored, old cache retained intact",
  );
  console.log(
    "PASS: 1,200 out-of-order UUIDs, exact PIL restore, overlapping dedupe, gap planning, recent-window refresh, trie equality, IDB rollback/file recovery, concurrent writers, schema/tap/credential isolation, and no browser log.idx",
  );
} finally {
  await browser.close();
  await rm(output, { recursive: true, force: true });
}
