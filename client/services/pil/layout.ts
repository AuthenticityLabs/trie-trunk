import { cbor } from "./cbor.ts";
export type PILField = { name: string; type: string; values?: string[] };
export type ResourceBinding = {
  service?: string; resource?: string;
  roles: {event_id:string; id?:string; revision_id?:string; shared?:string};
  path?: string[];
};
export type PILDefinition = {
  pil: number;
  adaptive_integers?: "none" | "16" | "32";
  definition: string;
  version: number;
  fields: PILField[];
  resource?: ResourceBinding;
};
export function resourceBinding(def: Pick<PILDefinition,'resource'>): ResourceBinding | undefined {
  return def.resource;
}
const widths: Record<string, number> = {
  BOOL: 1,
  I8: 1,
  U8: 1,
  I16: 2,
  U16: 2,
  REF16: 2,
  I32: 4,
  U32: 4,
  F32: 4,
  I64: 8,
  U64: 8,
  F64: 8,
  UUID: 16,
  TIMESTAMP_MS: 8,
  TIMESTAMP_US: 8,
  TIMESTAMP_NS: 8,
};
export const textDecoder = new TextDecoder("utf-8", { fatal: true });
export function width(field: PILField): number {
  const n = field.values?.length ?? 0;
  return n
    ? n <= 256
      ? 1
      : n <= 65536
        ? 2
        : n <= 4294967296
          ? 4
          : 8
    : (widths[field.type] ?? 0);
}
export function extended(view: DataView, offset: number): [number, number] {
  let n = view.getUint16(offset, true);
  offset += 2;
  if (n === 65535) {
    n = view.getUint32(offset, true);
    offset += 4;
    if (n === 4294967295) {
      const big = view.getBigUint64(offset, true);
      if (big > BigInt(Number.MAX_SAFE_INTEGER))
        throw Error("PIL integer exceeds addressable range");
      n = Number(big);
      offset += 8;
    }
  }
  return [n, offset];
}
const integerSelectors = new WeakMap<PILDefinition, Uint8Array>();
function selectors(def: PILDefinition): Uint8Array {
  let result = integerSelectors.get(def);
  if (!result) {
    result = Uint8Array.from(def.fields, (f) => {
      if (f.values?.length) return 0;
      if (/^[IU](32|64)$/.test(f.type)) return 2;
      return def.adaptive_integers === "16" && /^[IU]16$/.test(f.type) ? 1 : 0;
    });
    integerSelectors.set(def, result);
  }
  return result;
}
export type Span = { state: number; start: number; end: number };
// The WASM validator has already checked this frame. Resolve just one position,
// without allocating a field/value array for each retained event.
export function span(
  bytes: Uint8Array,
  def: PILDefinition,
  index: number,
): Span {
  const state = (i: number) => (bytes[i >> 2] >> (6 - 2 * (i % 4))) & 3;
  const kind = state(index);
  if (kind < 2) return { state: kind, start: 0, end: 0 };
  const mask = Math.ceil(def.fields.length / 4);
  const adaptive = def.adaptive_integers === "16" || def.adaptive_integers === "32";
  const bits = adaptive ? selectors(def) : undefined;
  let bit = 0;
  let total = 0;
  if (bits) {
    for (let i = 0; i < bits.length; i++) {
      if (state(i) === 2) total += bits[i];
    }
  }
  let direct = mask + Math.ceil(total / 8);
  for (let i = 0; i < def.fields.length; i++)
    if (state(i) === 2) {
      let w = width(def.fields[i]);
      const n = bits?.[i] ?? 0;
      if (n) {
        const offset = bit % 8;
        const at = mask + Math.floor(bit / 8);
        const code = offset + n <= 8
          ? (bytes[at] >> (8 - offset - n)) & ((1 << n) - 1)
          : ((bytes[at] & 1) << 1) | (bytes[at + 1] >> 7);
        w = 1 << code;
        bit += n;
      }
      const end = direct + w;
      if (i === index) return { state: kind, start: direct, end };
      direct = end;
    }
  let previous = 0,
    start = 0,
    end = 0,
    cursor = direct;
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.length);
  for (let i = 0; i < def.fields.length; i++)
    if (state(i) === 3) {
      let next;
      [next, cursor] = extended(view, cursor);
      if (i === index) {
        start = previous;
        end = next;
      }
      previous = next;
    }
  return { state: kind, start: cursor + start, end: cursor + end };
}
export function integer(
  view: DataView,
  size: number,
  signed = false,
): number | bigint {
  if (size === 1) return signed ? view.getInt8(0) : view.getUint8(0);
  if (size === 2)
    return signed ? view.getInt16(0, true) : view.getUint16(0, true);
  if (size === 4)
    return signed ? view.getInt32(0, true) : view.getUint32(0, true);
  const n = signed ? view.getBigInt64(0, true) : view.getBigUint64(0, true);
  return n >= BigInt(Number.MIN_SAFE_INTEGER) &&
    n <= BigInt(Number.MAX_SAFE_INTEGER)
    ? Number(n)
    : n;
}
export function read(
  bytes: Uint8Array,
  def: PILDefinition,
  index: number,
): unknown {
  const s = span(bytes, def, index),
    field = def.fields[index];
  if (s.state === 0) return undefined;
  if (s.state === 1) return null;
  const value = bytes.subarray(s.start, s.end);
  if (s.state === 2 && field.values?.length) {
    return integer(
      new DataView(value.buffer, value.byteOffset, value.length),
      width(field),
    );
  }
  return scalar(value, field.type);
}
export function scalar(value: Uint8Array, type: string): unknown {
  const view = new DataView(value.buffer, value.byteOffset, value.length);
  if (type === "CBOR") return cbor(value);
  if (type === "STRING") return textDecoder.decode(value);
  if (type === "UUID" || type === "BYTES") return value;
  if (type === "BOOL") return value[0] === 1;
  if (type === "F32") return view.getFloat32(0, true);
  if (type === "F64") return view.getFloat64(0, true);
  if (type.startsWith("ARRAY<")) {
    const element = type.slice(6, -1);
    const w = widths[element] ?? 0;
    const [count, offset] = extended(view, 0);
    if (w) {
      return Array.from({ length: count }, (_, i) =>
        scalar(value.subarray(offset + i * w, offset + (i + 1) * w), element),
      );
    }
    const ends: number[] = [];
    let cursor = offset;
    for (let i = 0; i < count; i++) {
      let end;
      [end, cursor] = extended(view, cursor);
      ends.push(end);
    }
    return ends.map((end, i) =>
      scalar(value.subarray(cursor + (i ? ends[i - 1] : 0), cursor + end), element),
    );
  }
  if (widths[type])
    return integer(
      view,
      value.length,
      type.startsWith("I") || type.startsWith("TIMESTAMP"),
    );
  throw Error(`Unsupported browser field type ${type}`);
}
