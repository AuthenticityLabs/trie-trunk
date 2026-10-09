import assert from "node:assert/strict";
import { test } from "node:test";
import { dateWindow, timeWindow, dayCoverage } from "./calendar.ts";
const now = Date.parse("2026-09-27T12:30:00.000Z");

test("inclusive calendar dates round trip the exclusive millisecond bound", () => {
  const from = "2026-09-20T14:00:00.123", to = "2026-09-27T00:00:00.000";
  const value = dateWindow(from, to, now);
  assert.deepEqual(value, {dates: ["2026-09-20", "2026-09-26"], startTime: "14:00:00.123", endTime: "23:59:59.999"});
  assert.deepEqual(timeWindow(value.dates, value.startTime, value.endTime, now), {from, to});
  const one = dateWindow("2026-09-20T14:00:00.123", "2026-09-20T14:00:00.124", now);
  assert.deepEqual(timeWindow(one.dates, one.startTime, one.endTime, now), {from, to: "2026-09-20T14:00:00.124"});
});

test("whole days cross month boundaries in UTC and today stops at now", () => {
  assert.deepEqual(timeWindow(["2026-08-31", "2026-09-02"], "00:00", "23:59:59.999", now), {from:"2026-08-31T00:00:00.000", to:"2026-09-03T00:00:00.000"});
  assert.equal(timeWindow(["2026-09-26", "2026-09-27"], "00:00", "23:59:59.999", now).to, "2026-09-27T12:30:00.000");
  assert.throws(() => timeWindow(["2026-09-25"], "00:00", "23:59:59", now));
  assert.throws(() => timeWindow(["2026-09-25", "2026-09-24"], "00:00", "23:59:59", now));
  assert.throws(() => timeWindow(["2026-09-25", "2026-09-25"], "invalid", "23:59", now));
});

test("day shading distinguishes full, partial and uncollected days", () => {
  const ranges = [{from: Date.parse("2026-09-20T14:00:00Z"), to: Date.parse("2026-09-23T00:00:00Z")}];
  assert.equal(dayCoverage("2026-09-19", ranges), undefined);
  assert.equal(dayCoverage("2026-09-20", ranges), "partial");
  assert.equal(dayCoverage("2026-09-21", ranges), "full");
  assert.equal(dayCoverage("2026-09-22", ranges), "full");
  assert.equal(dayCoverage("2026-09-23", ranges), undefined);
});
