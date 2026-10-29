import { decodeValue, valueAt } from '../dictionary.ts';
import type { QueryPlan, Filter, Predicate } from '../query.ts';
import type { Mapping } from '../pil/metadata.ts';
import { NativeTrie, NativeEvent } from '../pil/native-event.ts';
function numeric(text: string): number | bigint {
  if (/^[+-]?\d+$/.test(text)) {
    const n = BigInt(text);
    return n <= BigInt(Number.MAX_SAFE_INTEGER) && n >= BigInt(Number.MIN_SAFE_INTEGER) ? Number(n) : n;
  }
  const n = Number(text);
  if (!Number.isFinite(n)) throw Error('Invalid numeric comparison');
  return n;
}
function operand(f: Filter): unknown {
  const {
    kind,
    value
  } = f.value;
  if (kind === 'null') return null;
  if (kind === 'boolean') return value === 'true';
  if (kind === 'number') return numeric(value);
  if (kind === 'duration') {
    const m = value.match(/^([+-]?(?:\d+(?:\.\d*)?|\.\d+))(ns|us|µs|μs|ms|s)$/);
    if (!m) throw Error('Invalid duration comparison');
    return Number(m[1]) * {
      ns: .001,
      us: 1,
      'µs': 1,
      'μs': 1,
      ms: 1000,
      s: 1000000
    }[m[2]]!;
  }
  return value;
}
export function compareValue(actual: unknown, filter: Filter): boolean {
  return compare(actual, filter, operand(filter));
}
function compare(actual: unknown, filter: Filter, expected: unknown): boolean {
  if (actual === undefined) return false;
  if (filter.op === 'contains') return typeof actual === 'string' && typeof expected === 'string' && actual.includes(expected);
  const numbers = (typeof actual === 'number' || typeof actual === 'bigint') && (typeof expected === 'number' || typeof expected === 'bigint');
  if (!numbers && typeof actual !== typeof expected) return filter.op === '!=';
  const cmp = actual === expected || numbers && actual == expected ? 0 : actual as any < expected! ? -1 : 1;
  switch (filter.op) {
    case '==':
      return cmp === 0;
    case '!=':
      return cmp !== 0;
    case '>':
      return actual !== null && cmp > 0;
    case '>=':
      return actual !== null && cmp >= 0;
    case '<':
      return actual !== null && cmp < 0;
    case '<=':
      return actual !== null && cmp <= 0;
  }
}
/** Definition-bound text only. Unknown text never becomes a payload scan. */
export class StatePredicate {
  readonly ignoredText: string[] = [];
  private cached = new Map<number, boolean>();
  private stateFilters: {
    key: number;
    test: (value: unknown) => boolean;
  }[] = [];
  private eventFilters: {
    field: string;
    test: (value: unknown) => boolean;
  }[] = [];
  private expression?: {
    leaf: (leaf: number) => boolean | undefined;
    event: (event: NativeEvent) => boolean;
  };
  private text: {
    keys: number[];
    labels: Set<string>;
  }[] = [];
  readonly mapping: Mapping;
  readonly trie: NativeTrie;
  constructor(mapping: Mapping, trie: NativeTrie, plan?: QueryPlan, recognized?: Set<string>) {
    this.mapping = mapping;
    this.trie = trie;
    const path = mapping.dictionary.path;
    for (const f of plan?.filters ?? []) {
      const key = mapping.dictionary.fields.names.get(f.field)?.key,
        expected = operand(f);
      const test = (actual: unknown) => compare(actual, f, expected);
      if (key !== undefined && path.has(key)) this.stateFilters.push({
        key,
        test
      });else this.eventFilters.push({
        field: f.field,
        test
      });
    }
    if (plan?.predicate) this.expression = this.compileExpression(plan.predicate);
    for (const term of plan?.search?.text.match(/"[^"\n]+"|\S+/g) ?? []) {
      const word = term.replace(/^"|"$/g, '').toLocaleLowerCase();
      const labels = new Set<string>(),
        keys: number[] = [];
      for (const [key, field] of mapping.dictionary.fields.keys) {
        const matches = [...field.enums.values()].filter(v => v.toLocaleLowerCase().includes(word));
        if (matches.length) {
          keys.push(key);
          for (const v of matches) labels.add(v);
        }
      }
      if (keys.length || recognized?.has(term)) this.text.push({
        keys,
        labels
      });else this.ignoredText.push(term);
    }
  }
  private compileExpression(p: Predicate): {
    leaf: (leaf: number) => boolean | undefined;
    event: (event: NativeEvent) => boolean;
  } {
    if (p.kind === 'filter') {
      const f = p.filter,
        expected = operand(f),
        field = this.mapping.dictionary.fields.names.get(f.field);
      const state = field && this.mapping.dictionary.path.has(field.key);
      return {
        leaf: leaf => {
          if (!state) return undefined;
          const node = this.trie.field(leaf, field.key);
          return compare(node ? decodeValue(this.trie.read(node), field) : undefined, f, expected);
        },
        event: event => compare(valueAt(event, f.field), f, expected)
      };
    }
    if (p.kind === 'not') {
      const child = this.compileExpression(p.value);
      return {
        leaf: leaf => {
          const v = child.leaf(leaf);
          return v === undefined ? undefined : !v;
        },
        event: event => !child.event(event)
      };
    }
    const left = this.compileExpression(p.left),
      right = this.compileExpression(p.right);
    return {
      leaf: leaf => {
        const a = left.leaf(leaf),
          b = right.leaf(leaf);
        if (p.kind === 'and') return a === false || b === false ? false : a === true && b === true ? true : undefined;
        if (p.kind === 'or') return a === true || b === true ? true : a === false && b === false ? false : undefined;
        return a === undefined || b === undefined ? undefined : a !== b;
      },
      event: event => p.kind === 'and' ? left.event(event) && right.event(event) : p.kind === 'or' ? left.event(event) || right.event(event) : left.event(event) !== right.event(event)
    };
  }
  matchesLeaf(leaf: number) {
    let result = this.cached.get(leaf);
    if (result !== undefined) return result;
    result = this.stateFilters.every(f => {
      const field = this.mapping.dictionary.fields.keys.get(f.key)!,
        node = this.trie.field(leaf, f.key);
      return f.test(node ? decodeValue(this.trie.read(node), field) : undefined);
    });
    if (result) for (const term of this.text) {
      if (term.keys.every(k => this.mapping.dictionary.path.has(k)) && !term.keys.some(k => {
        const n = this.trie.field(leaf, k);
        return n && term.labels.has(String(decodeValue(this.trie.read(n), this.mapping.dictionary.fields.keys.get(k))));
      })) {
        result = false;
        break;
      }
    }
    if (result && this.expression?.leaf(leaf) === false) result = false;
    this.cached.set(leaf, result);
    return result;
  }
  matches(event: NativeEvent): boolean {
    if (!this.matchesLeaf(event.stateLeaf)) return false;
    if (!this.eventFilters.every(f => f.test(valueAt(event, f.field)))) return false;
    if (this.expression && !this.expression.event(event)) return false;
    return this.text.every(term => term.keys.every(k => this.mapping.dictionary.path.has(k)) || term.keys.some(k => term.labels.has(String(decodeValue(event.read(k), this.mapping.dictionary.fields.keys.get(k))))));
  }
}
