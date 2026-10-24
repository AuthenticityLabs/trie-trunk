import assert from "node:assert/strict";
import { test } from "node:test";
import { duration, dictionaryJSONL } from "./events.ts";
import { Dictionaries, expandEvent, valueAt, pack, decodeValue, eventId } from "./dictionary.ts";
import { compileIndicator } from "./indicators.ts";

import { Results } from "./results.ts";
import { eventOf } from "./fixtures.mjs";
function summarize(events, matchers = []) {
  const result = new Results();
  result.append(events);
  result.setMatchers(matchers);
  return result.summary();
}

function identity(millis = Date.parse("2026-09-25T00:00:01Z")) {
  const bytes = Buffer.alloc(16);
  bytes.writeUIntBE(millis, 0, 6);
  bytes[6] = 0x70;
  bytes[8] = 0x80;
  const hex = bytes.toString("hex");
  return {
    raw: bytes.toString("base64"),
    text: [
      hex.slice(0, 8),
      hex.slice(8, 12),
      hex.slice(12, 16),
      hex.slice(16, 20),
      hex.slice(20),
    ].join("-"),
  };
}

const id = identity();
const digest = "a".repeat(64);
const token = "1".repeat(64);
const definition = {
  version: 42,
  fields: {
    3: { name: "level", values: { 5: "ERROR" } },
    4: { name: "service", domain: "service" },
    8: { name: "id", encoding: "uuidv7" },
    12: { name: "duration", unit: "us" },
    15: { name: "message" },
    16: {
      name: "error",
      object: { 0: { name: "code", values: { 1: "unauthorized" } } },
    },
    21: { name: "fields" },
    22: {
      name: "items",
      array: {
        1: { name: "service", domain: "service", values: { 2: "override" } },
      },
    },
  },
  domains: { service: { 2: "vault", 3: "billing" } },
  timezone: "UTC",
};
const binding = { tap: "test", version: 42, digest, dictionary: definition };
const negotiated = { negotiation: token, schemas: [binding] };
const dictionary = { schema: 0, tap: "test", version: 42, headers: [] };
const lines = (...values) =>
  values.map((value) => JSON.stringify(value) + "\n").join("");

test("summary reflects only loaded events and excludes missing durations", () => {
  const result = summarize(
    [
      {
        service: "vault",
        level: "ERROR",
        duration: 2000,
        time: "2026-09-25T00:00:00Z",
      },
      {
        service: "vault",
        level: "INFO",
        duration: 4000,
        time: "2026-09-25T00:01:00Z",
      },
      { service: "billing", level: "error", time: "2026-09-25T00:02:00Z" },
    ].map((value) => eventOf(value)),
  );
  assert.equal(result.errors, 2);
  assert.equal(result.average, 3000);
  assert.deepEqual(result.services, [
    ["vault", 2],
    ["billing", 1],
  ]);
  assert.deepEqual(result.levels, [
    ["ERROR", 1],
    ["INFO", 1],
    ["error", 1],
  ]);
  assert.equal(
    result.buckets.reduce((sum, value) => sum + value, 0),
    3,
  );
  assert.equal(result.buckets[0], 1);
  assert.equal(result.buckets[35], 1);
  assert.equal(duration(result.average), "3.00 ms");
  assert.equal(duration(undefined), "—");
  assert.equal(summarize([]).average, undefined);
});

test("expands nested definitions, domain references, literal fallbacks and opaque objects", () => {
  const decoder = new Dictionaries();
  decoder.install(negotiated);
  decoder.read(dictionary);
  const record = decoder.read([
    8,
    id.raw,
    3,
    5,
    4,
    "literal-service",
    12,
    2234,
    16,
    { 0: 1 },
    21,
    { 3: 5, name: "Bill Smith" },
    22,
    [{ 1: 2 }, { 1: 3 }, { 1: "literal" }],
    99,
    "unknown",
  ]);
  assert.deepEqual(expandEvent(record), {
    id: id.text,
    time: "2026-09-25T00:00:01Z",
    level: "ERROR",
    service: "literal-service",
    duration: 2234,
    error: { code: "unauthorized" },
    fields: { 3: 5, name: "Bill Smith" },
    items: [
      { service: "override" },
      { service: "billing" },
      { service: "literal" },
    ],
    99: "unknown",
  });
  assert.equal(valueAt(decoder.read([8, id.raw, 3, 99]), "level"), 99);
  assert.equal(valueAt(decoder.read([8, id.raw, 3, null]), "level"), null);
});

test("same version in different taps uses distinct handles and switches without resending dictionaries", () => {
  const decoder = new Dictionaries();
  decoder.install(negotiated);
  decoder.read(dictionary);
  const alternate = structuredClone(definition);
  alternate.fields[3].values[5] = "FAILURE";
  decoder.install({
    negotiation: token,
    schemas: [
      {
        tap: "other",
        version: 42,
        digest: "b".repeat(64),
        dictionary: alternate,
      },
    ],
  });
  decoder.read({ schema: 1, tap: "other", version: 42 });
  assert.equal(valueAt(decoder.read([8, id.raw, 3, 5]), "level"), "FAILURE");
  decoder.read({ schema: 0 });
  assert.equal(valueAt(decoder.read([8, id.raw, 3, 5]), "level"), "ERROR");
});

test("presentation headers are exact and otherwise retain lineage then population order", () => {
  for (const headers of [[], ["duration", "time_begin", "level", "duration"]]) {
    const decoder = new Dictionaries();
    decoder.install(negotiated);
    decoder.read({ ...dictionary, headers });
    const record = decoder.read([
      8,
      id.raw,
      4,
      2,
      3,
      5,
      12,
      2234,
      15,
      "literal",
    ]);
    assert.deepEqual(
      Object.keys(expandEvent(record)),
      headers.length
        ? ["duration", "level", "id", "time", "service", "message"]
        : ["id", "time", "service", "level", "duration", "message"],
    );
  }
  const decoder = new Dictionaries();
  decoder.install(negotiated);
  decoder.read(dictionary);
  assert.equal(
    valueAt(
      decoder.read([8, identity(Date.parse("2026-09-25T00:00:01.030Z")).raw]),
      "time",
    ),
    "2026-09-25T00:00:01.03Z",
  );
});

test("rejects missing, unknown or redefined dictionaries and invalid events", () => {
  assert.throws(
    () => new Dictionaries().read([8, id.raw]),
    /before its dictionary/,
  );
  const decoder = new Dictionaries();
  decoder.install(negotiated);
  assert.throws(() => decoder.read({ schema: 4 }), /Unknown dictionary/);
  decoder.read(dictionary);
  assert.throws(() => decoder.read(dictionary), /redefined/);
  for (const value of [
    [8],
    [8, "bad"],
    [8, id.raw, 8, id.raw],
    [8, id.raw, -1, "bad"],
    [15, "missing id"],
  ]) {
    assert.throws(() => decoder.read(value));
  }
});

test("rejects inline dictionaries and changes to an immutable catalog", () => {
  const decoder = new Dictionaries();
  decoder.install(negotiated);
  assert.throws(
    () => decoder.read({ schema: 0, map: definition }),
    /negotiated before/,
  );
  assert.throws(
    () =>
      decoder.install({
        negotiation: token,
        schemas: [{ ...binding, digest: "f".repeat(64) }],
      }),
    /without a new schema version/,
  );
});

test("timeline groups view indicators without modifying events or classifying event_type", () => {
  const from = Date.parse("2026-09-20T00:00:00Z");
  const records = [
    {
      time: new Date(from).toISOString(),
      level: "INFO",
      message: "Deployment started",
      service: "billing",
    },
    {
      time: new Date(from + 1000).toISOString(),
      level: "INFO",
      message: "Deployment started",
      service: "gateway",
    },
    {
      time: new Date(from + 3600000).toISOString(),
      level: "ERROR",
      message: "Request failed",
    },
    {
      time: new Date(from + 3601000).toISOString(),
      level: "CRITICAL",
      message: "Server restarting",
      service: "billing",
    },
    {
      time: new Date(from + 7 * 86400000).toISOString(),
      level: "WARN",
      message: "Release rolled back",
    },
  ];
  const compact = records.map((value) => eventOf(value));
  const matchers = [
    { id: "deploy", label: "Deployment", pattern: "Deployment" },
    { id: "restart", label: "Server restart", pattern: "restarting" },
    { id: "rollback", label: "Rollback", pattern: "rolled back" },
  ].map((rule) =>
    compileIndicator({
      ...rule,
      path: ["message"],
      mode: "text",
      ignoreCase: false,
    }),
  );
  const before = JSON.stringify(records);
  const result = summarize(compact, matchers);
  assert.equal(JSON.stringify(records), before);
  assert.equal(result.signals.length, 3);
  assert.equal(result.signals[0].indicator.label, "Deployment");
  assert.equal(result.signals[0].events.length, 2);
  assert.equal(result.signals[1].indicator.label, "Server restart");
  assert.equal(result.signals[1].events[0], compact[3]);
  assert.equal(result.signals[2].indicator.label, "Rollback");
  assert.equal(result.signals[2].bucket, 35);
  assert.equal(result.errors, 2);
  assert.equal(
    result.errorBuckets.reduce((a, b) => a + b),
    2,
  );
  assert.ok(
    result.errorBuckets.every((value, index) => value <= result.buckets[index]),
  );
  assert.equal(
    summarize([
      eventOf({ time: records[0].time, message: "Deployment started" }),
    ]).signals.length,
    0,
  );
  assert.equal(
    summarize([eventOf({ time: records[0].time, event_type: "deployment" })])
      .signals.length,
    0,
  );
  const single = summarize([compact[0]], matchers);
  assert.equal(single.signals[0].bucket, 0);
  assert.equal(single.buckets[0], 1);
  assert.deepEqual(summarize([]).signals, []);
});

test("cache listing uses GET and does not negotiate dictionaries", async (t) => {
  const { listCaches } = await import("./events.ts");
  const abort = new AbortController();
  t.mock.method(globalThis, "fetch", async (url, options) => {
    assert.equal(url, "/caches");
    assert.equal(options.method ?? "GET", "GET");
    assert.equal(options.headers.Authorization, "Bearer secret");
    assert.equal(options.signal, abort.signal);
    return Response.json({ caches: ["billing", "demo-week"] });
  });
  assert.deepEqual(await listCaches(" secret ", abort.signal), [
    "billing",
    "demo-week",
  ]);
});

test("cache listing reports auth, malformed data and unavailable endpoints", async (t) => {
  const { listCaches } = await import("./events.ts");
  let result;
  t.mock.method(globalThis, "fetch", async () => result);
  for (const [response, pattern] of [
    [new Response("unauthorized", { status: 401 }), /Authentication required/],
    [
      new Response("<html>", { headers: { "Content-Type": "text/html" } }),
      /No cache listing/,
    ],
    [Response.json({ caches: ["../private"] }), /Invalid cache listing/],
    [Response.json({ caches: null }), /Invalid cache listing/],
  ]) {
    result = response;
    await assert.rejects(listCaches("", new AbortController().signal), pattern);
  }
  result = Response.json({ caches: [] });
  assert.deepEqual(await listCaches("", new AbortController().signal), []);
});

test('service/resource labels can come from the source without synthesizing object fields',async()=>{
 const {field}=await import('./events.ts');
 const event=eventOf({message:'hello'});
 event.dictionary.tap='demo/payment/transactions.v1/write';
 assert.equal(field(event,'service'),'payment');assert.equal(field(event,'resource'),'transactions.v1');
 assert.equal(valueAt(event,'service'),undefined);
 const explicit=eventOf({service:'explicit',resource:null});explicit.dictionary.tap=event.dictionary.tap;
 assert.equal(field(explicit,'service'),'explicit');assert.equal(field(explicit,'resource'),'');
});

// Native PIL records use pack() lazily when object fields first render.
test("object UUIDs retain arbitrary versions while event identities require UUIDv7",()=>{
 const field={name:"id",key:1,uuid:true,enums:new Map()};
 for(const version of [1,4,7,8]){
  const bytes=new Uint8Array(16);bytes[6]=version<<4;bytes[8]=0x80;bytes[15]=42;
  assert.equal(pack(bytes,field),bytes);
  const text=`00000000-0000-${version}000-8000-00000000002a`;
  assert.equal(decodeValue(bytes,field),text);
  const encoded=Buffer.from(bytes).toString('base64');
  assert.equal(decodeValue(pack(encoded,field),field),text);
  if(version===7)assert.equal(eventId({uuid:encoded}),text);
  else assert.throws(()=>eventId({uuid:encoded}),/UUIDv7/);
 }
 assert.throws(()=>pack(new Uint8Array(15),field),/UUID bytes/);
});
