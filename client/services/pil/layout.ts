import { cbor } from "./cbor.ts";
export type PILField = { name: string; type: string; values?: string[] };
export type PILDefinition = {
  pil: number;
  definition: string;
  version: number;
  fields: PILField[];
};
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
  let direct = Math.ceil(def.fields.length / 4);
  for (let i = 0; i < def.fields.length; i++)
    if (state(i) === 2) {
      const end = direct + width(def.fields[i]);
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
  const value = bytes.subarray(s.start, s.end),
    view = new DataView(value.buffer, value.byteOffset, value.length);
  if (s.state === 2 && field.values?.length) return integer(view, width(field));
  if (field.type === "CBOR") return cbor(value);
  if (field.type === "STRING") return textDecoder.decode(value);
  if (field.type === "UUID" || field.type === "BYTES") return value;
  if (field.type === "BOOL") return value[0] === 1;
  if (field.type === "F32") return view.getFloat32(0, true);
  if (field.type === "F64") return view.getFloat64(0, true);
  if ((field.type === "ARRAY<U16>" || field.type === "ARRAY<REF16>")) {
    const [n, offset] = extended(view, 0);
    return Array.from({ length: n }, (_, i) =>
      view.getUint16(offset + i * 2, true),
    );
  }
  if (widths[field.type])
    return integer(
      view,
      widths[field.type],
      field.type.startsWith("I") || field.type.startsWith("TIMESTAMP"),
    );
  throw Error(`Unsupported browser field type ${field.type}`);
}
