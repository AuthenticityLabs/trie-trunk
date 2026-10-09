import assert from "node:assert/strict";
import { test } from "node:test";
import { Dictionaries, expandEvent, valueAt } from "./dictionary.ts";
import { compileIndicator, eventFields } from "./indicators.ts";
import { Results } from "./results.ts";
import { identity, eventOf } from "./fixtures.mjs";

function decoder(tap = "test", version = 42, levelKey = 3, label = "ERROR") {
  const reader = new Dictionaries();
  reader.install({
    negotiation: "2".repeat(64),
    schemas: [
      {
        tap,
        version,
        digest: `${version.toString(16).padStart(8, "0")}${tap}${label}`
          .split("")
          .map((c) => c.charCodeAt(0).toString(16))
          .join("")
          .padEnd(64, "0")
          .slice(0, 64),
        dictionary: {
          version,
          timezone: "UTC",
          fields: {
            0: { name: "id", encoding: "uuidv7" },
            [levelKey]: { name: "level", values: { 5: label } },
            4: { name: "service", values: { 2: "vault" } },
            12: { name: "duration" },
            16: {
              name: "error",
              object: { 1: { name: "code", values: { 9: "unauthorized" } } },
            },
            17: {
              name: "attempts",
              array: { 1: { name: "result", values: { 2: "failed" } } },
            },
          },
        },
      },
    ],
  });
  reader.read({ schema: 0, tap, version });
  return reader;
}

const base = 1790294400000;
const rule = {
  id: "error",
  label: "Failed",
  path: ["level"],
  mode: "regex",
  pattern: "^ERROR$",
  ignoreCase: false,
};

test("ingestion packs numeric values with shared field layouts and no display strings", (t) => {
  const reader = decoder();
  const nested = { 1: 9 };
  const wire = [0, identity(base), 3, 5, 16, nested];
  t.mock.method(Date.prototype, "toISOString", () =>
    assert.fail("No timestamp formatting on arrival"),
  );
  const first = reader.read(wire);
  const second = reader.read([0, identity(base + 1), 3, 5]);
  assert.notEqual(first.values, wire);
  assert.deepEqual(first.layout.keys, [0, 3, 16]);
  assert.deepEqual(first.values[2].values, [9]);
  assert.equal(first.dictionary, second.dictionary);
  const repeated = reader.read([0, identity(base + 2), 3, 5, 16, { 1: 9 }]);
  assert.equal(first.layout, repeated.layout);
  assert.equal(first.values[2], repeated.values[2]);
  assert.deepEqual(Object.keys(first), [
    "dictionary",
    "layout",
    "values",
    "uuid",
    "millis",
  ]);
  assert.equal(first.millis, base);
  assert.equal(second.millis, base + 1);
  assert.equal(first.values[1], 5);
  const results = new Results();
  results.append([first, second]);
  assert.equal(results.summary().errors, 2);
});

test("histogram matches linear counts across day and timestamp byte rollovers", () => {
  for (const first of [0, 255, 65530, base - 1000, 2 ** 40 - 1000]) {
    const reader = decoder();
    const records = Array.from({ length: 321 }, (_, i) =>
      reader.read([
        0,
        identity(first + Math.floor(i / 3) * 701),
        3,
        i % 5 ? 4 : 5,
      ]),
    );
    const results = new Results();
    results.append(records.slice(0, 100));
    results.append(records.slice(100, 300));
    results.append(records.slice(300));
    const actual = results.summary();
    const expected = Array(36).fill(0),
      errors = Array(36).fill(0);
    for (let i = 0; i < records.length; i++) {
      let bucket = 35;
      for (let b = 0; b < 35; b++) {
        if (
          records[i].millis <
          actual.first + ((actual.last - actual.first) * (b + 1)) / 36
        ) {
          bucket = b;
          break;
        }
      }
      expected[bucket]++;
      if (i % 5 === 0) errors[bucket]++;
    }
    assert.deepEqual(actual.buckets, expected);
    assert.deepEqual(actual.errorBuckets, errors);
    assert.equal(
      actual.buckets.reduce((a, b) => a + b),
      records.length,
    );
  }
});

test("equal timestamps share a bucket regardless of UUID suffix; empty summaries stay empty", () => {
  const reader = decoder();
  const results = new Results();
  assert.equal(
    results.summary().buckets.reduce((a, b) => a + b),
    0,
  );
  results.append([
    reader.read([0, identity(base, 2)]),
    reader.read([0, identity(base, 1)]),
  ]);
  assert.equal(results.summary().buckets[0], 2);
  assert.ok(results.events[0].uuid < results.events[1].uuid);
});

test("late batches merge in UUID order and deduplicate by tap across schema versions", () => {
  const a = decoder(),
    b = decoder("other"),
    revised = decoder("test", 43);
  const raw = identity(base);
  const first = a.read([0, raw, 3, 5]);
  const results = new Results();
  results.append([
    a.read([0, identity(base + 30)]),
    a.read([0, identity(base + 40)]),
  ]);
  results.append([
    a.read([0, identity(base + 20)]),
    first,
    a.read([0, identity(base + 10)]),
  ]);
  assert.deepEqual(
    results.events.map((e) => e.millis),
    [base, base + 10, base + 20, base + 30, base + 40],
  );
  assert.equal(results.append([first, revised.read([0, raw, 3, 5])]), 0);
  assert.equal(results.append([b.read([0, raw, 3, 5])]), 1);
  assert.equal(results.events.length, 6);
  assert.equal(results.summary().errors, 2);
});

test("numeric aggregation reconciles dictionary meanings across versions and handles literals", () => {
  const a = decoder(),
    b = decoder("test", 43, 9, "INFO");
  const results = new Results();
  results.append([
    a.read([0, identity(base), 3, 5, 4, 2, 12, 100]),
    b.read([0, identity(base + 1), 9, 5, 4, 2, 12, 300]),
    a.read([0, identity(base + 2), 3, "ERROR", 4, "vault"]),
  ]);
  assert.deepEqual(results.summary().levels, [
    ["ERROR", 2],
    ["INFO", 1],
  ]);
  assert.deepEqual(results.summary().services, [["vault", 3]]);
  assert.equal(results.summary().average, 200);
  assert.equal(results.summary().errors, 2);
});

test("indicator predicates do not revisit old events on append or summary", () => {
  let evaluated = 0;
  const results = new Results();
  results.setMatchers([
    {
      indicator: rule,
      matches: (event) => {
        evaluated++;
        return valueAt(event, "level") === "ERROR";
      },
    },
  ]);
  const a = eventOf({ level: "ERROR" }),
    b = eventOf({ level: "INFO" });
  results.append([a]);
  results.summary();
  results.summary();
  results.append([b, a]);
  results.summary();
  assert.equal(evaluated, 2);
  assert.equal(results.annotations.size, 1);
  results.setMatchers([]);
  assert.equal(results.annotations.size, 0);
  assert.equal(results.summary().signals.length, 0);
});

test("enum predicates evaluate the label once per schema, then match numeric keys", (t) => {
  const reader = decoder();
  const events = [
    reader.read([0, identity(base), 3, 5]),
    reader.read([0, identity(base + 1), 3, 5]),
  ];
  const original = RegExp.prototype.test;
  let evaluations = 0;
  t.mock.method(RegExp.prototype, "test", function (value) {
    evaluations++;
    return original.call(this, value);
  });
  const matcher = compileIndicator(rule);
  assert.equal(matcher.matches(events[0]), true);
  assert.equal(matcher.matches(events[1]), true);
  assert.equal(evaluations, 1);
});

test("nested dictionary tags and predicates agree with explicit single-event expansion", () => {
  const reader = decoder();
  const record = reader.read([
    0,
    identity(base),
    3,
    5,
    16,
    { 1: 9 },
    17,
    [{ 1: 2 }],
  ]);
  assert.equal(expandEvent(record).error.code, "unauthorized");
  assert.deepEqual(eventFields(record).slice(3), [
    { path: ["error", "code"], text: "unauthorized" },
    { path: ["attempts", "0", "result"], text: "failed" },
  ]);
  for (const [path, pattern] of [
    [["error", "code"], "unauthorized"],
    [["attempts", "0", "result"], "failed"],
  ]) {
    assert.equal(
      compileIndicator({ ...rule, path, pattern }).matches(record),
      true,
    );
  }
  assert.equal(
    compileIndicator({ ...rule, path: ["error", "1"], pattern: "9" }).matches(
      record,
    ),
    false,
  );
  assert.equal(
    compileIndicator({
      ...rule,
      path: ["attempts", "0", "1"],
      pattern: "2",
    }).matches(record),
    false,
  );
});

test("same-millisecond UUID ordering follows binary bytes across base64 alphabet boundaries", () => {
  const reader = decoder();
  const rows = Array.from({ length: 512 }, (_, i) =>
    reader.read([0, identity(base, i)]),
  );
  const expected = [...rows].sort((a, b) =>
    Buffer.compare(
      Buffer.from(a.uuid, "base64"),
      Buffer.from(b.uuid, "base64"),
    ),
  );
  const results = new Results();
  results.append(rows.filter((_, i) => i % 2).reverse());
  results.append(rows.filter((_, i) => i % 2 === 0).reverse());
  assert.deepEqual(results.events, expected);
});

test("cached field layouts preserve record order and reject invalid keys", () => {
  const reader = decoder();
  const first = reader.read([0, identity(base), 3, 5]);
  const reordered = reader.read([3, 5, 0, identity(base + 1)]);
  const reused = reader.read([0, identity(base + 2), 3, 5]);
  assert.deepEqual(reordered.layout.keys, [3, 0]);
  assert.equal(first.layout, reused.layout);
  assert.throws(
    () => reader.read(["0", identity(base), 3, 5]),
    /Invalid dictionary key/,
  );
  assert.throws(
    () => reader.read([0, identity(base), 0, identity(base)]),
    /Duplicate/,
  );
  reader.read([0, identity(base), 16, { 1: 9 }]);
  assert.throws(
    () => reader.read([0, identity(base), 16, { "01": 9 }]),
    /Invalid dictionary key/,
  );
});
