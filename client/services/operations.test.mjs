import assert from "node:assert/strict";
import { test } from "node:test";
import { createHash } from "node:crypto";
import { Results } from "./results.ts";
import { Dictionaries, eventId } from "./dictionary.ts";
import { compileIndicator, eventFields } from "./indicators.ts";
import { eventOf, identity } from "./fixtures.mjs";

function decoder(version, serviceKey, serviceValue, serviceLabel = "vault") {
  const definition = {
    version,
    timezone: "UTC",
    path: [serviceKey, 3, 4],
    fields: {
      0: { name: "id", encoding: "uuidv7" },
      [serviceKey]: {
        name: "service",
        values: { [serviceValue]: serviceLabel },
      },
      2: { name: "level", values: { 1: "INFO", 5: "ERROR" } },
      3: { name: "resource", values: { 1: "secrets" } },
      4: { name: "operation", values: { 1: "decrypt" } },
      7: { name: "duration" },
      8: { name: "message" },
    },
  };
  const dictionaries = new Dictionaries();
  dictionaries.install({
    negotiation: "a".repeat(64),
    schemas: [
      {
        tap: "test",
        version,
        digest: createHash("sha256")
          .update(JSON.stringify(definition))
          .digest("hex"),
        dictionary: definition,
      },
    ],
  });
  dictionaries.read({ schema: 0, tap: "test", version });
  return dictionaries;
}

function numericRecord(duration, values = {}) {
  return eventOf({
    service: "vault",
    resource: "secrets",
    operation: "decrypt",
    level: "INFO",
    duration,
    ...values,
  });
}

test("p95 is exact nearest rank across streamed, out-of-order and repeated durations", () => {
  const results = new Results(),
    samples = [];
  let seed = 47;
  for (let batch = 0; batch < 70; batch++) {
    const records = [];
    for (let i = 0; i < 37; i++) {
      seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
      const value = batch % 3 === 0 ? seed % 20 : (seed % 100000) / 10;
      samples.push(value);
      records.push(numericRecord(value));
    }
    results.append(records);
    const expected = [...samples].sort((a, b) => a - b)[
      Math.ceil(samples.length * 0.95) - 1
    ];
    const top = results.summary().operations.latency;
    assert.equal(top.length, 1);
    assert.equal(top[0].value, expected);
    assert.equal(top[0].samples, samples.length);
  }
});

test("p95 boundary counts, zeros and sparse groups do not invent durations", () => {
  for (const n of [1, 2, 19, 20, 21, 39, 40, 41, 100]) {
    const results = new Results();
    results.append(Array.from({ length: n }, (_, i) => numericRecord(i)));
    assert.equal(
      results.summary().operations.latency[0].value,
      Math.ceil(n * 0.95) - 1,
    );
  }
  const results = new Results();
  results.append([
    numericRecord(0),
    ...[-1, NaN, Infinity, null, undefined, "50"].map((value) =>
      numericRecord(value),
    ),
  ]);
  assert.equal(results.summary().operations.latency[0].value, 0);
  assert.equal(results.summary().operations.latency[0].samples, 1);
  const empty = new Results();
  empty.append([numericRecord(null, { level: "ERROR" })]);
  assert.equal(empty.summary().operations.latency.length, 0);
  assert.equal(empty.summary().operations.errors[0].value, 1);
});

test("top ten rankings cover full service/resource/operation tuples and deduplicate events", () => {
  const results = new Results(),
    records = [];
  for (let i = 0; i < 15; i++)
    for (let j = 0; j <= i; j++)
      records.push(
        numericRecord(150 - i, { operation: `op${i}`, level: "ERROR" }),
      );
  records.push(
    numericRecord(1000, {
      service: "billing",
      operation: "op14",
      level: "INFO",
    }),
  );
  records.push(
    numericRecord(900, { resource: "keys", operation: "op14", level: "FATAL" }),
  );
  records.push(
    numericRecord(800, {
      resource: "keys",
      operation: "op14",
      level: "CRITICAL",
    }),
  );
  results.append(records);
  results.append(records);
  const ranking = results.summary().operations;
  assert.equal(ranking.errors.length, 10);
  assert.equal(ranking.latency.length, 10);
  assert.equal(ranking.errors[0].value, 15);
  assert.equal(ranking.errors[0].operation, "op14");
  assert.equal(ranking.errors[9].value, 6);
  assert.equal(ranking.latency[0].service, "billing");
  assert.equal(ranking.latency[0].value, 1000);
  assert.equal(ranking.latency[1].resource, "keys");
  assert.equal(ranking.latency[1].samples, 2);
  assert.equal(results.events.length, records.length);
  assert.equal(results.summary().operations, ranking);
  results.setMatchers([]);
  assert.equal(results.summary().operations, ranking);
  assert.deepEqual(new Results().summary().operations, {
    errors: [],
    latency: [],
  });
});

test("grouping reconciles field and value IDs across dictionary versions and literal fallbacks", () => {
  const a = decoder(1, 1, 1),
    b = decoder(2, 6, 9),
    c = decoder(3, 6, 9, "billing");
  const results = new Results();
  results.append([
    a.read([0, identity(), 1, 1, 2, 5, 3, 1, 4, 1, 7, 20]),
    b.read([0, identity(), 6, 9, 2, 5, 3, 1, 4, 1, 7, 40]),
    b.read([
      0,
      identity(),
      6,
      "vault",
      2,
      5,
      3,
      "secrets",
      4,
      "decrypt",
      7,
      60,
    ]),
    c.read([0, identity(), 6, 9, 2, 5, 3, 1, 4, 1, 7, 100]),
  ]);
  assert.deepEqual(
    results.summary().operations.errors.map((row) => [row.service, row.value]),
    [
      ["vault", 3],
      ["billing", 1],
    ],
  );
  assert.deepEqual(
    results
      .summary()
      .operations.latency.map((row) => [row.service, row.value, row.samples]),
    [
      ["billing", 100, 1],
      ["vault", 60, 3],
    ],
  );
});

test("dimension keys cannot collide on delimiters or with missing values", () => {
  const results = new Results();
  results.append([
    numericRecord(1, { service: "a/b", resource: "c", operation: "d" }),
    numericRecord(2, { service: "a", resource: "b/c", operation: "d" }),
    numericRecord(3, { service: undefined }),
    numericRecord(4, { service: "Unspecified" }),
    numericRecord(5, { service: "" }),
  ]);
  assert.equal(results.summary().operations.latency.length, 5);
});

test("trie fields are read-only tags and stored rules do not evaluate them", () => {
  const reader = decoder(7, 1, 1);
  const record = reader.read([
    0,
    identity(),
    1,
    1,
    3,
    1,
    4,
    1,
    7,
    1500,
    8,
    "request failed",
  ]);
  const tags = eventFields(record);
  for (const name of ["service", "resource", "operation"])
    assert.equal(tags.find((tag) => tag.path[0] === name).fixed, true);
  for (const name of ["id", "time", "duration", "message"])
    assert.equal(tags.find((tag) => tag.path[0] === name).fixed, undefined);
  const rule = {
    id: "test",
    label: "Match",
    path: ["service"],
    mode: "text",
    pattern: "vault",
    ignoreCase: false,
  };
  assert.equal(compileIndicator(rule).matches(record), false);
  assert.equal(
    compileIndicator({
      ...rule,
      path: ["duration"],
      mode: "gte",
      pattern: "1500",
    }).matches(record),
    true,
  );
  assert.equal(
    compileIndicator({ ...rule, path: ["message"], pattern: "failed" }).matches(
      record,
    ),
    true,
  );
  assert.ok(eventId(record));
  const fallback = reader.read([0, identity(), 1, "vault"]);
  assert.equal(compileIndicator(rule).matches(fallback), false);
});
