import assert from "node:assert/strict";
import { test } from "node:test";
import { compileIndicator, eventFields } from "./indicators.ts";

import { Results } from "./results.ts";
function annotate(events, matchers) {
  const results = new Results();
  results.setMatchers(matchers);
  results.append(events);
  return results.annotations;
}
import { eventOf } from "./fixtures.mjs";
import { expandEvent } from "./dictionary.ts";
const rule = {
  id: "restart",
  label: "Server restart",
  path: ["message"],
  mode: "text",
  pattern: "Server restarting",
  ignoreCase: false,
};

test("text matching is literal and case is explicit", () => {
  const exact = compileIndicator({ ...rule, pattern: "[restart].*" });
  assert.equal(
    exact.matches(eventOf({ message: "prefix [restart].* suffix" })),
    true,
  );
  assert.equal(
    exact.matches(eventOf({ message: "prefix [RESTART].* suffix" })),
    false,
  );
  assert.equal(
    exact.matches(eventOf({ message: "restart everything" })),
    false,
  );
  const insensitive = compileIndicator({ ...rule, ignoreCase: true });
  assert.equal(
    insensitive.matches(eventOf({ message: "SERVER RESTARTING now" })),
    true,
  );
});

test("regex is compiled once and repeated matches remain stable", () => {
  const matcher = compileIndicator({
    ...rule,
    mode: "regex",
    pattern: "^server (restart|boot)ing$",
    ignoreCase: true,
  });
  for (let i = 0; i < 3; i++)
    assert.equal(matcher.matches(eventOf({ message: "Server booting" })), true);
  assert.equal(
    matcher.matches(eventOf({ message: "Server booting later" })),
    false,
  );
  assert.throws(
    () => compileIndicator({ ...rule, mode: "regex", pattern: "[" }),
    SyntaxError,
  );
  assert.throws(() => compileIndicator({ ...rule, pattern: "" }), /pattern/);
  assert.throws(() => compileIndicator({ ...rule, label: "  " }), /label/);
});

test("nested fields, arrays and literal dotted keys have distinct paths", () => {
  const event = eventOf({
    error: { code: 401 },
    "error.code": 403,
    attempts: [{ retry: false }],
    empty: [],
    nil: null,
  });
  assert.deepEqual(
    eventFields(event).filter(
      (field) => !["id", "time"].includes(field.path[0]),
    ),
    [
      { path: ["error", "code"], text: "401" },
      { path: ["error.code"], text: "403" },
      { path: ["attempts", "0", "retry"], text: "false" },
      { path: ["empty"], text: "[]", fixed: true },
      { path: ["nil"], text: "null", fixed: true },
    ],
  );
  for (const field of eventFields(event).filter(
    (field) => !["id", "time"].includes(field.path[0]),
  )) {
    const matcher = compileIndicator({
      ...rule,
      path: field.path,
      pattern: field.text,
    });
    assert.equal(matcher.matches(event), !field.fixed);
    assert.equal(matcher.matches(eventOf({})), false);
  }
  assert.equal(
    compileIndicator({ ...rule, path: ["error.code"], pattern: "401" }).matches(
      event,
    ),
    false,
  );
  assert.equal(
    compileIndicator({
      ...rule,
      path: ["toString"],
      pattern: "function",
    }).matches(eventOf({})),
    false,
  );
});

test("multiple indicators attach to a view without mutating event data", () => {
  const event = Object.freeze(
    eventOf({
      message: "Server restarting",
      operation: "restart",
    }),
  );
  const rules = [
    rule,
    { ...rule, id: "operation", path: ["operation"], pattern: "restart" },
  ];
  const annotations = annotate(
    [event, eventOf({ message: "no match" })],
    rules.map(compileIndicator),
  );
  assert.deepEqual(annotations.get(event), rules);
  assert.equal(annotations.size, 1);
  assert.equal(expandEvent(event).message, "Server restarting");
  assert.equal(expandEvent(event).operation, "restart");
  assert.equal(annotate([event], []).size, 0);
  assert.equal(annotations.get(event)[0], rule);
});

test("numeric comparisons include their boundaries without coercing strings or null", () => {
  for (const mode of ["lte", "gte"]) {
    const matcher = compileIndicator({
      ...rule,
      path: ["duration"],
      mode,
      pattern: "1000.5",
    });
    assert.equal(matcher.matches(eventOf({ duration: 1000.5 })), true);
    assert.equal(matcher.matches(eventOf({ duration: 1000 })), mode === "lte");
    assert.equal(matcher.matches(eventOf({ duration: 1001 })), mode === "gte");
    for (const value of [
      "1000.5",
      null,
      false,
      {},
      [],
      NaN,
      Infinity,
      -Infinity,
    ])
      assert.equal(matcher.matches(eventOf({ duration: value })), false);
    assert.equal(matcher.matches(eventOf({})), false);
    for (const pattern of [" ", "nope", "Infinity", "NaN"])
      assert.throws(
        () => compileIndicator({ ...rule, mode, pattern }),
        /threshold/,
      );
  }
  const zero = compileIndicator({
    ...rule,
    path: ["delta"],
    mode: "lte",
    pattern: "0",
  });
  assert.equal(zero.matches(eventOf({ delta: -1 })), true);
  assert.equal(zero.matches(eventOf({ delta: 0 })), true);
  assert.equal(zero.matches(eventOf({ delta: 1 })), false);
  const nested = compileIndicator({
    ...rule,
    path: ["timing", "duration"],
    mode: "gte",
    pattern: "1e3",
  });
  assert.equal(nested.matches(eventOf({ timing: { duration: 1000 } })), true);
});

test("numeric comparisons never treat an enum reference as a measured value", async () => {
  const { Dictionaries } = await import("./dictionary.ts");
  const { identity } = await import("./fixtures.mjs");
  const reader = new Dictionaries();
  reader.install({
    negotiation: "c".repeat(64),
    schemas: [
      {
        tap: "numeric",
        version: 1,
        digest: "d".repeat(64),
        dictionary: {
          version: 1,
          timezone: "UTC",
          fields: {
            0: { name: "id", encoding: "uuidv7" },
            1: { name: "status", values: { 2: "200", 5: "ERROR" } },
          },
        },
      },
    ],
  });
  reader.read({ schema: 0, tap: "numeric", version: 1 });
  for (const mode of ["lte", "gte"]) {
    const matcher = compileIndicator({
      ...rule,
      path: ["status"],
      mode,
      pattern: mode === "lte" ? "1000" : "0",
    });
    for (const key of [2, 5])
      assert.equal(
        matcher.matches(reader.read([0, identity(), 1, key])),
        false,
      );
  }
});

test("string equality matches the whole literal value without trimming or coercion", () => {
  const matcher = compileIndicator({
    ...rule,
    mode: "equals",
    pattern: "[ready].*",
  });
  assert.equal(matcher.matches(eventOf({ message: "[ready].*" })), true);
  for (const message of [
    "prefix [ready].*",
    "[ready].* suffix",
    "[READY].*",
    " [ready].*",
    "ready",
    null,
    false,
    [],
    {},
  ])
    assert.equal(matcher.matches(eventOf({ message })), false);
  assert.equal(matcher.matches(eventOf({})), false);
  const numericText = compileIndicator({
    ...rule,
    mode: "equals",
    pattern: "200",
  });
  assert.equal(numericText.matches(eventOf({ message: "200" })), true);
  assert.equal(numericText.matches(eventOf({ message: 200 })), false);
  const booleanText = compileIndicator({
    ...rule,
    mode: "equals",
    pattern: "true",
  });
  assert.equal(booleanText.matches(eventOf({ message: true })), false);
});

test("string equality supports explicit ignore-case and empty strings", () => {
  const matcher = compileIndicator({
    ...rule,
    mode: "equals",
    pattern: "Service READY",
    ignoreCase: true,
  });
  assert.equal(matcher.matches(eventOf({ message: "service ready" })), true);
  assert.equal(
    matcher.matches(eventOf({ message: "service ready now" })),
    false,
  );
  const empty = compileIndicator({ ...rule, mode: "equals", pattern: "" });
  assert.equal(empty.matches(eventOf({ message: "" })), true);
  for (const event of [
    eventOf({}),
    eventOf({ message: null }),
    eventOf({ message: " " }),
  ])
    assert.equal(empty.matches(event), false);
  assert.throws(
    () => compileIndicator({ ...rule, mode: "text", pattern: "" }),
    /pattern/,
  );
});

test("string equality reads PIL values at their field and keeps trie fields ineligible", async () => {
  const { fixture } = await import("./pil-fixtures.mjs");
  const { compile } = await import("./pil/metadata.ts");
  const { PILEvent } = await import("./pil/event.ts");
  const f = fixture();
  const event = new PILEvent(f.row(), compile(f, "test"));
  assert.equal(
    compileIndicator({
      ...rule,
      mode: "equals",
      pattern: "unauthorized λ",
    }).matches(event),
    true,
  );
  assert.equal(
    compileIndicator({
      ...rule,
      mode: "equals",
      pattern: "unauthorized",
    }).matches(event),
    false,
  );
  assert.equal(
    compileIndicator({
      ...rule,
      mode: "equals",
      path: ["error", "code"],
      pattern: "unauthorized",
    }).matches(event),
    true,
  );
  assert.equal(
    compileIndicator({
      ...rule,
      mode: "equals",
      path: ["error", "code"],
      pattern: "1",
    }).matches(event),
    false,
  );
  assert.equal(
    compileIndicator({
      ...rule,
      mode: "equals",
      path: ["service"],
      pattern: "vault",
    }).matches(event),
    false,
  );
  assert.equal(
    eventFields(event).find((f) => f.path[0] === "service").fixed,
    true,
  );
  assert.equal(
    eventFields(event).find((f) => f.path[0] === "message").fixed,
    undefined,
  );
  assert.equal("values" in event, false);
});
