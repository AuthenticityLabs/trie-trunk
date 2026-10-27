import type { ViewQuery } from './pil/view.ts';

export type SummaryProgress = { completed: number; total: number; packets: number };
export type SummaryWindow = { from: string; to: string };
const day = 86_400_000;

/** UTC day boundaries reuse canonical packets. Newest weeks are collected first. */
export function summaryWindows(now: number, recent = false): SummaryWindow[] {
  if (!Number.isSafeInteger(now) || now <= 0) throw Error('Invalid summary time');
  const year = Date.UTC(new Date(now).getUTCFullYear(), 0, 1);
  const start = recent ? Math.max(year, Math.floor(now / day) * day - day) : year;
  const windows: SummaryWindow[] = [];
  let end = now;
  while (end > start) {
    const begin = Math.max(start, Math.floor((end - 1) / day) * day - 6 * day);
    windows.push({ from: new Date(begin).toISOString(), to: new Date(end).toISOString() });
    end = begin;
  }
  return windows;
}
export function summaryQuery(service: string, window: SummaryWindow): ViewQuery {
  if (!/^[a-z][a-z0-9._-]{0,127}\/[a-z][a-z0-9._-]{0,127}$/.test(service)) throw Error('Invalid service subscription');
  return { tap: `${service}/*/*`, ...window, include: 'summary', size: 1 };
}

type Listener = (progress: SummaryProgress) => void;
type Job = { controller: AbortController; listeners: Set<Listener>; progress: SummaryProgress; done: Promise<void> };
/** Shared-worker queue: one priming window at a time, shared by subscribing tabs. */
export class SummaryReplication {
  private jobs = new Map<string, Job>();
  private tail: Promise<unknown> = Promise.resolve();
  private load: (query: ViewQuery, signal: AbortSignal) => Promise<number>;
  constructor(load: (query: ViewQuery, signal: AbortSignal) => Promise<number>) { this.load = load; }
  async collect(service: string, now: number, recent: boolean, signal: AbortSignal, progress: Listener): Promise<void> {
    signal.throwIfAborted();
    const windows = summaryWindows(now, recent);
    // A common minute boundary lets simultaneous tabs share a single job.
    const key = JSON.stringify([service, Math.floor(now / 60_000), recent]);
    let job = this.jobs.get(key);
    if (!job) {
      const controller = new AbortController();
      job = { controller, listeners: new Set(), progress: { completed: 0, total: windows.length, packets: 0 }, done: Promise.resolve() };
      const entry = job;
      entry.done = (async () => {
        for (const window of windows) {
          const run = this.tail.catch(() => {}).then(async () => {
            controller.signal.throwIfAborted();
            return this.load(summaryQuery(service, window), controller.signal);
          });
          this.tail = run;
          const packets = await run;
          entry.progress = { completed: entry.progress.completed + 1, total: windows.length, packets: entry.progress.packets + packets };
          for (const notify of entry.listeners) notify(entry.progress);
        }
      })().finally(() => { if (this.jobs.get(key) === entry) this.jobs.delete(key); });
      this.jobs.set(key, job);
    }
    const entry = job;
    entry.listeners.add(progress); progress(entry.progress);
    let aborted!: () => void;
    const cancelled = new Promise<never>((_, reject) => {
      aborted = () => reject(signal.reason ?? new DOMException('Cancelled', 'AbortError'));
      signal.addEventListener('abort', aborted, { once: true });
    });
    try { await Promise.race([entry.done, cancelled]); }
    finally {
      signal.removeEventListener('abort', aborted);
      entry.listeners.delete(progress);
      if (!entry.listeners.size) {
        entry.controller.abort();
        if (this.jobs.get(key) === entry) this.jobs.delete(key);
      }
    }
  }
}
