import assert from "node:assert/strict";
import { test } from "node:test";
import { WorkingSet } from "./working-set.ts";
import { compileSearch } from "./search.ts";
import { valueAt } from "./dictionary.ts";
import { eventOf } from "./fixtures.mjs";

const immediate = async () => {};

test("search preserves compact working set and clear restores its original summaries", async () => {
  const rows = [
    eventOf({ service: "vault", level: "ERROR", duration: 1000 }),
    eventOf({ service: "billing", level: "INFO", duration: 2000 }),
  ];
  const workspace = new WorkingSet();
  workspace.append([...rows, rows[0]]);
  const all = workspace.collected;
  const summary = all.summary();
  await workspace.search(
    compileSearch({
      parameters: { service: "vault" },
      tap: "",
      from: "",
      to: "",
    }),
    immediate,
  );
  assert.equal(workspace.collected, all);
  assert.equal(all.events.length, 2);
  assert.deepEqual(workspace.visible.events, [rows[0]]);
  assert.equal(workspace.visible.summary().errors, 1);
  assert.deepEqual(workspace.choices().service, ["billing", "vault"]);
  await workspace.search(undefined, immediate);
  assert.equal(workspace.visible, all);
  assert.deepEqual(workspace.visible.summary(), summary);
});

test("follow arrivals and duplicates only add matching events to search view", async () => {
  const workspace = new WorkingSet();
  const first = eventOf({ duration: 10 });
  workspace.append([first]);
  await workspace.search(
    (event) => valueAt(event, "duration") >= 100,
    immediate,
  );
  const matching = eventOf({ duration: 100 }),
    other = eventOf({ duration: 99 });
  workspace.append([matching, matching, first, other]);
  assert.equal(workspace.collected.events.length, 3);
  assert.deepEqual(workspace.visible.events, [matching]);
});

test("late arrivals during chunked searching cannot be skipped or counted twice", async () => {
  const rows = Array.from({ length: 4097 }, (_, i) =>
    eventOf({ duration: i, time: "2026-09-25T00:05:00Z" }),
  );
  const workspace = new WorkingSet();
  workspace.append(rows);
  const late = eventOf({ duration: 5000, time: "2026-09-25T00:00:00Z" });
  let yields = 0;
  await workspace.search(
    (event) => valueAt(event, "duration") >= 4000,
    async () => {
      if (!yields++) workspace.append([late, rows[4000]]);
    },
  );
  assert.equal(workspace.collected.events.length, 4098);
  assert.equal(workspace.visible.events.length, 98);
  assert.equal(workspace.visible.events[0], late);
  assert.ok(yields >= 3);
});

test("clearing or replacing a search cancels stale work without changing current view", async () => {
  const workspace = new WorkingSet();
  workspace.append(
    Array.from({ length: 2050 }, (_, i) => eventOf({ duration: i })),
  );
  let release;
  const pending = workspace.search(
    (event) => valueAt(event, "duration") >= 1000,
    () => new Promise((resolve) => (release = resolve)),
  );
  await workspace.search(undefined);
  release();
  assert.equal(await pending, false);
  assert.equal(workspace.visible, workspace.collected);
  const pending2 = workspace.search(
    (event) => valueAt(event, "duration") >= 2000,
    () => new Promise((resolve) => (release = resolve)),
  );
  await workspace.search((event) => valueAt(event, "duration") <= 2, immediate);
  release();
  assert.equal(await pending2, false);
  assert.equal(workspace.visible.events.length, 3);
});

test("new collection applies the retained active predicate to its first arrivals", () => {
  const workspace = new WorkingSet(
    compileSearch({
      parameters: { level: "ERROR" },
      tap: "",
      from: "",
      to: "",
    }),
  );
  workspace.append([eventOf({ level: "INFO" }), eventOf({ level: "ERROR" })]);
  assert.equal(workspace.collected.events.length, 2);
  assert.equal(workspace.visible.events.length, 1);
});

test("duplicates cannot replace the first collected event in a search view", async () => {
  const workspace = new WorkingSet(
    (event) => valueAt(event, "duration") >= 100,
  );
  const first = eventOf({ duration: 1 });
  const duplicate = { ...eventOf({ duration: 999 }), uuid: first.uuid };
  workspace.append([first, duplicate]);
  assert.equal(workspace.collected.events.length, 1);
  assert.equal(workspace.visible.events.length, 0);
  workspace.append([duplicate, eventOf({ duration: 100 })]);
  assert.equal(workspace.visible.events.length, 1);
});
