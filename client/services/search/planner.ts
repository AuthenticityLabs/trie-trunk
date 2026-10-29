import { createAbortScope } from '../abort.ts';
import type { TrunkBootstrap } from '../pil/trunk.ts';
import type { Metadata } from '../pil/metadata.ts';
import type { Archive } from '../store/protocol.ts';
import type { TimeReceipt } from '../store/native.ts';
import type { NativeCollectOptions } from '../pil/native-collect.ts';
import { branchTargets, loadTimeWindow } from '../pil/time-window.ts';
import { HOUR, TIME_INDEX, TIME_STATE, TIME_SUM, TIME_INVERTED, TIME_SODS } from '../store/time-trie.ts';
export type SearchHour = {
  time: number;
  count: number;
  summary: boolean;
  index: boolean;
  state: boolean;
  inverted: boolean;
  events: boolean;
};
export type SearchBranch = {
  tap: string;
  definition: string;
  metadata: Metadata;
  indexPath: string;
  podsPath: string;
  hours: SearchHour[];
  receipt: TimeReceipt;
};
export type PackRequest = {
  path: string;
  tap: string;
  definition: string;
  from: number;
  to: number;
};
export type SearchPlan = {
  from: number;
  to: number;
  branches: SearchBranch[];
  matching: 'client';
  packRequests: PackRequest[];
};

/** Bounded tasks share cancellation and settle before a failure is returned. */
export async function searchTasks<T, R>(items: readonly T[], signal: AbortSignal, work: (item: T, index: number, signal: AbortSignal) => Promise<R>): Promise<R[]> {
  const failed = createAbortScope([signal]),
    combined = failed.signal;
  const out = new Array<R>(items.length);
  let next = 0;
  const tasks = Array.from({
    length: Math.min(4, items.length)
  }, async () => {
    for (;;) {
      combined.throwIfAborted();
      const i = next++;
      if (i >= items.length) return;
      out[i] = await work(items[i], i, combined);
    }
  });
  try {
    await Promise.all(tasks);
    combined.throwIfAborted();
    return out;
  } catch (error) {
    failed.abort(error);
    await Promise.allSettled(tasks);
    throw error;
  } finally {
    failed.dispose();
  }
}

/** Collect whole hours before local matching. Completion bits control transfer,
 * never whether a record satisfies the user's predicates. */
export async function planSearch(trunk: TrunkBootstrap, scope: string, from: number, to: number, token: string, archive: Archive, signal: AbortSignal = new AbortController().signal): Promise<SearchPlan> {
  if (!Number.isSafeInteger(from) || !Number.isSafeInteger(to) || from >= to) throw Error('Invalid search window');
  from = Math.floor(from / HOUR) * HOUR;
  to = Math.ceil(to / HOUR) * HOUR;
  const files = new Map(trunk.catalog.files.map(f => [f.path, f]));
  const branches = await searchTasks(branchTargets(trunk, scope, true), signal, async ({
    file,
    tap,
    definition
  }) => {
    const podsPath = file.path.slice(0, -3) + 'pods',
      pods = files.get(podsPath);
    if (pods?.kind !== 'pods' || pods.definition !== definition.digest) throw Error('Missing catalog pods artifact');
    const receipt = (await archive(token, {
      kind: 'time-read',
      query: {
        tap,
        digest: definition.digest,
        from,
        to
      }
    })).time;
    if (!receipt) throw Error('Missing time trie receipt');
    const hours = receipt.rows.map(row => {
      const has = (bit: number) => (row.flags & bit) === bit;
      return {
        time: row.time,
        count: row.count,
        summary: has(TIME_SUM),
        index: has(TIME_INDEX),
        state: has(TIME_STATE),
        inverted: has(TIME_INVERTED),
        events: has(TIME_SODS)
      };
    });
    return {
      tap,
      definition: definition.digest,
      metadata: definition,
      indexPath: file.path.slice(0, -3) + 'idx',
      podsPath,
      hours,
      receipt
    };
  });
  const plan: SearchPlan = {
    from,
    to,
    branches,
    matching: 'client',
    packRequests: []
  };
  for (const branch of branches) {
    let gap: PackRequest | undefined;
    for (const hour of branch.hours) {
      if (hour.count && !hour.events) {
        if (gap && gap.to === hour.time) gap.to += HOUR;else {
          gap = {
            path: branch.podsPath,
            tap: branch.tap,
            definition: branch.definition,
            from: hour.time,
            to: hour.time + HOUR
          };
          plan.packRequests.push(gap);
        }
      } else gap = undefined;
    }
  }
  return plan;
}
export async function prepareSearch(trunk: TrunkBootstrap, scope: string, from: number, to: number, options: Pick<NativeCollectOptions, 'token' | 'signal' | 'codec' | 'definitions' | 'archive' | 'fetcher'>): Promise<SearchPlan> {
  const {
    token,
    signal,
    archive
  } = options;
  await loadTimeWindow({
    trunk,
    scope,
    from,
    to,
    diagnostics: true
  }, token, signal, archive, options.fetcher);
  signal.throwIfAborted();
  return planSearch(trunk, scope, from, to, token, archive, signal);
}

/** First results need one hour, not an entire day. Remaining work batches at
 * most a day, skipping gaps and retaining the requested chronological order. */
export function searchWindows(plan: SearchPlan, from: number, to: number, descending: boolean): {
  from: number;
  to: number;
}[] {
  const hours = [...new Set(plan.branches.flatMap(b => b.hours.filter(h => h.count && h.time < to && h.time + HOUR > from).map(h => h.time)))].sort((a, b) => a - b);
  if (descending) hours.reverse();
  const windows: {
    from: number;
    to: number;
  }[] = [];
  for (const hour of hours) {
    const a = Math.max(hour, from),
      z = Math.min(hour + HOUR, to),
      last = windows.at(-1);
    if (windows.length > 1 && last && Math.floor(hour / (24 * HOUR)) === Math.floor(last.from / (24 * HOUR))) {
      last.from = Math.min(last.from, a);
      last.to = Math.max(last.to, z);
    } else windows.push({
      from: a,
      to: z
    });
  }
  return windows;
}
