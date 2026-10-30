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
  discoveryMs?: number;
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
export async function planSearch(trunk: TrunkBootstrap, scope: string, from: number, to: number, token: string, archive: Archive, signal: AbortSignal = new AbortController().signal, discovered?: TimeReceipt[]): Promise<SearchPlan> {
  if (!Number.isSafeInteger(from) || !Number.isSafeInteger(to) || from >= to) throw Error('Invalid search window');
  from = Math.floor(from / HOUR) * HOUR;
  to = Math.ceil(to / HOUR) * HOUR;
  const files = new Map(trunk.catalog.files.map(f => [f.path, f]));
  signal.throwIfAborted();
  const targets=branchTargets(trunk,scope,true);
  for(const {file,definition} of targets){const pods=files.get(file.path.slice(0,-3)+'pods');if(pods?.kind!=='pods'||pods.definition!==definition.digest)throw Error('Missing catalog pods artifact');}
  const receipts=discovered??(targets.length?(await archive(token,{kind:'time-read-many',queries:targets.map(t=>({tap:t.tap,digest:t.definition.digest,from,to}))})).times:[]);
  if(receipts?.length!==targets.length)throw Error('Missing time trie receipts');
  signal.throwIfAborted();
  const branches = targets.map(({
    file,
    tap,
    definition
  },i) => {
    const podsPath = file.path.slice(0, -3) + 'pods',
      pods = files.get(podsPath);
    if (pods?.kind !== 'pods' || pods.definition !== definition.digest) throw Error('Missing catalog pods artifact');
    const receipt = receipts[i];
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
    let gap: PackRequest | undefined, previousTime:number|undefined;
    for (const hour of branch.hours) {
      if (hour.count && !hour.events) {
        if (gap && previousTime !== undefined && previousTime + HOUR === hour.time) gap.to = hour.time + HOUR;else {
          gap = {
            path: branch.podsPath,
            tap: branch.tap,
            definition: branch.definition,
            from: hour.time,
            to: hour.time + HOUR
          };
          plan.packRequests.push(gap);
        }
      } else if(hour.count) gap = undefined;
      previousTime=hour.time;
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
  const started = performance.now();
  const discovery=await loadTimeWindow({
    trunk,
    scope,
    from,
    to,
    diagnostics: true
  }, token, signal, archive, options.fetcher);
  const discoveryMs = performance.now() - started;
  signal.throwIfAborted();
  const plan = await planSearch(trunk, scope, from, to, token, archive, signal, discovery.receipts);
  return {...plan, discoveryMs};
}

/** Retain the first-hour/first-day opportunity to publish a page promptly.
 * Then hand the remaining time range to the existing streaming collector,
 * which already bounds transfer/write batches. Calendar days are not I/O limits. */
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
  if(windows.length<=3)return windows;
  const tail=windows.slice(2);
  return [windows[0],windows[1],{from:Math.min(...tail.map(w=>w.from)),to:Math.max(...tail.map(w=>w.to))}];
}
