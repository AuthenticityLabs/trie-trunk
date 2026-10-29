import {NativeCacheError} from "./store/errors.ts";
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { workerService } from "./pil/worker-runtime.ts";
import { fixture, response, bytes, magic, frame, le, pilObject } from "./pil-fixtures.mjs";
import { NativeStore } from "./store/native.ts";
import { Backend } from "./native-backend-fixture.mjs";
import { Codec } from "./pil/wasm.ts";
import { PILStream } from "./pil/stream.ts";
const module = WebAssembly.compile(
  readFileSync(new URL("../assets/pil.wasm", import.meta.url)),
);
const selection = {
  tap: "test",
  from: "2026-01-01T00:00:00Z",
  to: "2027-01-01T00:00:00Z",
  where: [],
  limit: null,
  tail: false,
  follow: false,
  pretty: false,
};
function tab(connect, t) {
  const channel = new MessageChannel();
  connect(channel.port2);
  const port = channel.port1;
  let seq = 0;
  const jobs = new Map();
  port.onmessage = ({ data: m }) => {
    const job = jobs.get(m.id);
    if (!job) return;
    job.messages.push(m);
    if (m.kind === "batch" || m.kind === "native-batch") port.postMessage({ kind: "ack", id: m.id });
    if (m.kind === "done") {
      jobs.delete(m.id);
      job.resolve(job.messages);
    }
    if (m.kind === "error") {
      jobs.delete(m.id);
      job.reject(Object.assign(Error(m.message), { messages: job.messages }));
    }
  };
  t.after(() => {
    port.postMessage({ kind: "close" });
    port.close();
  });
  return {
    port,
    start(command) {
      const id = ++seq;
      const promise = new Promise((resolve, reject) =>
        jobs.set(id, { resolve, reject, messages: [] }),
      );
      port.postMessage({ id, ...command });
      return { id, promise };
    },
    run(command) {
      return this.start(command).promise;
    },
  };
}
test("explicit cache rebase goes only to the credential-scoped archive", async t => {
 const commands=[];
 const connect=workerService(module,async()=>{throw Error("unexpected network request");},async(token,command)=>{commands.push({token,command});return {};});
 const client=tab(connect,t);
 await client.run({kind:"rebase",sources:["demo/payment/transactions.v1/write"],token:"auth"});
 assert.deepEqual(commands,[{token:"auth",command:{kind:"native-rebase",sources:["demo/payment/transactions.v1/write"]}}]);
});
const historyBytes = readFileSync(new URL("./fixtures/replay/history.pil", import.meta.url));
const historyWarmBytes = readFileSync(new URL("./fixtures/replay/warm.pil", import.meta.url));
const historyMetadata = JSON.parse(readFileSync(new URL("./fixtures/replay/metadata.json", import.meta.url), "utf8"));
const historyQuery = {
  source: "demo/payment/transactions.v1/write", id: "42",
  from: "2026-10-16T00:00:00Z", to: "2026-10-17T00:00:00Z",
};
function historyResponse(body = historyBytes) {
  const reply = response(body, historyMetadata.definition, 37);
  reply.headers.set("Trunk-State", historyMetadata.token);
  reply.headers.set("Trunk-Replay", "retained-history");
  return reply;
}
test("object history streams original cross-minor PIL and reuses maps without certifying time coverage", async t => {
  const calls = [], storage = [];
  const connect = workerService(module, async (url, options) => {
    calls.push(options.headers);
    const parsed = new URL(url, "https://localhost");
    assert.equal(parsed.pathname, "/collections/42");
    assert.equal(parsed.searchParams.has("id"), false);
    assert.equal(options.cache, 'no-store');
    assert.equal(options.headers.Authorization, "Bearer auth");
    return historyResponse(calls.length === 1 ? historyBytes : historyWarmBytes);
  }, async (_, command) => {
    storage.push(command);
    return { definitions: [] };
  });
  const current = tab(connect, t);
  for (let n = 0; n < 2; n++) {
    const messages = await current.run({ kind: "history", query: historyQuery, token: "auth" });
    assert.equal(messages.filter(m => m.kind === "definition").length, 2);
    assert.equal(messages.filter(m => m.kind === "batch").reduce((count, m) => count + m.batch.rows.length / 3, 0), 2);
    assert.deepEqual(messages.at(-2), { id: n + 1, kind: "history-state", state: {
      source: historyQuery.source, id: "42", token: historyMetadata.token,
    } });
    assert.equal(messages.at(-1).kind, "done");
  }
  assert.equal(calls[0]["Definition-Known"], "");
  assert.equal(calls[1]["Definition-Known"].split(",").length, 2);
  assert.deepEqual(storage.map(c => c.kind), ["definitions", "remember", "remember"]);
});
test("truncated object history never publishes a state token or remembered replay", async t => {
  const storage = [];
  const connect = workerService(module, async () => historyResponse(historyBytes.subarray(0, -1)), async (_, c) => {
    storage.push(c.kind); return { definitions: [] };
  });
  await assert.rejects(tab(connect, t).run({ kind: "history", query: historyQuery, token: "auth" }), error => {
    assert.ok(!error.messages.some(m => m.kind === "history-state"));
    return true;
  });
  assert.deepEqual(storage, ["definitions"]);
});
test("object history rejects a server binding outside the requested collection", async t => {
  const connect = workerService(module, async () => historyResponse());
  await assert.rejects(tab(connect, t).run({ kind: "history", query: { ...historyQuery, source: "demo/customer/accounts.v1/write" }, token: "auth" }), /escaped the selected collection/);
});
test("two tabs share negotiated definitions; collect sends both version headers and transfers PIL, scoped by credential", async (t) => {
  const f = fixture(),
    calls = [];
  const connect = workerService(module, async (url, options) => {
    calls.push({ url, headers: options.headers });
    if (url === "/schemas") {
      await new Promise((r) => setTimeout(r, 10));
      return response(f.schemas);
    }
    assert.equal(new URL(url, "https://localhost").pathname, "/collect");
    assert.equal(options.method, "GET");
    assert.equal(options.body, undefined);
    assert.equal(options.headers["PIL-Version"], "2");
    assert.equal(options.headers["Definition-Version"], "a".repeat(64));
    assert.equal(options.headers.Accept, "application/pil");
    return response(f.cached);
  });
  const a = tab(connect, t),
    b = tab(connect, t);
  await Promise.all([
    a.run({ kind: "schemas", tap: "test", token: "one" }),
    b.run({ kind: "schemas", tap: "test", token: "one" }),
  ]);
  assert.equal(calls.length, 1);
  for (const current of [a, b]) {
    const messages = await current.run({
      kind: "events",
      selection,
      token: "one",
    });
    assert.equal(messages.filter((m) => m.kind === "batch").length, 1);
    assert.equal(messages.filter((m) => m.kind === "definition").length, 0);
    const batch = messages.find((m) => m.kind === "batch").batch;
    assert.ok(batch.bytes.byteLength > 0);
    assert.equal(batch.rows.length, 3);
  }
  await b.run({ kind: "schemas", tap: "test", token: "two" });
  assert.equal(calls.filter((c) => c.url === "/schemas").length, 2);
});
test("cold collect gets definitions first, newer version refreshes in the same request and is reused", async (t) => {
  const first = fixture(),
    second = fixture(43, "restarted");
  let current = first,
    version = "a".repeat(64),
    calls = 0;
  const connect = workerService(module, async (url, options) => {
    calls++;
    assert.equal(new URL(url, "https://localhost").pathname, "/collect");
    assert.equal(options.method, "GET");
    assert.equal(options.body, undefined);
    return response(
      options.headers["Definition-Version"] === version
        ? current.cached
        : current.cold,
      version,
      37,
    );
  });
  const client = tab(connect, t);
  const collect = () => client.run({ kind: "events", selection, token: "" });
  let messages = await collect();
  assert.ok(
    messages.findIndex((m) => m.kind === "definition") <
      messages.findIndex((m) => m.kind === "batch"),
  );
  messages = await collect();
  assert.equal(
    messages.some((m) => m.kind === "definition"),
    false,
  );
  current = second;
  version = "b".repeat(64);
  messages = await collect();
  assert.equal(
    messages.find((m) => m.kind === "definition").definition.digest,
    second.digest,
  );
  messages = await collect();
  assert.equal(
    messages.some((m) => m.kind === "definition"),
    false,
  );
  assert.equal(calls, 4);
});
test("canceling one port aborts only its event request; the other tab can reuse definitions", async (t) => {
  const f = fixture();
  let active;
  const connect = workerService(module, async (url, options) => {
    if (url === "/schemas") return response(f.schemas);
    if (new URL(url, "https://localhost").searchParams.get("follow") !== "true")
      return response(f.cached);
    active = options.signal;
    return new Response(
      new ReadableStream({
        start(controller) {
          controller.enqueue(bytes(magic, f.bind()));
          options.signal.addEventListener("abort", () =>
            controller.error(options.signal.reason),
          );
        },
      }),
      {
        headers: {
          "Content-Type": "application/pil",
          "PIL-Version": "2",
          "Definition-Version": "a".repeat(64),
          "Trunk-Completion": "body",
        },
      },
    );
  });
  const a = tab(connect, t),
    b = tab(connect, t);
  await a.run({ kind: "schemas", tap: "test", token: "" });
  const job = a.start({
    kind: "events",
    selection: { ...selection, follow: true },
    token: "",
  });
  while (!active) await new Promise((r) => setTimeout(r, 1));
  a.port.postMessage({ kind: "cancel", id: job.id });
  await new Promise((r) => setTimeout(r, 5));
  assert.equal(active.aborted, true);
  const messages = await b.run({ kind: "events", selection, token: "" });
  assert.equal(
    messages.some((m) => m.kind === "definition"),
    false,
  );
});

test("changing tap selection reuses cached definition bodies for the same credentials", async (t) => {
  const f = fixture();
  const known = [];
  const connect = workerService(module, async (url, options) => {
    assert.equal(url, "/schemas");
    const query = JSON.parse(options.body);
    known.push(query.known);
    return response(
      query.known.includes(f.digest)
        ? bytes(magic, f.bind("other"), [255, 254])
        : f.schemas,
    );
  });
  const client = tab(connect, t);
  await client.run({ kind: "schemas", tap: "test", token: "one" });
  await client.run({ kind: "schemas", tap: "other", token: "one" });
  assert.deepEqual(known, [[], [f.digest]]);
});

test("persisted definitions are negotiated as known and frames commit before delivery", async (t) => {
  const f = fixture(),
    order = [];
  const archive = async (token, command) => {
    assert.equal(token, "credential");
    order.push(command.kind);
    if (command.kind === "definitions")
      return {
        definitions: [{ digest: f.digest, store: f.store, pil: f.pil }],
      };
    if (command.kind === "save") {
      assert.equal(command.collection, "collection");
      assert.deepEqual(command.definitions[0].pil, f.pil);
      assert.equal(command.bindings[0].tap, "test");
      return { batch: command.batch };
    }
    return {};
  };
  const client = tab(
    workerService(
      module,
      async (url, options) => {
        if (url === "/schemas") {
          assert.deepEqual(JSON.parse(options.body).known, [f.digest]);
          return response(bytes(magic, f.bind(), [255, 254]));
        }
        return response(f.cached);
      },
      archive,
    ),
    t,
  );
  await client.run({ kind: "schemas", tap: "test", token: "credential" });
  const messages = await client.run({
    kind: "events",
    selection,
    token: "credential",
    collection: "collection",
  });
  assert.deepEqual(order, [
    "definitions",
    "remember",
    "lookup",
    "save",
    "finish",
    "remember",
  ]);
  assert.equal(messages.filter((m) => m.kind === "batch").length, 1);
});
test("restoration streams PIL without a server fetch and releases its cursor", async (t) => {
  const f = fixture(),
    commands = [];
  const archive = async (token, c) => {
    commands.push(c.kind);
    if (c.kind === "restore")
      return {
        collection: { id: "saved", selection },
        coverage: [],
        definitions: [{ digest: f.digest, store: f.store, pil: f.pil }],
        bindings: [{ stream: 0, digest: f.digest, tap: "test" }],
        cursor: "cursor",
      };
    if (c.kind === "definitions") return { definitions: [] };
    if (c.kind === "next")
      return {
        batch: {
          bytes: f.row().buffer,
          rows: Uint32Array.of(0, 0, f.row().length),
        },
        done: true,
      };
    return {};
  };
  const client = tab(
    workerService(
      module,
      async () => {
        throw Error("Unexpected network fetch");
      },
      archive,
    ),
    t,
  );
  const result = await client.run({
    kind: "restore",
    token: "",
    collection: "saved",
  });
  assert.deepEqual(commands, ["restore", "definitions", "next", "release"]);
  assert.equal(result[0].kind, "collection");
  assert.equal(result.filter((r) => r.kind === "batch").length, 1);
});
test("failed persistence cannot be reported as a successful collection", async (t) => {
  const f = fixture();
  const archive = async (_, c) => {
    if (c.kind === "definitions") return { definitions: [] };
    if (c.kind === "lookup") return {};
    if (c.kind === "save") throw Error("Quota exhausted");
    throw Error("Unexpected commit");
  };
  const client = tab(
    workerService(module, async () => response(f.cold), archive),
    t,
  );
  await assert.rejects(
    client.run({ kind: "events", selection, token: "", collection: "c" }),
    /Quota exhausted/,
  );
});

test("cold collect reuses persisted definitions in one GET without schema discovery", async (t) => {
  const f = fixture();
  let calls = 0;
  const archive = async (_, c) =>
    c.kind === "definitions"
      ? { definitions: [{ digest: f.digest, store: f.store, pil: f.pil }] }
      : {};
  const client = tab(
    workerService(
      module,
      async (url, options) => {
        calls++;
        const query = new URL(url, "https://localhost");
        assert.equal(query.pathname, "/collect");
        assert.equal(options.method, "GET");
        assert.equal(options.body, undefined);
        assert.equal(options.headers["Definition-Version"], "");
        assert.equal(options.headers["Definition-Known"], f.digest);
        assert.equal(query.searchParams.get("limit"), "all");
        assert.deepEqual(query.searchParams.getAll("where"), [
          'service=="one & two"',
          'message=="a+b?=é"',
        ]);
        return response(f.cached);
      },
      archive,
    ),
    t,
  );
  const messages = await client.run({
    kind: "events",
    selection: {
      ...selection,
      where: ['service=="one & two"', 'message=="a+b?=é"'],
    },
    token: "",
  });
  assert.equal(calls, 1);
  assert.ok(
    messages.findIndex((m) => m.kind === "definition") <
      messages.findIndex((m) => m.kind === "binding"),
  );
  assert.equal(messages.filter((m) => m.kind === "batch").length, 1);
});
test("a pending schema choice lookup does not gate collection", async (t) => {
  const f = fixture();
  let began;
  const pending = new Promise((r) => (began = r));
  let finish;
  const connect = workerService(module, async (url, options) => {
    if (url === "/schemas") {
      began();
      return await new Promise((r) => (finish = () => r(response(f.schemas))));
    }
    assert.equal(options.method, "GET");
    return response(f.cold);
  });
  const client = tab(connect, t);
  const schemas = client.run({ kind: "schemas", tap: "test", token: "" });
  await pending;
  try {
    const events = await client.run({ kind: "events", selection, token: "" });
    assert.equal(events.filter((m) => m.kind === "batch").length, 1);
  } finally {
    finish();
    await schemas;
  }
});

test("a fully covered collection reads local PIL without a collect request", async (t) => {
  const f = fixture(),
    commands = [];
  const client = tab(
    workerService(
      module,
      async () => {
        throw Error("Unexpected network");
      },
      async (_, c) => {
        commands.push(c.kind);
        if (c.kind === "definitions")
          return {
            definitions: [{ digest: f.digest, store: f.store, pil: f.pil }],
          };
        if (c.kind === "lookup")
          return {
            cursor: "local",
            gaps: [],
            definitions: [{ digest: f.digest, store: f.store, pil: f.pil }],
            bindings: [{ stream: 0, tap: "test", digest: f.digest }],
          };
        if (c.kind === "next")
          return {
            done: true,
            batch: {
              bytes: f.row().buffer,
              rows: Uint32Array.of(0, 0, f.row().length),
            },
          };
        return {};
      },
    ),
    t,
  );
  const messages = await client.run({
    kind: "events",
    selection,
    token: "",
    collection: "cached",
  });
  assert.equal(messages.filter((m) => m.kind === "batch").length, 1);
  assert.deepEqual(commands, [
    "definitions",
    "lookup",
    "next",
    "release",
    "finish",
  ]);
});
test("only missing ranges are fetched, and a failed later gap never certifies the whole collection", async (t) => {
  const f = fixture(),
    calls = [],
    finished = [];
  const gaps = [
    {
      from: Date.parse("2026-01-01T00:00:00Z"),
      to: Date.parse("2026-02-01T00:00:00Z"),
    },
    {
      from: Date.parse("2026-12-01T00:00:00Z"),
      to: Date.parse("2027-01-01T00:00:00Z"),
    },
  ];
  const archive = async (_, c) => {
    if (c.kind === "definitions") return { definitions: [] };
    if (c.kind === "lookup")
      return { cursor: "c", gaps, definitions: [], bindings: [] };
    if (c.kind === "next") return { done: true };
    if (c.kind === "save") return { batch: c.batch };
    if (c.kind === "finish") finished.push(c.selection);
    return {};
  };
  const client = tab(
    workerService(
      module,
      async (url) => {
        calls.push(new URL(url, "https://local"));
        return calls.length === 1
          ? response(f.cold)
          : new Response("failed gap", { status: 500 });
      },
      archive,
    ),
    t,
  );
  await assert.rejects(
    client.run({ kind: "events", selection, token: "", collection: "c" }),
    /failed gap/,
  );
  assert.deepEqual(
    calls.map((u) => [
      Date.parse(u.searchParams.get("from")),
      Date.parse(u.searchParams.get("to")),
    ]),
    gaps.map((g) => [g.from, g.to]),
  );
  assert.equal(finished.length, 1);
  assert.equal(finished[0].to, new Date(gaps[0].to).toISOString());
});

test("native-capable worker collects missing ranges, delivers unassembled pages and restores offline",async t=>{
 const wire=new Uint8Array(readFileSync(new URL("./fixtures/native-ranges/ranges.pil",import.meta.url)));
 const definitions=new Map(),records=[];
 const parser=new PILStream(new Codec(await WebAssembly.instantiate(await module)),new Map(),d=>definitions.set(d.digest,d),()=>{},async()=>{},async r=>records.push(r));
 await parser.feed(wire);parser.finish();parser.close();
 const first=records[0],binding=first.binding,definition=definitions.get(binding.digest),hash=Uint8Array.from(Buffer.from(binding.digest,"hex"));
 const define=bytes([1],frame([3,3,3],[hash,definition.store,definition.pil]));
 const bind=bytes([2],frame([2,3,3],[le(binding.stream,4),hash,new TextEncoder().encode(binding.tap)]));
 const schemas=bytes(magic,define,bind,[255,254]);
 const manifest={...first.manifest,IndexBases:[]};
 const state=records.find(r=>r.kind==="chunk"&&r.file==="state");
 const warm=bytes(magic,bind,[4],frame([2,3],[le(binding.stream,4),pilObject(manifest)]),[5],frame([2,2,2,3,3],[le(binding.stream,4),le(2,1),le(0,8),Uint8Array.from(Buffer.from(state.digest,"hex")),state.bytes]),[6],frame([2],[le(binding.stream,4)]),[255,254]);
 const backend=new Backend(),store=new NativeStore(backend),sessions=new Map(),saved=new Map();
 const archive=async(token,c)=>{
  for(const item of c.records??[])await store.record(sessions.get(c.session),item.record,item.definition);
  switch(c.kind){
   case "definitions":return {definitions:await store.definitions()};
   case "remember":return {};
   case "begin":saved.set(c.collection.id,{collection:c.collection});return {};
   case "native-begin":sessions.set(c.session,store.begin());return {};
   case "native-coverage":return {nativeCoverage:await store.coverage(c.tap)};
   case "native-records":return {};
   case "native-record":await store.record(sessions.get(c.session),c.record,c.definition);return {};
   case "native-finish":{
    const session=sessions.get(c.session);await store.finish(session);sessions.delete(c.session);
    const sources=[...new Set(session.packages.map(p=>p.key))];
    saved.get(c.collection).nativeSelections=sources.map(source=>({source,from:Date.parse(c.selection.from),to:Date.parse(c.selection.to),where:[]}));
    return {nativeSources:sources};
   }
   case "native-abort":sessions.delete(c.session);return {};
   case "native-dependencies":return {nativeDependencies:await store.dependencies(c.source)};
   case "native-rows":return {nativePage:await store.records(c.source,c.from,c.to,c.rows)};
   case "native-page":return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit)};
   case "restore":return {...saved.get(c.collection),coverage:[],cursor:"empty-legacy",definitions:[],bindings:[]};
   case "next":return {done:true};
   case "release":return {};
   default:throw Error(`unexpected legacy archive action ${c.kind}`);
  }
 };
 let requests=0;
 const fetcher=async(url,options)=>{
  if(url==="/schemas")return new Response(schemas,{headers:{"Content-Type":"application/pil","PIL-Version":"2","Definition-Version":"a".repeat(64),"Trunk-Completion":"body","Trunk-Features":"native-ranges"}});
  assert.match(url,/format=native/);assert.equal(options.method,"GET");requests++;
  if(requests===2){
   const have=JSON.parse(Buffer.from(options.headers["Trunk-Coverage"],"base64url"));
   assert.deepEqual(have[0].Index,[{Start:1,End:7}]);
  }
  return new Response(requests===1?wire:warm,{headers:{"Content-Type":"application/pil","PIL-Version":"2","Definition-Version":binding.digest,"Trunk-Completion":"body","Trunk-Format":"native-pac/0"}});
 };
 const current=tab(workerService(module,fetcher,archive),t);
 await current.run({kind:"schemas",tap:binding.tap,token:"native"});
 const query={...selection,tap:binding.tap,from:"2026-10-16T12:12:00Z",to:"2026-10-16T12:28:00Z"};
 const collection={id:"native-working-set",created:1,selection:query,parameters:{},range:"custom",from:query.from,to:query.to};
 await current.run({kind:"begin",collection,token:"native"});
 for(let n=0;n<2;n++){
  const replies=await current.run({kind:"events",selection:query,collection:collection.id,token:"native"});
  assert.equal(replies.filter(r=>r.kind==="batch").length,0);
  const page=replies.find(r=>r.kind==="native-batch").page;
  assert.equal(page.rows.length,6);
  const check=new Codec(await WebAssembly.instantiate(await module)),handle=check.define(definition.pil);
  check.upload(page.payload);assert.equal(check.scanStructural(handle,0,page.payload.length),page.payload.length);check.release(handle);
  assert.ok(replies.some(r=>r.kind==="native-source"));
 }
 assert.equal(requests,2);
 const second=tab(workerService(module,async()=>{throw Error("offline restore attempted network")},archive),t);
 const restored=await second.run({kind:"restore",collection:collection.id,token:"native"});
 assert.equal(restored.filter(r=>r.kind==="native-batch").length,1);
 assert.equal(restored.find(r=>r.kind==="collection").collection.id,collection.id);
});

test("native worker delivers provisional hours in memory without claiming persistent event coverage",async t=>{
 const native=readFileSync(new URL("./fixtures/pac-storage/native-open.pil",import.meta.url));
 const live=readFileSync(new URL("./fixtures/pac-storage/live-open.pil",import.meta.url));
 const definitions=new Map(),records=[];
 const parser=new PILStream(new Codec(await WebAssembly.instantiate(await module)),new Map(),d=>definitions.set(d.digest,d),()=>{},async()=>{},async r=>records.push(r));
 await parser.feed(native);parser.finish();parser.close();
 const binding=records[0].binding,d=definitions.get(binding.digest),hash=Uint8Array.from(Buffer.from(binding.digest,"hex"));
 const schemas=bytes(magic,[1],frame([3,3,3],[hash,d.store,d.pil]),[2],frame([2,3,3],[le(binding.stream,4),hash,new TextEncoder().encode(binding.tap)]),[255,254]);
 const backend=new Backend(),store=new NativeStore(backend),sessions=new Map(),calls=[];
 const archive=async(token,c)=>{
  calls.push(c.kind);
  for(const item of c.records??[])await store.record(sessions.get(c.session),item.record,item.definition);
  switch(c.kind){
   case "definitions":return {definitions:await store.definitions()};
   case "remember":return {};
   case "native-begin":sessions.set(c.session,store.begin());return {};
   case "native-coverage":return {nativeCoverage:await store.coverage(c.tap)};
   case "native-records":return {};
   case "native-record":await store.record(sessions.get(c.session),c.record,c.definition);return {};
   case "native-finish":{const session=sessions.get(c.session);await store.finish(session);return {nativeSources:[...new Set(session.packages.map(p=>p.key))]};}
   case "native-abort":sessions.delete(c.session);return {};
   case "native-dependencies":return {nativeDependencies:await store.dependencies(c.source)};
   case "native-rows":return {nativePage:await store.records(c.source,c.from,c.to,c.rows)};
   case "native-page":return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit)};
   default:throw Error(`unexpected persistence of provisional data: ${c.kind}`);
  }
 };
 const requests=[];
 const fetcher=async url=>{
  requests.push(url);
  const headers={"Content-Type":"application/pil","PIL-Version":"2","Definition-Version":binding.digest,"Trunk-Completion":"body","Trunk-Features":"native-ranges"};
  if(url==="/schemas")return new Response(schemas,{headers});
  if(url.includes("format=native"))return new Response(native,{headers:{...headers,"Trunk-Format":"native-pac/0"}});
  assert.match(url,/from=2026-10-19T12/);return new Response(live,{headers});
 };
 const client=tab(workerService(module,fetcher,archive),t);
 await client.run({kind:"schemas",tap:binding.tap,token:""});
 const query={...selection,tap:binding.tap,from:"2026-10-19T12:00:00Z",to:"2026-10-19T13:00:00Z"};
 const replies=await client.run({kind:"events",selection:query,collection:"live",token:""});
 assert.equal(replies.filter(r=>r.kind==="batch").reduce((n,r)=>n+r.batch.rows.length/3,0),2);
 assert.equal(requests.length,3);
 assert.ok(!calls.includes("save")&&!calls.includes("finish")&&!calls.includes("lookup"));
 for(const source of await backend.list()){assert.equal(source.index,undefined);assert.equal(source.payload,undefined);assert.equal(source.timeTrie,undefined);}
});

test('view delivery completes independently of failed durable cache storage',async t=>{
 const raw=readFileSync(new URL('./fixtures/pac-view/cold.pil',import.meta.url));
 const storage=[];
 const archive=async(token,command)=>{
  storage.push(command.kind);
  if(command.kind==='definitions')return {definitions:[]};
  throw Error('disk quota exhausted');
 };
 const fetcher=async url=>{
  assert.match(url,/^\/events\?/);
  const r=response(raw,'a'.repeat(64));r.headers.set('Trunk-Format','pac-view/0');return r;
 };
 const client=tab(workerService(module,fetcher,archive),t);
 const replies=await client.run({kind:'view',token:'auth',query:{engine:'tantivy',tap:'demo/payment/transactions.v1/write',from:'2026-09-01T12:00:00Z',to:'2026-09-01T14:00:00Z',size:2}});
 const view=replies.find(r=>r.kind==='view').view;
 assert.equal(view.result.page.length,2);assert.equal(replies.at(-1).kind,'done');
 assert.deepEqual(storage,['definitions']);
 await assert.rejects(client.run({kind:'persist-view',token:'auth',definitions:view.definitions,bindings:view.bindings}),/disk quota/);
 assert.equal(view.batch.rows.length,6,'persistence failure does not remove the delivered page');
});


test("worker errors preserve scoped cache recovery and generation diagnostics",async t=>{
 const issue={code:'native-cache-conflict',source:'demo/payment/transactions.v1/write',reason:'generation-fork',detail:{cachedSequence:1,incomingSequence:1}};
 const connect=workerService(module,async()=>{throw Error('unexpected fetch')},async()=>{throw new NativeCacheError('technical conflict',issue)});
 await assert.rejects(tab(connect,t).run({kind:'rebase',sources:[issue.source],token:'auth'}),error=>{
  assert.deepEqual(error.messages.find(m=>m.kind==='error').cacheIssue,issue);return true;
 });
});

test('multi-resource refresh reaches the archive as one credential-scoped command',async t=>{
 const commands=[],sources=['demo/payment/transactions.v1/write','demo/billing/invoices.v1/read'];
 const connect=workerService(module,async()=>{throw Error('unexpected network')},async(token,command)=>{commands.push({token,command});return {}});
 await tab(connect,t).run({kind:'rebase',sources,token:'auth'});
 assert.deepEqual(commands,[{token:'auth',command:{kind:'native-rebase',sources}}]);
});

test('native stream windows survive shared codec reuse at every callback and arbitrary chunk boundaries',async()=>{
 const wire=new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/ranges.pil',import.meta.url)));
 let expected;
 for(const width of [wire.length,97,1]){
  const codec=new Codec(await WebAssembly.instantiate(await module)),seen=[];
  const parser=new PILStream(codec,new Map(),()=>{codec.upload(new Uint8Array(17));},()=>{codec.upload(new Uint8Array(19));},async()=>{throw Error('expanded native events');},async r=>{
   if(r.kind==='manifest'){const check=v=>{if(v instanceof Uint8Array)assert.equal(v.buffer.byteLength,v.byteLength,'nested metadata owns only its visible bytes');else if(v&&typeof v==='object')Object.values(v).forEach(check);};check(r.manifest);}
   seen.push(r.kind==='pac'?{kind:r.kind,bytes:Buffer.from(r.bytes).toString('hex'),bounds:[...r.bounds]}:{kind:r.kind});
   await Promise.resolve();codec.upload(new Uint8Array(23));
  });
  try{for(let at=0;at<wire.length;at+=width)await parser.feed(wire.subarray(at,at+width));parser.finish();}finally{parser.close();}
  if(expected)assert.deepEqual(seen,expected);else expected=seen;
 }
});

test('native reception verifies the sealed CRC and uses offsets without semantic PAC scans',async()=>{
 const wire=new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/ranges.pil',import.meta.url)));
 const codec=new Codec(await WebAssembly.instantiate(await module)),records=[];
 for(const name of ['scan','pac','pacStructural'])codec[name]=()=>{throw Error('semantic/member scan in native reception');};
 let checks=0;const checked=[];const verify=codec.verifyCRC32C.bind(codec);codec.verifyCRC32C=(...a)=>{checks++;checked.push(a[0].slice());return verify(...a);};
 const parse=async(input,output)=>{const stream=new PILStream(codec,new Map(),()=>{},()=>{},async()=>{throw Error('event expansion');},async r=>output.push(r));try{await stream.feed(input);stream.finish();}finally{stream.close();}};
 await parse(wire,records);const packs=records.filter(r=>r.kind==='pac');assert.ok(packs.length);assert.equal(checks,packs.length);
 assert.ok(records.filter(r=>r.kind==='pac'||r.kind==='chunk').every(r=>r.integrityVerified===true));
 const pack=checked[0],at=Buffer.from(wire).indexOf(pack);assert.ok(at>=0);
 // Alter gzip MTIME (valid gzip, unchanged decoded content) or a plain payload.
 const broken=wire.slice();broken[at+(new DataView(pack.buffer).getUint32(4,true)===2?20:pack.length-1)]^=1;
 const failed=[];await assert.rejects(parse(broken,failed),/checksum/);assert.equal(failed.some(r=>r.kind==='pac'),false);
 const backend=new Backend(),store=new NativeStore(backend),session=store.begin(),definitions=new Map();
 const stream=new PILStream(codec,definitions,d=>definitions.set(d.digest,d),()=>{},async()=>{throw Error('event expansion');},r=>store.record(session,r,definitions.get(r.binding.digest)));
 try{await assert.rejects(stream.feed(broken),/checksum/);}finally{stream.close();}
 assert.deepEqual(await store.coverage(),[],'a checksum failure cannot publish an hourly trie leaf');
});

test('contiguous native ranges decode incrementally across arbitrary network boundaries',async()=>{
 const wire=new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/multi-hour.pil',import.meta.url)));
 let expected,wirePacks=[];
 const parse=async(input,width)=>{
  const codec=new Codec(await WebAssembly.instantiate(await module)),records=[];
  wirePacks=[];const verify=codec.verifyCRC32C.bind(codec);codec.verifyCRC32C=(b,c)=>{wirePacks.push(b.slice());return verify(b,c);};
  const stream=new PILStream(codec,new Map(),()=>{},()=>{},async()=>{throw Error('expanded events');},async r=>records.push(r));
  try{for(let at=0;at<input.length;at+=width)await stream.feed(input.subarray(at,at+width));stream.finish();return records;}finally{stream.close();}
 };
 for(const width of [wire.length,31,1]){
  const records=await parse(wire,width),packs=records.filter(r=>r.kind==='pac');
  assert.equal(records.filter(r=>r.kind==='manifest').length,1);assert.equal(records.filter(r=>r.kind==='package-end').length,1);assert.equal(packs.length,3);
  const got=packs.map(r=>({bytes:Buffer.from(r.bytes).toString('hex'),bounds:[...r.bounds]}));
  if(expected)assert.deepEqual(got,expected);else expected=got;
 }
 const packs=(await parse(wire,wire.length)).filter(r=>r.kind==='pac');
 const firstPack=wirePacks[0];const start=Buffer.from(wire).indexOf(firstPack),header=start-13;assert.equal(wire[header],9);
 const length=new DataView(wire.buffer).getBigUint64(header+5,true);
 for(const delta of [-1n,1n]){
  const broken=wire.slice();new DataView(broken.buffer).setBigUint64(header+5,length+delta,true);
  await assert.rejects(parse(broken,37));
 }
 const wrongStream=wire.slice();new DataView(wrongStream.buffer).setUint32(header+1,999,true);
 await assert.rejects(parse(wrongStream,37),/Invalid native PAC range/);
 await assert.rejects(parse(wire.subarray(0,start+firstPack.length),37),/before successful completion/);
});
