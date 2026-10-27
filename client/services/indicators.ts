import {
  bindPath,
  jsonText,
  decodeValue,
  entries,
  objectEntries,
  type Values,
  type Dictionary,
  type Field,
  type LogEvent,
} from "./dictionary.ts";
import { transact } from "./database.ts";

export type EventField = { path: string[]; text: string; fixed?: true };

export type Indicator = {
  id: string;
  label: string;
  path: string[];
  mode: "text" | "equals" | "regex" | "lte" | "gte";
  pattern: string;
  ignoreCase: boolean;
};

export type Matcher = {
  indicator: Indicator;
  matches: (event: LogEvent) => boolean;
};

export type Annotations = ReadonlyMap<LogEvent, readonly Indicator[]>;

export type Signal = {
  key: string;
  bucket: number;
  indicator: Indicator;
  events: LogEvent[];
};

function text(value: unknown): string {
  return typeof value === "string" ? value : jsonText(value);
}

export function eventFields(event: LogEvent): EventField[] {
  const fields: EventField[] = [];
  function visit(value: unknown, path: string[], field?: Field, fixed = false) {
    if (
      value !== null &&
      typeof value === "object" &&
      !(value instanceof Uint8Array)
    ) {
      if (field?.object) {
        const children = objectEntries(value as Values);
        if (children.length) {
          for (const [name, child, nested] of children)
            visit(child, [...path, name], nested, fixed);
          return;
        }
      } else if (field?.array && Array.isArray(value) && value.length) {
        for (let i = 0; i < value.length; i++) {
          const children = objectEntries(value[i] as Values);
          if (!children.length)
            fields.push({
              path: [...path, String(i)],
              text: "{}",
              fixed: true,
            });
          for (const [name, child, nested] of children)
            visit(child, [...path, String(i), name], nested, fixed);
        }
        return;
      } else if (!field?.object && !field?.array) {
        const children = Object.entries(value);
        if (children.length) {
          for (const [key, child] of children)
            visit(child, [...path, key], undefined, fixed);
          return;
        }
      }
    }
    if (value !== undefined) {
      const decoded = decodeValue(value, field);
      fields.push({
        path,
        text: text(decoded),
        ...(!scalar(decoded) || fixed ? { fixed: true as const } : {}),
      });
    }
  }
  for (const [name, value, field] of entries(event))
    visit(
      value,
      [name],
      field,
      !!field && event.dictionary.path.has(field.key),
    );
  return fields;
}

function scalar(value: unknown): boolean {
  return (
    typeof value === "string" ||
    typeof value === "boolean" ||
    (typeof value === "number" && Number.isFinite(value))
  );
}

export function compileIndicator(indicator: Indicator): Matcher {
  if (
    !indicator ||
    typeof indicator.id !== "string" ||
    !indicator.id ||
    typeof indicator.label !== "string" ||
    !indicator.label.trim() ||
    !Array.isArray(indicator.path) ||
    !indicator.path.length ||
    !indicator.path.every((part) => typeof part === "string") ||
    !["text", "equals", "regex", "lte", "gte"].includes(indicator.mode) ||
    typeof indicator.pattern !== "string" ||
    (!indicator.pattern.length && indicator.mode !== "equals") ||
    typeof indicator.ignoreCase !== "boolean"
  )
    throw new Error("An indicator needs a label, field and match pattern.");
  const numeric = indicator.mode === "lte" || indicator.mode === "gte";
  const threshold = numeric ? Number(indicator.pattern) : 0;
  if (numeric && (!indicator.pattern.trim() || !Number.isFinite(threshold)))
    throw new Error("Enter a finite numeric threshold.");
  const pattern = indicator.ignoreCase
    ? indicator.pattern.toLowerCase()
    : indicator.pattern;
  const regex =
    indicator.mode === "regex"
      ? new RegExp(indicator.pattern, indicator.ignoreCase ? "i" : "")
      : undefined;
  const bindings = new WeakMap<
    Dictionary,
    {
      read: (event: LogEvent) => unknown;
      field?: Field;
      matches: Map<number, boolean>;
    } | null
  >();
  const accepts = (value: unknown) => {
    if (!scalar(value)) return false;
    if (numeric)
      return (
        typeof value === "number" &&
        Number.isFinite(value) &&
        (indicator.mode === "lte" ? value <= threshold : value >= threshold)
      );
    if (indicator.mode === "equals")
      return (
        typeof value === "string" &&
        (indicator.ignoreCase ? value.toLowerCase() : value) === pattern
      );
    const candidate = text(value);
    return regex
      ? regex.test(candidate)
      : (indicator.ignoreCase ? candidate.toLowerCase() : candidate).includes(
          pattern,
        );
  };
  return {
    indicator,
    matches(event) {
      let bound = bindings.get(event.dictionary);
      if (bound === undefined) {
        const root = event.dictionary.fields.names.get(indicator.path[0]);
        if (root && event.dictionary.path.has(root.key)) {
          bindings.set(event.dictionary, null);
          return false;
        }
        const path = bindPath(event.dictionary, indicator.path);
        bound = {
          ...path,
          matches: new Map(
            [...(path.field?.enums ?? [])].map(([key, label]) => [
              key,
              accepts(label),
            ]),
          ),
        };
        bindings.set(event.dictionary, bound);
      }
      if (!bound) return false;
      const value = bound.read(event);
      if (typeof value === "number" && bound.matches.has(value))
        return bound.matches.get(value)!;
      return accepts(decodeValue(value, bound.field));
    },
  };
}

export async function loadIndicators(): Promise<Indicator[]> {
  const value = await transact<Indicator[] | undefined>('preferences','readonly',s=>s.get('event-highlights'));
  const indicators=value ?? [];for(const indicator of indicators)compileIndicator(indicator);return indicators;
}
export async function saveIndicator(indicator: Indicator): Promise<void> {
  compileIndicator(indicator);
  await transact('preferences','readwrite',s=>{const r=s.get('event-highlights');r.onsuccess=()=>s.put([...(r.result??[]).filter((i:Indicator)=>i.id!==indicator.id),indicator],'event-highlights');return r;});
}
export async function deleteIndicator(id: string): Promise<void> {
  await transact('preferences','readwrite',s=>{const r=s.get('event-highlights');r.onsuccess=()=>s.put((r.result??[]).filter((i:Indicator)=>i.id!==id),'event-highlights');return r;});
}
