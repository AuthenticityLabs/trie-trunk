import {
  decodeValue,
  rawValue,
  type Field,
  type LogEvent,
} from "./dictionary.ts";
import { type Indicator, type Matcher, type Signal } from "./indicators.ts";
import { Operations } from "./operations.ts";
import { isDiagnostic } from './diagnostics.ts';

type Counts = Map<Field | undefined, Map<unknown, number>>;
const failures = new Set(["ERROR", "FATAL", "CRITICAL"]);
const enumFailures = new WeakMap<Field, Set<number>>();

export function isError(value: unknown, field?: Field): boolean {
  if (field && typeof value === "number" && field.enums.has(value)) {
    let keys = enumFailures.get(field);
    if (!keys) {
      keys = new Set(
        [...field.enums]
          .filter(([, label]) => failures.has(label.toUpperCase()))
          .map(([key]) => key),
      );
      enumFailures.set(field, keys);
    }
    return keys.has(value);
  }
  return typeof value === "string" && failures.has(value.toUpperCase());
}

function count(counts: Counts, field: Field | undefined, value: unknown) {
  let values = counts.get(field);
  if (!values) counts.set(field, (values = new Map()));
  values.set(value, (values.get(value) ?? 0) + 1);
}

function labels(counts: Counts, fallback: string): [string, number][] {
  const combined = new Map<string, number>();
  for (const [field, values] of counts) {
    for (const [value, count] of values) {
      const decoded = decodeValue(value, field);
      const label =
        decoded == null || decoded === ""
          ? fallback
          : typeof decoded === "object"
            ? JSON.stringify(decoded)
            : String(decoded);
      combined.set(label, (combined.get(label) ?? 0) + count);
    }
  }
  return [...combined].sort((a, b) => b[1] - a[1]);
}

const alphabet =
  "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const ranks = new Int16Array(128);
for (let i = 0; i < alphabet.length; i++) ranks[alphabet.charCodeAt(i)] = i;

function compare(a: LogEvent, b: LogEvent): number {
  if (a.millis !== b.millis) return a.millis - b.millis;
  if (a.uuid !== b.uuid) {
    for (let i = a.pil && b.pil ? 6 : 8; i < (a.pil && b.pil ? 16 : 22); i++) {
      const difference =
        a.pil && b.pil
          ? a.uuid.charCodeAt(i) - b.uuid.charCodeAt(i)
          : ranks[a.uuid.charCodeAt(i)] - ranks[b.uuid.charCodeAt(i)];
      if (difference) return difference;
    }
  }
  return a.dictionary.tap < b.dictionary.tap
    ? -1
    : a.dictionary.tap > b.dictionary.tap
      ? 1
      : 0;
}

function merge(target: LogEvent[], batch: LogEvent[]) {
  if (!batch.length) return;
  for (let i = 1; i < batch.length; i++) {
    if (compare(batch[i - 1], batch[i]) > 0) {
      batch.sort(compare);
      break;
    }
  }
  if (!target.length || compare(target[target.length - 1], batch[0]) <= 0) {
    for (const event of batch) target.push(event);
    return;
  }
  let left = target.length - 1;
  let right = batch.length - 1;
  let index = target.length + batch.length - 1;
  target.length += batch.length;
  while (right >= 0) {
    if (left >= 0 && compare(target[left], batch[right]) > 0)
      target[index--] = target[left--];
    else target[index--] = batch[right--];
  }
}

export function lowerBound(
  events: readonly LogEvent[],
  time: number,
  lo = 0,
): number {
  let hi = events.length;
  while (lo < hi) {
    const mid = lo + Math.floor((hi - lo) / 2);
    if (events[mid].millis < time) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

export class Results {
  readonly events: LogEvent[] = [];
  readonly annotations = new Map<LogEvent, readonly Indicator[]>();
  private identities = new Map<string, Set<string>>();
  private services: Counts = new Map();
  private levels: Counts = new Map();
  private errors: LogEvent[] = [];
  private marked: LogEvent[] = [];
  private groups = new Map<Indicator, LogEvent[]>();
  private matchers: Matcher[] = [];
  private durationSum = 0;
  private durationCount = 0;
  private operations = new Operations();

  setMatchers(matchers: Matcher[]) {
    this.matchers = matchers;
    this.annotations.clear();
    this.marked = [];
    this.groups.clear();
    for (const event of this.events) {
      if (this.match(event)) {
        this.marked.push(event);
        for (const indicator of this.annotations.get(event)!) {
          let events = this.groups.get(indicator);
          if (!events) this.groups.set(indicator, (events = []));
          events.push(event);
        }
      }
    }
  }

  private match(event: LogEvent): boolean {
    let matched: Indicator[] | undefined;
    for (const matcher of this.matchers) {
      if (matcher.matches(event)) (matched ??= []).push(matcher.indicator);
    }
    if (!matched) return false;
    this.annotations.set(event, matched);
    return true;
  }

  append(
    batch: readonly LogEvent[],
    onAdded?: (event: LogEvent) => void,
  ): number {
    const added: LogEvent[] = [];
    const errors: LogEvent[] = [];
    const marked: LogEvent[] = [];
    for (const event of batch) {
      if (isDiagnostic(event)) continue;
      let ids = this.identities.get(event.dictionary.tap);
      if (!ids) this.identities.set(event.dictionary.tap, (ids = new Set()));
      if (ids.has(event.uuid)) continue;
      ids.add(event.uuid);
      added.push(event);
      onAdded?.(event);
      const fields = event.dictionary.fields.names;
      const service = fields.get("service");
      const level = fields.get("level");
      const duration = fields.get("duration");
      const severity = rawValue(event, level);
      count(this.services, service, rawValue(event, service));
      count(this.levels, level, severity);
      const error = isError(severity, level);
      if (error) errors.push(event);
      const elapsed = rawValue(event, duration);
      this.operations.add(event, error, elapsed);
      if (typeof elapsed === "number" && Number.isFinite(elapsed)) {
        this.durationSum += elapsed;
        this.durationCount++;
      }
      if (this.match(event)) marked.push(event);
    }
    merge(this.events, added);
    merge(this.errors, errors);
    merge(this.marked, marked);
    const groups = new Map<Indicator, LogEvent[]>();
    for (const event of marked) {
      for (const indicator of this.annotations.get(event)!) {
        let events = groups.get(indicator);
        if (!events) groups.set(indicator, (events = []));
        events.push(event);
      }
    }
    for (const [indicator, events] of groups) {
      let previous = this.groups.get(indicator);
      if (!previous) this.groups.set(indicator, (previous = []));
      merge(previous, events);
    }
    return added.length;
  }

  summary() {
    const first = this.events[0]?.millis ?? Infinity;
    const last = this.events[this.events.length - 1]?.millis ?? -Infinity;
    const buckets = Array<number>(36).fill(0);
    const errorBuckets = Array<number>(36).fill(0);
    const indicatorBuckets = Array<number>(36).fill(0);
    let markedStart = 0;
    let eventStart = 0;
    let errorStart = 0;
    for (let i = 0; i < 36 && this.events.length; i++) {
      const end =
        first === last || i === 35
          ? Infinity
          : first + ((last - first) * (i + 1)) / 36;
      const eventEnd = lowerBound(this.events, end, eventStart);
      const errorEnd = lowerBound(this.errors, end, errorStart);
      const markedEnd = lowerBound(this.marked, end, markedStart);
      indicatorBuckets[i] = markedEnd - markedStart;
      markedStart = markedEnd;
      buckets[i] = eventEnd - eventStart;
      errorBuckets[i] = errorEnd - errorStart;
      eventStart = eventEnd;
      errorStart = errorEnd;
    }
    const signals: Signal[] = [];
    for (const [indicator, events] of this.groups) {
      let start = 0;
      for (let bucket = 0; bucket < 36; bucket++) {
        const boundary =
          first === last || bucket === 35
            ? Infinity
            : first + ((last - first) * (bucket + 1)) / 36;
        const end = lowerBound(events, boundary, start);
        if (end !== start) {
          const from = start;
          signals.push({
            key: `${bucket}:${indicator.id}`,
            bucket,
            indicator,
            get events() {
              return events.slice(from, end);
            },
          });
        }
        start = end;
      }
    }
    return {
      errors: this.errors.length,
      average: this.durationCount
        ? this.durationSum / this.durationCount
        : undefined,
      services: labels(this.services, "Unspecified"),
      levels: labels(this.levels, "UNKNOWN"),
      operations: this.operations.top(),
      buckets,
      errorBuckets,
      signals: signals.sort((a, b) => a.bucket - b.bucket),
      indicatorBuckets,
      first,
      last,
    };
  }
}
