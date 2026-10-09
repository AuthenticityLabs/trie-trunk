import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { workerService } from "./pil/worker-runtime.ts";
import { fixture, response, bytes, magic } from "./pil-fixtures.mjs";
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
    if (m.kind === "batch") port.postMessage({ kind: "ack", id: m.id });
    if (m.kind === "done") {
      jobs.delete(m.id);
      job.resolve(job.messages);
    }
    if (m.kind === "error") {
      jobs.delete(m.id);
      job.reject(Error(m.message));
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
