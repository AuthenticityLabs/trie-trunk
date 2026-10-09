import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Codec } from "./pil/wasm.ts";
import { PILStream, consume } from "./pil/stream.ts";
import { compile } from "./pil/metadata.ts";
import { PILEvent } from "./pil/event.ts";
import { expandEvent, rawValue, valueAt } from "./dictionary.ts";
import { compileIndicator } from "./indicators.ts";
import { compileSearch } from "./search.ts";
import { WorkingSet } from "./working-set.ts";
import { bytes, fixture, magic, response } from "./pil-fixtures.mjs";
const module = await WebAssembly.compile(
  readFileSync(new URL("../assets/pil.wasm", import.meta.url)),
);
async function reader(defs) {
  const codec = new Codec(await WebAssembly.instantiate(module)),
    definitions = defs ?? new Map(),
    bindings = new Map(),
    events = [];
  const stream = new PILStream(
    codec,
    definitions,
    () => {},
    (b) => bindings.set(b.stream, compile(definitions.get(b.digest), b.tap)),
    async (batch) => {
      for (let i = 0; i < batch.rows.length; i += 3) {
        events.push(
          new PILEvent(
            new Uint8Array(
              batch.bytes,
              batch.rows[i + 1],
              batch.rows[i + 2] - batch.rows[i + 1],
            ),
            bindings.get(batch.rows[i]),
          ),
        );
      }
    },
  );
  return { codec, definitions, events, stream };
}
test("real WASM validates split PIL and views retain original frame bytes and sparse dictionary codes", async () => {
  const f = fixture(),
    r = await reader();
  await consume(
    response(f.cold, "a".repeat(64), 1),
    r.stream,
    new AbortController().signal,
  );
  const event = r.events[0];
  assert.equal(r.events.length, 1);
  assert.deepEqual(event.encoded(), f.row());
  assert.equal("values" in event, false);
  assert.equal(rawValue(event, event.fields.names.get("service")), 2);
  assert.equal(valueAt(event, "service"), "vault");
  assert.equal(valueAt(event, "message"), "unauthorized λ");
  assert.deepEqual(expandEvent(event), {
    id: "019f0234-6600-7000-8000-000000000001",
    time: new Date(event.millis).toISOString().replace(".000Z", "Z"),
    service: "vault",
    level: "ERROR",
    duration: 2237,
    error: { code: "unauthorized" },
    message: "unauthorized λ",
  });
  const working = new WorkingSet();
  working.setMatchers([
    compileIndicator({
      id: "error",
      label: "Auth failure",
      path: ["error", "code"],
      mode: "text",
      pattern: "unauthorized",
      ignoreCase: false,
    }),
  ]);
  working.append([event, event]);
  assert.equal(working.collected.events.length, 1);
  assert.equal(working.collected.summary().errors, 1);
  assert.equal(working.collected.annotations.get(event).length, 1);
  await working.search(
    compileSearch({
      parameters: { service: "billing" },
      tap: "",
      from: "",
      to: "",
    }),
    async () => {},
  );
  assert.equal(working.visible.events.length, 0);
  await working.search();
  assert.equal(working.visible.events[0], event);
});
test("a cached definition decodes repeated pulls without DEFINE; missing cache is rejected", async () => {
  const f = fixture(),
    r = await reader();
  await consume(response(f.schemas), r.stream, new AbortController().signal);
  const bindings = new Map(),
    rows = [];
  const stream = new PILStream(
    r.codec,
    r.definitions,
    () => assert.fail("definition repeated"),
    (b) => bindings.set(b.stream, b),
    async (batch) => rows.push(batch.rows.length / 3),
  );
  await consume(response(f.cached), stream, new AbortController().signal);
  assert.deepEqual(rows, [1]);
  const missing = await reader();
  await assert.rejects(
    consume(response(f.cached), missing.stream, new AbortController().signal),
    /Unknown schema/,
  );
});
test("truncated, altered, unbound and unsuccessful PIL never certifies collection coverage", async () => {
  const f = fixture();
  for (const bad of [
    f.cold.slice(0, -1),
    f.cold.slice(0, -2),
    bytes(magic, f.event, [255, 254]),
    bytes(f.cold, [0]),
  ]) {
    const r = await reader();
    await assert.rejects(
      consume(response(bad), r.stream, new AbortController().signal),
    );
  }
  const corrupt = f.cold.slice();
  corrupt[15] ^= 1;
  const r = await reader();
  await assert.rejects(
    consume(response(corrupt), r.stream, new AbortController().signal),
    /digest mismatch/,
  );
});
test("large extended offsets and overlapping tap identities stay correct", async () => {
  const f = fixture(42, "x".repeat(70000)),
    r = await reader();
  await consume(
    response(f.cold, "a".repeat(64), 997),
    r.stream,
    new AbortController().signal,
  );
  assert.equal(valueAt(r.events[0], "message").length, 70000);
  const mapping = compile(f, "second"),
    other = new PILEvent(f.row(), mapping),
    working = new WorkingSet();
  working.append([r.events[0], other]);
  assert.equal(working.collected.events.length, 2);
});
test("HTTP failures and protocol mismatches are surfaced before row delivery", async () => {
  for (const response of [
    new Response("unauthorized", { status: 401 }),
    new Response("<html/>"),
    new Response(magic, {
      headers: { "Content-Type": "application/pil", "PIL-Version": "999" },
    }),
  ]) {
    const r = await reader();
    await assert.rejects(
      consume(response, r.stream, new AbortController().signal),
    );
    assert.equal(r.events.length, 0);
  }
});
