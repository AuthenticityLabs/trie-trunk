import { createAbortScope } from '../abort.ts';
import type { ViewQuery, ViewResponse, ViewSummary, ViewStatistics, NativeViewRecord } from '../pil/view.ts';
import type { TrunkBootstrap } from '../pil/trunk.ts';
import { collectNative } from '../pil/native-collect.ts';
import { compile, type Binding } from '../pil/metadata.ts';
import { NativeTrie, NativeEvent } from '../seeds/index.ts';
import { StatePredicate } from './state-predicate.ts';
import { INDEX_ROW_BYTES } from '../pil/pac-native.ts';
import { prepareSearch, searchTasks, searchWindows } from './planner.ts';
import { HOUR } from '../store/time-trie.ts';
import type { Archive } from '../store/protocol.ts';
import type { Codec } from '../pil/wasm.ts';
import type { StoredDefinition } from '../pil/stream.ts';
import { valueAt } from '../dictionary.ts';
const hex = (b: Uint8Array) => Array.from(b, n => n.toString(16).padStart(2, '0')).join('');
const hash = async (s: string) => hex(new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(s))));
type Item = {
  event: NativeEvent;
  row: number;
  bytes: Uint8Array;
  common?: Uint8Array;
  native: NativeViewRecord;
  stream: number;
  key: string;
  sort: string;
};
const stats = (): ViewStatistics => ({
  complete: true,
  matched: 0,
  evaluated: 0,
  services: [],
  levels: [],
  duration: {
    count: 0,
    average: 0,
    min: 0,
    max: 0
  },
  operations: {
    errors: [],
    latency: []
  }
});
export async function executeLocalSearch(trunk: TrunkBootstrap, query: ViewQuery, token: string, signal: AbortSignal, codec: Codec, definitions: Map<string, StoredDefinition>, archive: Archive, fetcher: typeof fetch, onPage?: ((page: ViewResponse) => void), completed?: Map<string, ViewSummary>, pages?: {get(key:string):ViewResponse|undefined;set(key:string,value:ViewResponse):void}): Promise<ViewResponse> {
  const failed = createAbortScope([signal]);
  signal = failed.signal;
  try {
  const started = performance.now(),
    from = Date.parse(query.from),
    to = Date.parse(query.to);
  if (!Number.isSafeInteger(from) || !Number.isSafeInteger(to) || from >= to || !Number.isInteger(query.size) || query.size < 1 || query.size > 4096) throw Error('Invalid local search');
  const plan = await prepareSearch(trunk, query.tap, from, to, {
    token,
    signal,
    codec,
    definitions,
    archive,
    fetcher
  });
  const targets = plan.branches,
    receipts = targets.map(t => t.receipt);
  const bindings: Binding[] = targets.map((t, i) => ({
    stream: i + 1,
    digest: t.definition,
    tap: t.tap
  }));
  const mappings = targets.map(t => compile(t.metadata, t.tap));
  const snapshot = await hash(JSON.stringify([trunk.version, query.tap, from, to, query.query, targets.map((t, i) => [t.tap, t.definition, receipts[i].serial, receipts[i].generation, receipts[i].sequence, receipts[i].rows.map(h => [h.time, h.count])])]));
  const prior = completed?.get(snapshot);
  const pageKey=JSON.stringify([snapshot,query.after??'',query.size]);
  // Validate origin identity and local completion before reusing the same page.
  // Retention, reset, generation changes, and event-cache eviction all force work.
  const cached=plan.packRequests.length===0?pages?.get(pageKey):undefined;
  if(cached){
    signal.throwIfAborted();
    const done={...cached,timings:{discovery:plan.discoveryMs??0,planning:performance.now()-started-(plan.discoveryMs??0),request:0,response:0,summaries:0,events:0,client:0}};
    onPage?.(done);return done;
  }
  let after: string | undefined;
  if (query.after) {
    let cursor;
    try {
      cursor = JSON.parse(atob(query.after));
    } catch {
      throw Error('Invalid client search cursor');
    }
    ;
    if (cursor.snapshot !== snapshot || typeof cursor.after !== 'string') throw Error('Search snapshot changed; restart search');
    after = cursor.after;
  }
  const order = query.query?.order === 'desc' ? -1 : 1;
  const compare = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0) * order;
  const selected: Item[] = [],
    ignored = new Set<string>(),
    knownText = new Set<string>();
  const words = query.query?.search?.text.match(/"[^"\n]+"|\S+/g) ?? [];
  for (const term of words) {
    const word = term.replace(/^"|"$/g, '').toLocaleLowerCase();
    if (mappings.some(m => [...m.dictionary.fields.keys.values()].some(f => [...f.enums.values()].some(v => v.toLocaleLowerCase().includes(word))))) knownText.add(term);else ignored.add(term);
  }
  const step = Math.max(300000, Math.ceil((to - from) / 128 / 300000) * 300000),
    counts = Array(Math.ceil((to - from) / step)).fill(0),
    errors = [...counts],
    summary: ViewSummary = {
      from,
      to,
      step,
      counts,
      errors,
      count: 0,
      status: 'committed-snapshot',
      statistics: stats()
    };
  const services = new Map<string, number>(),
    levels = new Map<string, number>(),
    operations = new Map<string, {
      service: string;
      resource: string;
      operation: string;
      errors: number;
      sum: number;
      count: number;
    }>();
  let sum = 0,
    extra = false,
    published = false,
    committedThrough = 0;
  for (const r of receipts) for (const h of r.rows) committedThrough = Math.max(committedThrough, h.time + HOUR);
  const sourcePrefix = await Promise.all(targets.map(t => hash(t.tap + ':' + t.definition)));
  function result(withSummary: boolean): ViewResponse {
    let size = 0;
    for (const item of selected) size += item.bytes.length;
    const bytes = new Uint8Array(size),
      rows = new Uint32Array(selected.length * 3);
    let offset = 0;
    for (const [i, item] of selected.entries()) {
      bytes.set(item.bytes, offset);
      rows.set([item.stream, offset, offset + item.bytes.length], i * 3);
      offset += item.bytes.length;
    }
    const next = extra && selected.length ? btoa(JSON.stringify({
      snapshot,
      after: selected.at(-1)!.sort
    })) : undefined;
    return {
      result: {
        cursor: query.after ?? '',
        snapshot,
        summaryVersion: snapshot,
        ...(withSummary ? {
          summary: prior ?? summary
        } : {}),
        page: selected.map(item => ({
          stream: item.stream,
          row: item.row,
          key: item.key,
          cached: false
        })),
        next,
        committedThrough,
        provisional: committedThrough < to,
        notice: ignored.size ? `Ignored Text: ${[...ignored].join(', ')}` : undefined
      },
      definitions: [...new Map(targets.map(t => [t.definition, t.metadata])).values()],
      bindings,
      batch: {
        bytes: bytes.buffer,
        rows,
        common: selected.map(i => i.common)
      },
      native: selected.map(i => i.native),
      timings: {
        discovery: plan.discoveryMs ?? 0,
        planning: planned - started - (plan.discoveryMs ?? 0),
        request: 0,
        response: 0,
        summaries: 0,
        events: performance.now() - planned,
        client: 0
      }
    };
  }
  const planned = performance.now();
  // Reuse aggregates only for this exact immutable snapshot. A resumed page
  // seeks to its timestamp; tied rows are still clipped by the full cursor key.
  const cursorTime = after ? Number(after.slice(0, 16)) : undefined;
  if (cursorTime !== undefined && (!Number.isSafeInteger(cursorTime) || cursorTime < from || cursorTime >= to)) throw Error('Invalid client search cursor');
  const scanFrom = prior && cursorTime !== undefined && order > 0 ? cursorTime : from;
  const scanTo = prior && cursorTime !== undefined && order < 0 ? Math.min(to, cursorTime + 1) : to;
  const windows = searchWindows(plan, scanFrom, scanTo, order < 0);
  const dependencies = new Map<number, {
    mapping: ReturnType<typeof compile>;
    trie: NativeTrie;
    matcher: StatePredicate;
    deps: NonNullable<Awaited<ReturnType<Archive>>['nativeDependencies']>;
  }>();
  for (const window of windows) {
    signal.throwIfAborted();
    const a = window.from,
      z = window.to;
    const iterators = await searchTasks(targets, signal, async (t, i, taskSignal) => {
      const hours = t.hours.filter(h => h.time < z && h.time + HOUR > a && h.count);
      const missing = plan.packRequests.filter(r => r.path === t.podsPath && r.from < z && r.to > a);
      for (const gap of missing) {
        await collectNative({
          tap: t.tap,
          from: new Date(Math.max(gap.from, Math.floor(a / HOUR) * HOUR)).toISOString(),
          to: new Date(Math.min(gap.to, Math.ceil(z / HOUR) * HOUR)).toISOString(),
          token,
          signal: taskSignal,
          codec,
          definitions,
          archive,
          fetcher,
          definitionVersion: t.definition,
          artifact: {
            path: t.podsPath,
            kind: 'pods'
          }
        });
        dependencies.delete(i);
      }
      if (!hours.length) return undefined;
      let prepared = dependencies.get(i);
      if (!prepared) {
        const deps = (await archive(token, {
          kind: 'native-resolve',
          tap: t.tap,
          digest: t.definition,
          generation: receipts[i].generation!,
          from: hours[0].time,
          to: hours[0].time + HOUR
        })).nativeDependencies;
        if (!deps) throw Error('Collected hour is unavailable');
        const mapping = mappings[i],
          trie = new NativeTrie(mapping);
        for (const c of deps.trie) trie.append(c.start, c.bytes);
        prepared = {
          mapping,
          trie,
          matcher: new StatePredicate(mapping, trie, query.query, knownText),
          deps
        };
        dependencies.set(i, prepared);
      }
      const {
        trie,
        matcher,
        deps
      } = prepared;
      return async function* () {
        let position: number | undefined,
          firstRead = true;
        do {
          signal.throwIfAborted();
          const page = (await archive(token, {
            kind: 'native-page',
            source: deps.source.key,
            from: a,
            to: z,
            after: position,
            limit: firstRead ? Math.max(128, Math.min(1024, query.size + 1)) : 4096,
            descending: order < 0
          })).nativePage!;
          firstRead = false;
          if (!page.ordinals || page.rows.length !== page.ordinals.length * INDEX_ROW_BYTES) throw Error('Missing local record coordinates');
          for (let j = 0; j < page.ordinals.length; j++) {
            const row = page.rows.subarray(j * INDEX_ROW_BYTES, (j + 1) * INDEX_ROW_BYTES);
            if (!matcher.matchesLeaf(row[4] | row[5] << 8)) continue;
            const payload = page.payload.subarray(page.offsets[j * 2], page.offsets[j * 2 + 1]),
              common = page.common?.[j],
              event = new NativeEvent(row, payload, trie, common, page.bases[j]);
            if (!matcher.matches(event)) continue;
            // Aggregation after the visible page needs neither UUID formatting nor
            // global result ordering. Keep those costs confined to page selection.
            const uuid = extra ? '' : hex(Uint8Array.from(event.uuid, c => c.charCodeAt(0))),
              sort = extra ? '' : String(event.millis).padStart(16, '0') + uuid + t.tap + ':' + t.definition;
            yield {
              event,
              row: page.ordinals[j],
              bytes: payload,
              common,
              native: {
                row,
                base: page.bases[j],
                trie: deps.trie
              },
              stream: i + 1,
              key: uuid + sourcePrefix[i].slice(0, 32),
              sort
            } satisfies Item;
          }
          if (page.done) break;
          if (page.after === position) throw Error('Local search made no progress');
          position = page.after;
        } while (true);
      }();
    });
    const heads = await searchTasks(iterators, signal, async it => {
      try {
        return it ? await it.next() : undefined;
      } catch (error) {
        failed.abort(error);
        throw error;
      }
    });
    for (;;) {
      signal.throwIfAborted();
      let best = -1;
      for (let i = 0; i < heads.length; i++) {
        const h = heads[i];
        if (h && !h.done && (best < 0 || compare(h.value.sort, heads[best]!.value!.sort) < 0)) best = i;
      }
      if (best < 0) break;
      const item = heads[best]!.value!;
      if (!prior) {
        const event = item.event,
          bin = Math.floor((event.millis - from) / step),
          parts = event.dictionary.tap.split('/'),
          level = String(valueAt(event, 'level') ?? (parts[3] === 'error' ? 'ERROR' : parts[3] === 'debug' ? 'DEBUG' : parts[3] === 'trace' ? 'TRACE' : 'INFO')).toUpperCase();
        const isError = level === 'ERROR';
        summary.count++;
        counts[bin]++;
        if (isError) errors[bin]++;
        services.set(parts[1], (services.get(parts[1]) ?? 0) + 1);
        levels.set(level, (levels.get(level) ?? 0) + 1);
        const operation = String(valueAt(event, 'operation') ?? ''),
          opKey = JSON.stringify([parts[1], parts[2], operation]);
        let op = operations.get(opKey);
        if (!op) {
          op = {
            service: parts[1],
            resource: parts[2],
            operation,
            errors: 0,
            sum: 0,
            count: 0
          };
          operations.set(opKey, op);
        }
        if (isError) op.errors++;
        const duration = valueAt(event, mappingDuration(event));
        if (level !== 'DEBUG' && level !== 'TRACE' && typeof duration === 'number' && Number.isFinite(duration) && duration >= 0) {
          const d = summary.statistics!.duration;
          d.min = d.count ? Math.min(d.min, duration) : duration;
          d.max = Math.max(d.max, duration);
          d.count++;
          sum += duration;
          op.sum += duration;
          op.count++;
        }
      }
      if (!extra && (!after || compare(item.sort, after) > 0)) {
        if (selected.length < query.size) selected.push({
          ...item,
          bytes: item.bytes.slice(),
          common: item.common?.slice(),
          native: {
            ...item.native,
            row: item.native.row.slice()
          }
        });else extra = true;
      }
      if (extra && !published && onPage) {
        onPage(result(!!prior));
        published = true;
      }
      if (extra && prior) {
        signal.throwIfAborted();
        return result(true);
      }
      heads[best] = await iterators[best]!.next();
    }
  }
  const s = summary.statistics!;
  s.matched = s.evaluated = summary.count;
  s.services = [...services].map(([name, count]) => ({
    name,
    count
  }));
  s.levels = [...levels].map(([name, count]) => ({
    name,
    count
  }));
  s.duration.average = s.duration.count ? sum / s.duration.count : 0;
  s.operations.errors = [...operations.values()].filter(o => o.errors).sort((a, b) => b.errors - a.errors).slice(0, 10).map(o => ({
    service: o.service,
    resource: o.resource,
    operation: o.operation,
    value: o.errors,
    samples: o.errors
  }));
  s.operations.latency = [...operations.values()].filter(o => o.count).sort((a, b) => b.sum / b.count - a.sum / a.count).slice(0, 10).map(o => ({
    service: o.service,
    resource: o.resource,
    operation: o.operation,
    value: o.sum / o.count,
    samples: o.count,
    statistic: 'average'
  }));
  signal.throwIfAborted();
  if (!prior && completed) {
    completed.set(snapshot, summary);
    while (completed.size > 8) completed.delete(completed.keys().next().value!);
  }
  const done = result(true);
  pages?.set(pageKey,done);
  if (!published) onPage?.(done);
  return done;
  } finally {
    failed.dispose();
  }
}
function mappingDuration(event: NativeEvent) {
  return event.durationField ?? 'duration';
}
