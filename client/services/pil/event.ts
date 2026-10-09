import { pack, type LogEvent } from "../dictionary.ts";
import { read, span } from "./layout.ts";
import type { Mapping } from "./metadata.ts";
// A row is a view over original PIL bytes. It has no values array, expanded
// event object, or cached message strings. The backing batch is owned by this tab.
export class PILEvent {
  readonly pil = true as const;
  readonly uuid: string;
  readonly millis: number;
  readonly dictionary;
  readonly fields;
  private bytes: Uint8Array;
  private mapping: Mapping;
  constructor(bytes: Uint8Array, mapping: Mapping) {
    this.bytes = bytes;
    this.mapping = mapping;
    this.dictionary = mapping.dictionary;
    this.fields = this.dictionary.fields;
    const idIndex = mapping.keys.indexOf(this.dictionary.id.key);
    const id = read(bytes, mapping.definition, idIndex);
    if (
      !(id instanceof Uint8Array) ||
      id.length !== 16 ||
      id[6] >> 4 !== 7 ||
      id[8] >> 6 !== 2
    )
      throw Error("Invalid PIL event UUIDv7");
    this.uuid = String.fromCharCode(...id);
    this.millis = 0;
    for (let i = 0; i < 6; i++) this.millis = this.millis * 256 + id[i];
  }
  private fallback(): Record<string, unknown> | undefined {
    return read(
      this.bytes,
      this.mapping.definition,
      this.mapping.keys.length + 1,
    ) as Record<string, unknown> | undefined;
  }
  read(key: number): unknown {
    const { keys, definition, codes } = this.mapping;
    const index = keys.indexOf(key);
    let value: unknown;
    if (index < 0 || span(this.bytes, definition, index).state === 0)
      value = this.fallback()?.[key];
    else {
      value = read(this.bytes, definition, index);
      if (
        codes[index].length &&
        span(this.bytes, definition, index).state === 2
      )
        value = codes[index][value as number];
    }
    const field = this.fields.keys.get(key);
    if (field?.uuid && value instanceof Uint8Array) return value;
    return pack(value, field);
  }
  orderedKeys(): number[] {
    const { keys, definition } = this.mapping;
    const lineage = read(this.bytes, definition, keys.length);
    if (!Array.isArray(lineage)) throw Error("Missing PIL lineage");
    const present = keys.filter(
      (_, i) => span(this.bytes, definition, i).state !== 0,
    );
    const extra = Object.keys(this.fallback() ?? {}).map(Number);
    const all = new Set([...present, ...extra]);
    if (
      present.some((k) => extra.includes(k)) ||
      new Set(lineage).size !== lineage.length ||
      lineage.some((k) => !all.has(k))
    )
      throw Error("Invalid PIL lineage or fallback");
    return [
      ...lineage,
      ...[...all]
        .filter((k) => !lineage.includes(k))
        .sort((a, b) => {
          const x = this.fields.keys.get(a)?.name ?? String(a),
            y = this.fields.keys.get(b)?.name ?? String(b);
          return x < y ? -1 : x > y ? 1 : 0;
        }),
    ];
  }
  encoded(): Uint8Array {
    return this.bytes;
  }
}
// Compile-time guarantee that existing views, indicators and local queries can
// read the byte-backed event without an event-shaped conversion layer.
const compatible = (value: PILEvent): LogEvent => value;
void compatible;
