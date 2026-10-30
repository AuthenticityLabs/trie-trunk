import { readFileSync, writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const [root, fixture, report] = process.argv.slice(2);
if (!root || !fixture || !report) throw Error('Usage: node --experimental-strip-types client/scripts/benchmark-planner.mjs <checkout> <fixture.pil> <report.json>');
const load = p => import(pathToFileURL(root + '/client/services/' + p));
const {
    Codec
  } = await load('pil/wasm.ts'),
  {
    PILStream
  } = await load('pil/stream.ts'),
  {
    NativeStore
  } = await load('store/native.ts'),
  {
    Backend
  } = await load('native-backend-fixture.mjs'),
  {
    ViewCache
  } = await load('pil/view.ts');
const meta = JSON.parse(readFileSync(fixture + '.json')),
  wire = readFileSync(fixture),
  definitions = new Map(),
  backend = new Backend(),
  store = new NativeStore(backend),
  session = store.begin(await store.serial());
let reads = 0,
  readBytes = 0,
  commands = 0;
const file = backend.file.bind(backend);
backend.file = async (...args) => {
  const f = await file(...args);
  if (!f.measured) {
    const read = f.read.bind(f);
    f.read = (b, o) => {
      reads++;
      readBytes += b.byteLength;
      return read(b, o);
    };
    f.measured = true;
  }
  return f;
};
const codec = new Codec((await WebAssembly.instantiate(readFileSync(root + '/client/assets/pil.wasm'))).instance);
const start = performance.now(),
  parser = new PILStream(codec, definitions, () => {}, () => {}, async () => {
    throw Error('Unexpected expanded event');
  }, r => store.record(session, r, r.kind === 'manifest' ? definitions.get(r.binding.digest) : undefined));
try {
  for (let i = 0; i < wire.length; i += 65536) await parser.feed(wire.subarray(i, i + 65536));
  parser.finish();
  await store.finish(session);
} finally {
  parser.close();
}
const importMs = performance.now() - start,
  sources = await backend.list(),
  trunk = {
    version: 'benchmark',
    catalog: {
      files: sources.flatMap(s => ['tri', 'pods'].map(kind => ({
        kind,
        path: s.tap.slice(0, s.tap.lastIndexOf('/')) + '/0/' + s.tap.split('/').at(-1) + '.' + kind,
        definition: s.digest,
        bootstrap: false
      })))
    },
    bindings: sources.map((s, i) => ({
      tap: s.tap,
      digest: s.digest,
      stream: i + 1
    })),
    definitions: [...definitions.values()]
  };
const archive = async (_token, c) => {
  commands++;
  switch (c.kind) {
    case 'time-read-many':return {times:await store.timeWindows(c.queries)};
   case 'time-read':
      return {
        time: await store.timeWindow(c.query)
      };
    case 'native-resolve':
      return {
        nativeDependencies: await store.resolveHour(c)
      };
    case 'native-page':
      return {
        nativePage: await store.page(c.source, c.from, c.to, c.after, c.limit, c.descending)
      };
    default:
      throw Error(c.kind);
  }
};
const cache = new ViewCache(async () => {
  throw Error('Warm search must not fetch');
});
cache.setTrunk(trunk);
const query = {
  tap: meta.tap,
  from: meta.from,
  to: meta.to,
  size: 100,
  query: {
    filters: [],
    order: 'asc'
  }
};
const run = async q => {
  reads = readBytes = commands = 0;
  let first;
  const start = performance.now();
  const result = await cache.query(q, '', new AbortController().signal, codec, definitions, archive, false, undefined, () => first ??= performance.now() - start);
  return {
    result,
    measurement: {
      ms: performance.now() - start,
      firstPageMs: first,
      reads,
      readBytes,
      commands,
      matched: result.result.summary?.count,
      loaded: result.result.page.length
    }
  };
};
const scans = [],
  pages = [];
for (let i = 0; i < 3; i++) {
  cache.resetEvents();
  const scan = await run(query);
  scans.push(scan.measurement);
  const page = await run({
    ...query,
    after: scan.result.result.next
  });
  pages.push(page.measurement);
}
const output = {
  events: meta.events,
  hours: meta.hours,
  wireBytes: wire.length,
  importMs,
  scope: 'Production ViewCache, WASM decoder, NativeStore; memory-backed file I/O counters. Excludes HTTP, physical disk, DOM.',
  scans,
  pages
};
writeFileSync(report, JSON.stringify(output, null, 2) + '\n');
console.log(JSON.stringify(output));
