// Run against the bundled test worker (see rust/browser/README.md).
import assert from "node:assert/strict";
import {randomUUID} from "node:crypto";
import { readFile, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { build } from "vite";
import { fileURLToPath } from "node:url";
import { fixture, bytes, cb } from "../services/pil-fixtures.mjs";
import { cbor } from "../services/pil/cbor.ts";
import {pacHeader} from "../services/pil/pac.ts";
import {nativePAC} from "../services/pil/pac-native.ts";
import {Codec} from "../services/pil/wasm.ts";
import {PILStream} from "../services/pil/stream.ts";
const drivers = await import(
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
const engine=process.env.STORE_BROWSER??"chromium";
const context = await drivers[engine].launchPersistentContext(join(output,"profile"),{
  executablePath:engine==="chromium"?process.env.CHROME_PATH:process.env.WEBKIT_PATH,
  headless:true,
});
// WebKit may retain origin storage across temporary profiles. Isolate each run
// without clearing any existing browser data; reloads still use this same origin.
const origin = `https://store-${randomUUID()}.test`;
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
  command=JSON.parse(JSON.stringify(command,(_,v)=>v instanceof Uint8Array?{__bytes:[...v]}:v instanceof Uint32Array?{__u32:[...v]}:v));
  return page.evaluate(
    ({ id, command, namespace, worker }) =>
      new Promise((resolve, reject) => {
        const revive=v=>{
          if(!v||typeof v!=="object")return v;
          if(v.__bytes)return Uint8Array.from(v.__bytes);
          if(v.__u32)return Uint32Array.from(v.__u32);
          if(Array.isArray(v))return v.map(revive);
          return Object.fromEntries(Object.entries(v).map(([k,value])=>[k,revive(value)]));
        };
        command=revive(command);
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
          if(r.nativePage)r.nativePage={...r.nativePage,rows:[...r.nativePage.rows],population:[...r.nativePage.population],offsets:[...r.nativePage.offsets]};
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
try {
  await page.goto(origin);
  await page.evaluate(() => {
    globalThis.workers = [
      new Worker("/harness.js", { type: "module" }),
      new Worker("/harness.js", { type: "module" }),
    ];
  });
  // Native event files and atomic trie publication, with no IndexedDB.
  const nativeWire=new Uint8Array(await readFile(root+"client/services/fixtures/native-ranges/ranges.pil"));
  const pilModule=await WebAssembly.compile(await readFile(root+"client/assets/pil.wasm"));
  const nativeDefinitions=new Map(),nativeRecords=[];
  const nativeParser=new PILStream(new Codec(await WebAssembly.instantiate(pilModule)),new Map(),d=>nativeDefinitions.set(d.digest,d),()=>{},async()=>{},async r=>nativeRecords.push(r));
  await nativeParser.feed(nativeWire);nativeParser.finish();nativeParser.close();
  // Keep the legacy unordered-generation coverage case below explicit.
  for(const record of nativeRecords)if(record.kind==="manifest")delete record.manifest.GenerationSequence;
  const nativeSelection={...selection,tap:nativeRecords[0].binding.tap,from:"2026-10-16T12:12:00Z",to:"2026-10-16T12:28:00Z"};
  await cmd({kind:"begin",collection:collection("native-first",nativeSelection)},"native");
  const nativeRecord=(session,record)=>({kind:"native-record",session,record,definition:nativeDefinitions.get(record.binding.digest)});
  const nativeSizes=inv=>Object.fromEntries(Object.entries(inv).filter(([p])=>p.startsWith("/snoot-store/native/branches/")&&!/\/(layout|branch)\./.test(p)));
  await cmd({kind:"native-begin",session:"interrupted"},"native");
  for(const record of nativeRecords.slice(0,-1))await cmd(nativeRecord("interrupted",record),"native");
  const nativeBefore=nativeSizes(await raw("inventory"));
  assert.ok(Object.keys(nativeBefore).some(p=>p.endsWith("events.idx")));
  await page.evaluate(()=>{workers[0].terminate();workers[0]=new Worker("/harness.js",{type:"module"});});
  assert.deepEqual((await cmd({kind:"native-coverage"},"native")).nativeCoverage,[]);
  await cmd({kind:"native-begin",session:"retry"},"native",1);
  for(const record of nativeRecords)await cmd(nativeRecord("retry",record),"native",1);
  const finish=await cmd({kind:"native-finish",session:"retry",collection:"native-first",selection:nativeSelection},"native",1);
  const source=finish.nativeSources[0];
  assert.equal(finish.nativeSources.length,1);
  assert.deepEqual(nativeSizes(await raw("inventory")),nativeBefore);
  const pageRequest={kind:"native-page",source,from:Date.parse(nativeSelection.from),to:Date.parse(nativeSelection.to)};
  const nativePage=(await cmd(pageRequest,"native")).nativePage;
  assert.equal(nativePage.rows.length,16);assert.equal(nativePage.population.length>0,true);
  await Promise.all([0,1].map(async worker=>{
    const session="concurrent-"+worker;
    await cmd({kind:"native-begin",session},"native",worker);
    for(const record of nativeRecords)await cmd(nativeRecord(session,record),"native",worker);
    await cmd({kind:"native-finish",session,collection:"native-first",selection:nativeSelection},"native",worker);
  }));
  assert.deepEqual(nativeSizes(await raw("inventory")),nativeBefore);
  const rebindPAC=records=>{
    const identity=records.find(r=>r.kind==="manifest").manifest.Generation;
    for(const record of records)if(record.kind==="manifest"&&record.manifest.TimeTrie)record.manifest.TimeTrie.set(identity,48);
    for(const record of records)if(record.kind==="pac"){
      const header=pacHeader(record.bytes,0,64<<20,new Set([nativePAC]));
      header.extensions.find(e=>e.type===nativePAC).bytes.set(identity,0);
    }
  };
  const generationRecords=structuredClone(nativeRecords);generationRecords[0].manifest.Generation[0]^=1;rebindPAC(generationRecords);
  await cmd({kind:"native-begin",session:"metadata-abort"},"native");
  await raw("fail-native-publication");
  await assert.rejects(cmd(nativeRecord("metadata-abort",generationRecords[0]),"native"),/simulated file commit failure/);
  // The original generation remains certified; the failed new one does not.
  assert.equal((await cmd({kind:"native-coverage"},"native")).nativeCoverage.length,1);
  for(const record of generationRecords)await cmd(nativeRecord("metadata-abort",record),"native");
  await cmd({kind:"native-finish",session:"metadata-abort"},"native");
  assert.equal((await cmd({kind:"native-coverage"},"native")).nativeCoverage.length,2);
  await page.reload();
  await page.evaluate(()=>{globalThis.workers=[new Worker("/harness.js",{type:"module"}),new Worker("/harness.js",{type:"module"})];});
  const nativeRestored=await cmd({kind:"restore",collection:"native-first"},"native");
  assert.equal(nativeRestored.done,true,"page working sets are not durable event storage");
  assert.deepEqual((await cmd(pageRequest,"native")).nativePage,nativePage);
  const nativeDB=await raw("inspect",{namespace:"native"});
  assert.ok(nativeDB.sources.length);assert.deepEqual(await page.evaluate(()=>indexedDB.databases()),[]);
  assert.equal((await cmd({kind:"definitions"},"native")).definitions.length,1);
  console.log("PASS: native OPFS ranges + atomic trie commits, killed worker before completion, orphan-tail recovery, concurrent tabs, actual page reload, source-generation separation, exact lazy PIL population, zero IndexedDB databases");

  // Real OPFS pages: disjoint hours, an intervening fill and head retention.
  const importHour=async(name,session=name)=>{
    const records=[],definitions=new Map();
    const parser=new PILStream(new Codec(await WebAssembly.instantiate(pilModule)),new Map(),d=>definitions.set(d.digest,d),()=>{},async()=>{},async r=>records.push(r));
    await parser.feed(await readFile(root+`client/services/fixtures/pac-storage/${name}.pil`));parser.finish();parser.close();
    await cmd({kind:"native-begin",session},"pac-pages");
    for(const record of records)await cmd({kind:"native-record",session,record,definition:definitions.get(record.binding.digest)},"pac-pages");
    return cmd({kind:"native-finish",session},"pac-pages");
  };
  await importHour("gap-2");
  const pageSource=(await importHour("gap-0")).nativeSources[0];
  const sapSize=async()=>Object.entries(await raw("inventory")).find(([path])=>path.includes("/pac-pages/")&&path.endsWith("events.sods"))[1];
  assert.equal(await sapSize(),4*(1<<20)+8);
  await importHour("gap-1");assert.equal(await sapSize(),5*(1<<20)+8);
  await page.reload();await page.evaluate(()=>{globalThis.workers=[new Worker("/harness.js",{type:"module"}),new Worker("/harness.js",{type:"module"})];});
  const pageProbe={kind:"native-page",source:pageSource,from:Date.parse("2026-10-19T14:00:00Z"),to:Date.parse("2026-10-19T15:00:00Z"),limit:1};
  const crossPage=(await cmd(pageProbe,"pac-pages")).nativePage;
  assert.equal(crossPage.rows.length,16);assert.equal(crossPage.population.at(-1),101);
  await importHour("retired");
  assert.deepEqual((await cmd(pageProbe,"pac-pages")).nativePage,crossPage);
  await assert.rejects(importHour("gap-0","stale"),/Stale PAC retirement/);
  await importHour("after-retirement");
  assert.ok(await sapSize()<=5*(1<<20)+8);
  console.log("PASS: hourly PAC gap fills, cross-page records, sparse allocation, reload, head retirement, stale-hour rejection and page reuse",engine);

  const stateWire=new Uint8Array(await readFile(root+"client/services/fixtures/state-pages/ranges.pil"));
  const stateDefinitions=new Map(),stateRecords=[];
  const stateParser=new PILStream(new Codec(await WebAssembly.instantiate(pilModule)),new Map(),d=>stateDefinitions.set(d.digest,d),()=>{},async()=>{},async r=>stateRecords.push(r));
  await stateParser.feed(stateWire);stateParser.finish();stateParser.close();
  const stateQuery={kind:"native-object-state",query:{source:stateRecords[0].binding.tap,id:"00000000-0000-7000-8000-00000000002a"}};
  const stateRecord=(session,record)=>({kind:"native-record",session,record,definition:stateDefinitions.get(record.binding.digest)});
  await cmd({kind:"native-begin",session:"state-interrupted"},"state-pages");
  for(const record of stateRecords.slice(0,-1))await cmd(stateRecord("state-interrupted",record),"state-pages");
  await assert.rejects(cmd(stateQuery,"state-pages"),/No committed/);
  await page.evaluate(()=>{workers[0].terminate();workers[0]=new Worker("/harness.js",{type:"module"});});
  await assert.rejects(cmd(stateQuery,"state-pages"),/No committed/);
  await cmd({kind:"native-begin",session:"state-retry"},"state-pages",1);
  for(const record of stateRecords)await cmd(stateRecord("state-retry",record),"state-pages",1);
  const stateSource=(await cmd({kind:"native-finish",session:"state-retry"},"state-pages",1)).nativeSources[0];
  const observed=(await cmd(stateQuery,"state-pages")).nativeObjectState;
  assert.equal(observed.sequence,1200);assert.equal(observed.token.slice(-16),"2b00000000000000");
  await page.reload();
  await page.evaluate(()=>{globalThis.workers=[new Worker("/harness.js",{type:"module"}),new Worker("/harness.js",{type:"module"})];});
  assert.deepEqual((await cmd(stateQuery,"state-pages")).nativeObjectState,observed);
  const stateCoverage=(await cmd({kind:"native-coverage"},"state-pages")).nativeCoverage;
  assert.equal(stateCoverage.length,1);assert.equal(stateCoverage[0].StateSequence,1200);
  console.log("PASS: paged invalidation snapshots, committed token lookup, killed worker before publication, two-hour snapshot reuse, and exact token restoration after page reload");

  const updatedState=structuredClone(stateRecords);
  for(const record of updatedState){
    if(record.kind==="manifest")record.manifest.StateSequence=1201;
    if(record.kind!=="chunk"||record.file!=="state")continue;
    const value=cbor(record.bytes);value.Sequence=1201;
    for(const entry of value.Objects??[])if(Buffer.from(entry.ID).toString("hex")==="0000000000007000800000000000002a")entry.Token=1201;
    record.bytes=cb(value);record.digest=Buffer.from(await crypto.subtle.digest("SHA-256",record.bytes)).toString("hex");
  }
  await cmd({kind:"native-begin",session:"state-update"},"state-pages");
  for(const record of updatedState)await cmd(stateRecord("state-update",record),"state-pages");
  await cmd({kind:"native-finish",session:"state-update"},"state-pages");
  const updatedToken=(await cmd(stateQuery,"state-pages")).nativeObjectState;
  assert.equal(updatedToken.sequence,1201);assert.equal(updatedToken.token.slice(-16),"b104000000000000");
  await cmd({kind:"native-begin",session:"state-before-compaction"},"state-pages",1);
  for(const record of updatedState)await cmd(stateRecord("state-before-compaction",record),"state-pages",1);
  const stateFiles=inventory=>Object.entries(inventory).filter(([path])=>path.includes("/state-pages/native/"+stateSource+"/collection.state"));
  const bytesBefore=stateFiles(await raw("inventory")).reduce((n,[,size])=>n+size,0);
  const measurements=[];
  for(let i=0;i<5;i++)measurements.push((await raw("compact-native-state",{namespace:"state-pages",source:stateSource})).elapsedMs);
  await assert.rejects(cmd({kind:"native-finish",session:"state-before-compaction"},"state-pages",1),/state compacted/);
  const compactedFiles=stateFiles(await raw("inventory"));assert.equal(compactedFiles.length,1);
  assert.ok(compactedFiles[0][1]<bytesBefore);
  await page.reload();
  await page.evaluate(()=>{globalThis.workers=[new Worker("/harness.js",{type:"module"}),new Worker("/harness.js",{type:"module"})];});
  assert.deepEqual((await cmd(stateQuery,"state-pages")).nativeObjectState,updatedToken);
  await cmd({kind:"native-begin",session:"state-after-compaction"},"state-pages");
  for(const record of updatedState)await cmd(stateRecord("state-after-compaction",record),"state-pages");
  await cmd({kind:"native-finish",session:"state-after-compaction"},"state-pages");
  assert.deepEqual((await cmd(stateQuery,"state-pages")).nativeObjectState,updatedToken);
  console.log(`PASS: OPFS state compaction ${bytesBefore} → ${compactedFiles[0][1]} B, exact tokens, stale in-flight offsets rejected, reload/retry; median ${measurements.sort((a,b)=>a-b)[2].toFixed(2)} ms for 1,200 identities (5 local runs)`);

  const sequenced=structuredClone(nativeRecords);
  for(const record of sequenced)if(record.kind==="manifest")record.manifest.GenerationSequence=1;
  await cmd({kind:"begin",collection:collection("generation-first",nativeSelection)},"generation");
  await cmd({kind:"native-begin",session:"generation-first"},"generation");
  for(const record of sequenced)await cmd(nativeRecord("generation-first",record),"generation");
  const original=(await cmd({kind:"native-finish",session:"generation-first",collection:"generation-first",selection:nativeSelection},"generation")).nativeSources[0];
  await cmd({kind:"native-begin",session:"delayed-old"},"generation");
  for(const record of sequenced)await cmd(nativeRecord("delayed-old",record),"generation");
  const replacementRecords=structuredClone(sequenced);
  for(const record of replacementRecords)if(record.kind==="manifest"){
    record.manifest.GenerationSequence=2;record.manifest.Generation[0]^=1;
  }
  rebindPAC(replacementRecords);
  await cmd({kind:"native-begin",session:"replacement"},"generation",1);
  for(const record of replacementRecords)await cmd(nativeRecord("replacement",record),"generation",1);
  const replacement=(await cmd({kind:"native-finish",session:"replacement",collection:"generation-first",selection:nativeSelection},"generation",1)).nativeSources[0];
  assert.notEqual(original,replacement);
  assert.equal((await cmd({kind:"native-coverage"},"generation")).nativeCoverage.length,1);
  await assert.rejects(cmd({kind:"native-finish",session:"delayed-old"},"generation"),/retired/);
  await assert.rejects(cmd({kind:"native-page",source:original,from:0,to:Date.now()},"generation"),/retired/);
  const inventory=await raw("inventory");
  assert.ok(!Object.keys(inventory).some(p=>p.includes("/generation/branches/"+original+"/")&&p.endsWith("events.sods")));
  assert.ok(Object.keys(inventory).some(p=>p.includes("/generation/branches/"+replacement+"/")&&p.endsWith("events.sods")));
  await page.reload();
  await page.evaluate(()=>{globalThis.workers=[new Worker("/harness.js",{type:"module"}),new Worker("/harness.js",{type:"module"})];});
  const restoredGeneration=await cmd({kind:"restore",collection:"generation-first"},"generation");
  assert.equal(restoredGeneration.done,true);
  const replacementPage=await cmd({...pageRequest,source:replacement},"generation");
  assert.deepEqual(replacementPage.nativePage,nativePage);
  console.log("PASS: ordered retention generations, atomic working-set retirement, stale response rejection, OPFS reclamation, and replacement-only restoration after reload");
  await cmd({kind:"native-begin",session:"before-rebase"},"generation",1);
  for(const record of replacementRecords)await cmd(nativeRecord("before-rebase",record),"generation",1);
  await cmd({kind:"native-rebase",sources:[sequenced.find(r=>r.kind==="manifest").binding.tap]},"generation");
  assert.equal((await cmd({kind:"native-coverage"},"generation")).nativeCoverage.length,0);
  await assert.rejects(cmd({kind:"native-finish",session:"before-rebase"},"generation",1),/cache rebased/);
  await assert.rejects(cmd({...pageRequest,source:replacement},"generation"),/retired/);
  await page.reload();
  await page.evaluate(()=>{globalThis.workers=[new Worker("/harness.js",{type:"module"}),new Worker("/harness.js",{type:"module"})];});
  await cmd({kind:"native-begin",session:"after-rebase"},"generation");
  for(const record of sequenced)await cmd(nativeRecord("after-rebase",record),"generation");
  const rebased=(await cmd({kind:"native-finish",session:"after-rebase"},"generation")).nativeSources[0];
  assert.notEqual(rebased,original);assert.notEqual(rebased,replacement);
  assert.deepEqual((await cmd({...pageRequest,source:rebased},"generation")).nativePage,nativePage);
  assert.deepEqual((await cmd(stateQuery,"state-pages")).nativeObjectState,updatedToken);
  const afterRebaseFiles=await raw("inventory");
  assert.ok(!Object.keys(afterRebaseFiles).some(p=>p.includes("/generation/branches/"+replacement+"/")&&p.endsWith("events.sods")));
  console.log("PASS: explicit scoped cache rebuild, cross-worker stale response rejection, reload, server generation rollback, new local references and preserved unrelated cache");

} finally {
  await context.close();
  await rm(output, { recursive: true, force: true });
}
