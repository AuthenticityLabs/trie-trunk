import {
  rawValue,
  type Dictionary,
  type Field,
  type LogEvent,
} from "./dictionary.ts";
import { matchesSource } from "./source.ts";

export const parameterNames = [
  "level",
  "service",
  "resource",
  "operation",
] as const;
export type Parameters = Partial<Record<string, string>>;
export type Choices = Partial<Record<string, readonly string[]>>;
export type Predicate = (event: LogEvent) => boolean;
export type LocalSelection = {
  parameters: Parameters;
  tap: string;
  from: string;
  to: string;
};

export type TimeWindow = { from: string; to: string };

export function collectionWindow(
  events: readonly LogEvent[],
): TimeWindow | undefined {
  if (!events.length) return undefined;
  return {
    from: new Date(events[0].millis).toISOString().slice(0, -1),
    to: new Date(events[events.length - 1].millis + 1)
      .toISOString()
      .slice(0, -1),
  };
}

function selected(
  parameters: Parameters,
): [string, string][] {
  const names: string[] = [...parameterNames, ...Object.keys(parameters).filter(name =>
    !parameterNames.includes(name as (typeof parameterNames)[number])).sort()];
  return names.flatMap(name => parameters[name] ? [[name, parameters[name]!]] : []);
}

export function where(parameters: Parameters): string[] {
  const conditions = selected(parameters)
    .map(([name, value]) => `${name}==${JSON.stringify(value)}`)
    .join(" ");
  return conditions ? [conditions] : [];
}

export function describeSearch(selection: LocalSelection): string {
  return [
    ...selected(selection.parameters).map(
      ([name, value]) => `${name[0].toUpperCase() + name.slice(1)}: ${value}`,
    ),
    selection.tap ? `Tap: ${selection.tap}` : "",
    selection.from ? `From: ${selection.from.replace("T", " ")} UTC` : "",
    selection.to ? `To: ${selection.to.replace("T", " ")} UTC` : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

export function compileSearch(
  selection: LocalSelection,
): Predicate | undefined {
  const fields = selected(selection.parameters);
  const start = selection.from ? Date.parse(`${selection.from}Z`) : -Infinity;
  const end = selection.to ? Date.parse(`${selection.to}Z`) : Infinity;
  if (Number.isNaN(start) || Number.isNaN(end) || start >= end)
    throw new Error(
      "The search window must have a valid start before its end. Times use UTC.",
    );
  if (!fields.length && !selection.tap && !selection.from && !selection.to)
    return undefined;
  const bindings = new WeakMap<
    Dictionary,
    { field?: Field; value: string; ids: Set<number> }[]
  >();
  return (event) => {
    if (
      !matchesSource(selection.tap, event.dictionary.tap) ||
      event.millis < start ||
      event.millis >= end
    )
      return false;
    let comparisons = bindings.get(event.dictionary);
    if (!comparisons) {
      comparisons = fields.map(([name, value]) => {
        const field = event.dictionary.fields.names.get(name);
        return {
          field,
          value,
          ids: new Set(
            [...(field?.enums ?? [])]
              .filter(([, label]) => label === value)
              .map(([id]) => id),
          ),
        };
      });
      bindings.set(event.dictionary, comparisons);
    }
    return comparisons.every(({ field, value, ids }) => {
      const actual = rawValue(event, field);
      return typeof actual === "number" ? ids.has(actual) : actual === value;
    });
  };
}
