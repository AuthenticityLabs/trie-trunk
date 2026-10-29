import assert from 'node:assert/strict';
import { getEventListeners } from 'node:events';
import { test } from 'node:test';
import { createAbortScope } from './abort.ts';
import { searchTasks } from './search/planner.ts';

const waitForAbort = signal => new Promise((_, reject) => {
  if (signal.aborted) reject(signal.reason);
  else signal.addEventListener('abort', () => reject(signal.reason), { once: true });
});

test('cancellation works with both static AbortSignal APIs absent', async () => {
  const any = Object.getOwnPropertyDescriptor(AbortSignal, 'any');
  const timeout = Object.getOwnPropertyDescriptor(AbortSignal, 'timeout');
  Object.defineProperty(AbortSignal, 'any', { configurable: true, value: undefined });
  Object.defineProperty(AbortSignal, 'timeout', { configurable: true, value: undefined });
  try {
    const parent = new AbortController();
    const scope = createAbortScope([parent.signal], 30000);
    const reason = new Error('Search canceled');
    parent.abort(reason);
    assert.equal(scope.signal.reason, reason);
    assert.equal(getEventListeners(parent.signal, 'abort').length, 0);
    assert.deepEqual(await searchTasks([1, 2], new AbortController().signal, async n => n * 2), [2, 4]);
    scope.dispose();
  } finally {
    Object.defineProperty(AbortSignal, 'any', any);
    Object.defineProperty(AbortSignal, 'timeout', timeout);
  }
});

test('pre-aborted inputs preserve the first reason and detach earlier listeners', () => {
  const first = new AbortController(), second = new AbortController();
  const reason = new DOMException('Stopped', 'AbortError');
  second.abort(reason);
  const scope = createAbortScope([first.signal, second.signal], 30000);
  assert.equal(scope.signal.reason, reason);
  assert.equal(getEventListeners(first.signal, 'abort').length, 0);
  scope.dispose();
});

test('normal completion removes listeners and cancels the timeout', t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const parent = new AbortController();
  const scope = createAbortScope([parent.signal, parent.signal], 30000);
  assert.equal(getEventListeners(parent.signal, 'abort').length, 1);
  scope.dispose();
  scope.dispose();
  assert.equal(getEventListeners(parent.signal, 'abort').length, 0);
  t.mock.timers.tick(30001);
  parent.abort();
  assert.equal(scope.signal.aborted, false);
});

test('timeout aborts with TimeoutError and releases parent listeners', t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const parent = new AbortController();
  const scope = createAbortScope([parent.signal], 30000);
  t.mock.timers.tick(29999);
  assert.equal(scope.signal.aborted, false);
  t.mock.timers.tick(1);
  assert.equal(scope.signal.reason.name, 'TimeoutError');
  assert.equal(getEventListeners(parent.signal, 'abort').length, 0);
});

test('a failed search task cancels and settles siblings before returning', async () => {
  const parent = new AbortController(), reason = new Error('Collection failed');
  let finished = 0;
  await assert.rejects(searchTasks([0, 1, 2], parent.signal, async (n, _i, signal) => {
    try {
      if (n === 0) throw reason;
      await waitForAbort(signal);
    } finally { finished++; }
  }), error => error === reason);
  assert.equal(finished, 3);
  assert.equal(getEventListeners(parent.signal, 'abort').length, 0);
});
