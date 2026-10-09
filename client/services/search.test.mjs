import assert from "node:assert/strict";
import { test } from "node:test";
import {
  collectionWindow,
  compileSearch,
  describeSearch,
  where,
} from "./search.ts";
import { Dictionaries } from "./dictionary.ts";
import { eventOf, identity } from "./fixtures.mjs";

const selection = (parameters = {}, extra = {}) => ({
  parameters,
  tap: "",
  from: "",
  to: "",
  ...extra,
});

test("structured search requires each selected field without matching missing or differently typed values", () => {
  const predicate = compileSearch(
    selection({ service: "vault", level: "ERROR" }),
  );
  assert.equal(predicate(eventOf({ service: "vault", level: "ERROR" })), true);
  for (const values of [
    { service: "vault", level: "INFO" },
    { service: "billing", level: "ERROR" },
    { service: "vault" },
    { service: null, level: "ERROR" },
    { service: { name: "vault" }, level: "ERROR" },
  ])
    assert.equal(predicate(eventOf(values)), false);
  assert.equal(
    compileSearch(selection({ service: "1" }))(eventOf({ service: 1 })),
    false,
  );
  assert.equal(compileSearch(selection({ service: "" })), undefined);
});

test("selectors resolve dictionary labels across schemas and preserve literal fallback", () => {
  const predicate = compileSearch(selection({ service: "vault" }));
  for (const [version, id] of [
    [61, 1],
    [62, 99],
  ]) {
    const dictionaries = new Dictionaries();
    dictionaries.install({
      negotiation: "a".repeat(64),
      schemas: [
        {
          tap: "test",
          version,
          digest: String(version).padStart(64, "0"),
          dictionary: {
            version,
            timezone: "UTC",
            fields: {
              0: { name: "id", encoding: "uuidv7" },
              1: {
                name: "service",
                values: {
                  [id]: "vault",
                  [id + 1]: "billing",
                  [id + 2]: "vault",
                },
              },
            },
            path: [1],
          },
        },
      ],
    });
    dictionaries.read({ schema: 0, tap: "test", version });
    for (const value of [id, id + 2, "vault"])
      assert.equal(
        predicate(dictionaries.read([0, identity(), 1, value])),
        true,
      );
    for (const value of [id + 1, "billing", "VAULT", null])
      assert.equal(
        predicate(dictionaries.read([0, identity(), 1, value])),
        false,
      );
  }
});

test("local tap and UTC bounds use UUID time with an exclusive end", () => {
  const query = selection(
    {},
    { tap: "one", from: "2026-09-25T00:00", to: "2026-09-25T01:00" },
  );
  const predicate = compileSearch(query);
  assert.equal(
    predicate(eventOf({ time: "2026-09-25T00:00:00Z" }, "one")),
    true,
  );
  assert.equal(
    predicate(eventOf({ time: "2026-09-25T01:00:00Z" }, "one")),
    false,
  );
  assert.equal(
    predicate(eventOf({ time: "2026-09-25T00:00:00Z" }, "two")),
    false,
  );
  assert.throws(() => compileSearch({ ...query, to: query.from }));
  assert.throws(() => compileSearch({ ...query, from: "invalid" }));
});

test("collection selections become one server constraint and strings remain literal", () => {
  assert.deepEqual(where({ service: "vault", level: "ERROR", resource: "" }), [
    'level=="ERROR" service=="vault"',
  ]);
  assert.deepEqual(where({ service: "" }), []);
  const value = 'vault" operation=="delete';
  assert.deepEqual(where({ service: value }), [
    'service=="vault\\\" operation==\\\"delete"',
  ]);
  const predicate = compileSearch(selection({ service: value }));
  assert.equal(predicate(eventOf({ service: value })), true);
  assert.equal(
    predicate(eventOf({ service: "vault", operation: "delete" })),
    false,
  );
  assert.equal(
    describeSearch(selection({ service: "vault", level: "ERROR" })),
    "Level: ERROR · Service: vault",
  );
});

test("collection window uses the full sorted collection and includes the last millisecond", () => {
  const empty = collectionWindow([]);
  assert.equal(empty, undefined);
  const events = [
    eventOf({ time: "2026-09-20T14:00:00.123Z" }),
    eventOf({ time: "2026-09-26T18:45:00.999Z" }),
  ];
  const window = collectionWindow(events);
  assert.deepEqual(window, {
    from: "2026-09-20T14:00:00.123",
    to: "2026-09-26T18:45:01.000",
  });
  const predicate = compileSearch(selection({}, window));
  assert.ok(events.every(predicate));
  assert.equal(predicate(eventOf({ time: "2026-09-26T18:45:01.000Z" })), false);
});

test("a single timestamp still has a valid exclusive search window across midnight", () => {
  const event = eventOf({ time: "2026-09-26T23:59:59.999Z" });
  const window = collectionWindow([event]);
  assert.deepEqual(window, {
    from: "2026-09-26T23:59:59.999",
    to: "2026-09-27T00:00:00.000",
  });
  assert.equal(compileSearch(selection({}, window))(event), true);
});
