import type { ViewQuery, ViewResponse } from '../pil/view.ts';
import type { TrunkBootstrap } from '../pil/trunk.ts';
import { branchTargets, loadTimeWindow } from '../pil/time-window.ts';
import { HOUR } from '../store/time-trie.ts';
import type { Archive } from '../store/protocol.ts';
const DAY = 24 * HOUR;
const hex = (b: Uint8Array) => Array.from(b, n => n.toString(16).padStart(2, '0')).join('');
const hash = async (s: string) => hex(new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(s))));
/** Fast dashboards consume replicated aggregate columns, without a server search. */
export async function queryLocalSummaries(trunk: TrunkBootstrap, query: ViewQuery, token: string, signal: AbortSignal, archive: Archive, fetcher: typeof fetch, projection?: (manifest: import('../pil/summaries.ts').DailyAnalytics, days: import('../pil/summaries.ts').DecodedDay[]) => void): Promise<ViewResponse> {
  const {
    replicateSummaryRanges
  } = await import('../pil/summary-replication.ts');
  const {
    decodeSummaryDay,
    mergeDailyAnalytics
  } = await import('../pil/summaries.ts');
  const started = performance.now(),
    bucket = 300000,
    from = Math.ceil(Date.parse(query.from) / bucket) * bucket,
    to = Math.floor(Date.parse(query.to) / bucket) * bucket;
  if (!Number.isSafeInteger(from) || !Number.isSafeInteger(to) || from >= to) throw Error('Summary window must include a complete five-minute interval');
  await loadTimeWindow({
    trunk,
    scope: query.tap,
    from,
    to
  }, token, signal, archive, fetcher);
  const transient = await replicateSummaryRanges(trunk, query.tap, from, to, token, signal, archive, fetcher);
  const manifest: import('../pil/summaries.ts').DailyAnalytics = {
      from,
      to,
      days: []
    },
    days: import('../pil/summaries.ts').DecodedDay[] = [];
  for (const target of branchTargets(trunk, query.tap)) {
    const {
        tap,
        definition
      } = target,
      receipt = (await archive(token, {
        kind: 'time-read',
        query: {
          tap,
          digest: definition.digest,
          from: Math.floor(from / HOUR) * HOUR,
          to: Math.ceil(to / HOUR) * HOUR
        }
      })).time;
    if (!receipt?.generation) continue;
    for (const day of [...new Set(receipt.rows.filter(h => h.count).map(h => Math.floor(h.time / DAY) * DAY))]) {
      signal.throwIfAborted();
      const bytes = transient.find(d => d.tap === tap && d.digest === definition.digest && d.generation === receipt.generation && d.day === day)?.bytes ?? (await archive(token, {
        kind: 'summary-read',
        query: {
          tap,
          digest: definition.digest,
          generation: receipt.generation,
          day
        }
      })).summaryBytes;
      if (!bytes) continue; // Open days have no immutable summary yet.
      const decoded = await decodeSummaryDay(bytes),
        key = hex(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes.slice().buffer)));
      manifest.days.push({
        source: tap,
        definition: definition.digest,
        generation: receipt.generation,
        day,
        key,
        sealed: !!decoded.sealed
      });
      days.push(decoded);
    }
  }
  const step = Math.max(bucket, Math.ceil((to - from) / 128 / bucket) * bucket),
    counts = Array(Math.ceil((to - from) / step)).fill(0);
  const summary = mergeDailyAnalytics({
    from,
    to,
    step,
    counts,
    errors: [...counts],
    count: 0,
    status: 'committed-snapshot'
  }, manifest, days);
  projection?.(manifest, days);
  const snapshot = await hash(JSON.stringify(manifest));
  return {
    result: {
      cursor: '',
      snapshot,
      summaryVersion: snapshot,
      summary,
      analytics: manifest,
      page: [],
      committedThrough: Math.max(0, ...days.map(d => d.to)),
      provisional: days.some(d => !d.sealed) || to > Date.now() - DAY
    },
    definitions: [],
    bindings: [],
    batch: {
      bytes: new ArrayBuffer(0),
      rows: new Uint32Array()
    },
    timings: {
      request: 0,
      response: 0,
      summaries: performance.now() - started,
      events: 0,
      client: 0
    }
  };
}
