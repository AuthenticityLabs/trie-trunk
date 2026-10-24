import assert from 'node:assert/strict';
import { test } from 'node:test';
import { ChartProjection, validWindow, brushWindow } from './charts.ts';
import { eventOf } from './fixtures.mjs';
import { fixture } from './pil-fixtures.mjs';
import { compile } from './pil/metadata.ts';
import { PILEvent } from './pil/event.ts';

const start = Date.parse('2026-09-25T00:00:00Z');
const row = (offset, fields = {}) => eventOf({ time: new Date(start + offset).toISOString(), ...fields });

test('chart projection observes exclusive end, error count, duration units and empty buckets', () => {
  const events = [row(0, { level: 'ERROR', duration: 1000 }), row(1, { level: 'INFO', duration: 3000 }),
    row(20, { level: 'ERROR', duration: null }), row(30, { level: 'ERROR', duration: 999999 })];
  const result = new ChartProjection().query(events, 0, { from: start, to: start + 30, points: 3 });
  assert.deepEqual(result.time, [start, start + 10, start + 20]);
  assert.deepEqual(result.events, [2, 0, 1]);
  assert.deepEqual(result.errorRate, [50, null, 100]);
  assert.deepEqual(result.latency, [2, null, null]);
  assert.equal(result.count, 3);
});

test('projection directly reads PIL dictionary codes and leaves other fields undecoded', () => {
  const f = fixture(), event = new PILEvent(f.row(), compile(f, 'test'));
  const allowed = new Set(['level', 'duration'].map(name => event.fields.names.get(name).key));
  const read = event.read.bind(event);
  let reads = 0;
  event.read = key => { assert.ok(allowed.has(key)); reads++; return read(key); };
  const projection = new ChartProjection();
  const window = { from: event.millis, to: event.millis + 1, points: 300 };
  const first = projection.query([event], 1, window);
  assert.deepEqual(first.events, [1]);
  assert.deepEqual(first.errorRate, [100]);
  assert.deepEqual(first.latency, [2.237]);
  assert.equal(projection.query([event], 1, window), first);
  assert.equal(reads, 2, 'three panels can reuse the same projection');
  projection.query([event], 2, window);
  assert.equal(reads, 4, 'new view revision invalidates the cache');
  assert.deepEqual(event.encoded(), f.row());
});

test('empty or out-of-collection windows cannot invent events; projections stay bounded', () => {
  const projection = new ChartProjection();
  const events = [row(10), row(1000)];
  for (const [from, to] of [[0, 10], [1001, 1100]]) {
    const result = projection.query(events, 0, { from: start + from, to: start + to, points: 600 });
    assert.deepEqual(result.events, []);
    assert.equal(result.count, 0);
  }
  assert.deepEqual(projection.query([], 1, { from: start, to: start + 1, points: 1 }).events, []);
  const result = projection.query(events, 2, { from: start, to: start + 2000, points: 600 });
  assert.ok(result.time.length <= 600);
  assert.equal(result.events.reduce((sum, n) => sum + n, 0), 2);
  assert.deepEqual(result.latency.filter(v => v !== null), []);
});

test('chart windows reject invalid ranges and unbounded projections', () => {
  const window = { from: start, to: start + 100, points: 300 };
  for (const override of [{ from: NaN }, { from: -1 }, { to: Infinity }, { to: start },
    { to: 9e15 }, { points: 601 }, { points: 0 }, { points: 1.5 }]) {
    assert.equal(validWindow({ ...window, ...override }), false);
  }
  assert.ok(validWindow(window));
});

test('histogram brush maps fractional positions to inclusive start and exclusive end', () => {
  assert.deepEqual(brushWindow([0, 36], start, start + 3600, 36), [start, start + 3601]);
  assert.deepEqual(brushWindow([9, 18], start, start + 3600, 36), [start + 900, start + 1800]);
  assert.deepEqual(brushWindow([18, 9], start, start + 3600, 36), [start + 900, start + 1800]);
  assert.deepEqual(brushWindow([-2, 40], start, start + 3600, 36), [start, start + 3601]);
  assert.deepEqual(brushWindow([0.1, 0.2], start, start + 3600, 36), [start + 10, start + 20]);
  assert.deepEqual(brushWindow([5, 20], start, start, 36), [start, start + 1]);
  for (const selection of [undefined, [], [1, 1], [NaN, 4], [37, 38], [[0, 2], [1, 3]]]) {
    assert.equal(brushWindow(selection, start, start + 3600, 36), undefined);
  }
  assert.equal(brushWindow([0, 36], Infinity, -Infinity, 36), undefined);
});
