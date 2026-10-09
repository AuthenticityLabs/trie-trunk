import assert from "node:assert/strict";
import { test } from "node:test";
import { Coverage } from "./coverage.ts";

test("expansion fetches only missing portions across completed ranges", () => {
  const coverage = new Coverage();
  assert.deepEqual(coverage.missing({ from: 0, to: 100 }), [
    { from: 0, to: 100 },
  ]);
  coverage.add({ from: 20, to: 40 });
  coverage.add({ from: 60, to: 80 });
  assert.deepEqual(coverage.missing({ from: 0, to: 100 }), [
    { from: 0, to: 20 },
    { from: 40, to: 60 },
    { from: 80, to: 100 },
  ]);
  assert.deepEqual(coverage.missing({ from: 25, to: 30 }), []);
  for (const gap of coverage.missing({ from: 0, to: 100 })) coverage.add(gap);
  assert.deepEqual(coverage.missing({ from: 0, to: 100 }), []);
  assert.deepEqual(coverage.missing({ from: 90, to: 120 }), [
    { from: 100, to: 120 },
  ]);
});

test("completed empty requests count as coverage, and adjacent and overlapping ranges merge", () => {
  const coverage = new Coverage();
  for (const range of [
    { from: 30, to: 50 },
    { from: 0, to: 20 },
    { from: 10, to: 30 },
    { from: 12, to: 18 },
    { from: 50, to: 50 },
  ])
    coverage.add(range);
  assert.deepEqual(coverage.missing({ from: 0, to: 50 }), []);
  assert.deepEqual(coverage.missing({ from: 50, to: 51 }), [
    { from: 50, to: 51 },
  ]);
  assert.deepEqual(coverage.missing({ from: 20, to: 20 }), []);
  assert.deepEqual(coverage.missing({ from: 30, to: 20 }), []);
});

test("missing ranges agree with a per-millisecond reference for mixed insertions", () => {
  const coverage = new Coverage(),
    known = new Set();
  for (let step = 0; step < 40; step++) {
    const from = (step * 23) % 120,
      to = from + (step % 19);
    coverage.add({ from, to });
    for (let i = from; i < to; i++) known.add(i);
    for (let lower = 0; lower < 140; lower += 7) {
      const upper = Math.min(140, lower + 37);
      const missing = coverage.missing({ from: lower, to: upper });
      const actual = missing.flatMap(({ from, to }) =>
        Array.from({ length: to - from }, (_, i) => from + i),
      );
      const expected = Array.from(
        { length: upper - lower },
        (_, i) => lower + i,
      ).filter((i) => !known.has(i));
      assert.deepEqual(actual, expected);
    }
  }
});
