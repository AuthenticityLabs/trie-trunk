import { rawValue, type LogEvent } from "./dictionary.ts";
import { isError, lowerBound } from "./results.ts";

/** Convert a horizontal brush into integer UUIDv7 time bounds, end exclusive.
 * Chart coordinates stay independent of timestamps so the existing histogram
 * and its indicator bubbles share exactly the same bucket positions.
 */
export function brushWindow(
  selection: unknown, first: number, last: number, extent: number,
): [number, number] | undefined {
  if (!Array.isArray(selection) || selection.length !== 2 ||
      !selection.every(value => typeof value === "number" && Number.isFinite(value)) ||
      !Number.isSafeInteger(first) || !Number.isSafeInteger(last) ||
      first < 0 || last < first || !Number.isFinite(extent) || extent <= 0) return;
  const left = Math.max(0, Math.min(...selection));
  const right = Math.min(extent, Math.max(...selection));
  if (left >= right) return;
  if (first === last) return [first, last + 1];
  const from = Math.ceil(first + (last - first) * left / extent);
  const to = right === extent ? last + 1 : Math.ceil(first + (last - first) * right / extent);
  return from < to ? [from, to] : undefined;
}

export type ChartWindow = { from: number; to: number; points: number };
export type ChartColumns = {
  time: number[];
  events: number[];
  errorRate: (number | null)[];
  latency: (number | null)[];
  bucketMs: number;
  from: number;
  to: number;
  count: number;
};

export function validWindow(value: unknown): value is ChartWindow {
  if (!value || typeof value !== "object") return false;
  const { from, to, points } = value as ChartWindow;
  return Number.isSafeInteger(from) && from >= 0 &&
    Number.isSafeInteger(to) && to <= 8.64e15 && from < to &&
    Number.isSafeInteger(points) && points >= 1 && points <= 600;
}

/** One bounded projection shared by all panels; event frames remain untouched. */
export class ChartProjection {
  private revision = -1;
  private cache = new Map<string, ChartColumns>();

  query(events: LogEvent[], revision: number, window: ChartWindow): ChartColumns {
    if (!validWindow(window)) throw new Error("Invalid chart time window.");
    if (revision !== this.revision) {
      this.revision = revision;
      this.cache.clear();
    }
    const key = `${window.from}:${window.to}:${window.points}`;
    const cached = this.cache.get(key);
    if (cached) return cached;
    const from = Math.max(window.from, events[0]?.millis ?? window.from);
    const to = Math.min(window.to, (events.at(-1)?.millis ?? (window.to - 1)) + 1);
    const start = lowerBound(events, from);
    const end = lowerBound(events, to, start);
    const bucketMs = Math.max(1, Math.ceil((to - from) / window.points));
    const length = end > start ? Math.ceil((to - from) / bucketMs) : 0;
    const counts = Array<number>(length).fill(0);
    const errors = Array<number>(length).fill(0);
    const sums = Array<number>(length).fill(0);
    const samples = Array<number>(length).fill(0);
    for (let i = start; i < end; i++) {
      const event = events[i];
      const bucket = Math.floor((event.millis - from) / bucketMs);
      counts[bucket]++;
      const fields = event.dictionary.fields.names;
      const level = fields.get("level");
      if (isError(rawValue(event, level), level)) errors[bucket]++;
      const duration = rawValue(event, fields.get("duration"));
      // Snoot's existing log duration convention is microseconds.
      if (typeof duration === "number" && Number.isFinite(duration) && duration >= 0) {
        sums[bucket] += duration;
        samples[bucket]++;
      }
    }
    const result: ChartColumns = {
      time: counts.map((_, i) => from + i * bucketMs),
      events: counts,
      errorRate: counts.map((n, i) => n ? errors[i] * 100 / n : null),
      latency: sums.map((sum, i) => samples[i] ? sum / samples[i] / 1000 : null),
      bucketMs, from, to, count: end - start,
    };
    if (this.cache.size >= 4) this.cache.delete(this.cache.keys().next().value!);
    this.cache.set(key, result);
    return result;
  }
}
