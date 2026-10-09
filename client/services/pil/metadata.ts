import { cbor } from "./cbor.ts";
import { compileFields, type Dictionary } from "../dictionary.ts";
import type { PILDefinition } from "./layout.ts";
export type Metadata = { digest: string; store: Uint8Array; pil: Uint8Array };
export type Binding = { stream: number; digest: string; tap: string };
export type Mapping = {
  definition: PILDefinition;
  dictionary: Dictionary;
  keys: number[];
  codes: number[][];
};
export function compile(
  metadata: Metadata,
  tap: string,
  headers: string[] = [],
): Mapping {
  const store = cbor(metadata.store) as Record<string, any>;
  const def = cbor(metadata.pil) as PILDefinition;
  if (
    (store["4"] !== "UTC" && !(typeof store["4"] === "string" && store["4"].length && [3, 4].includes(store["7"]?.["0"]))) ||
    def.pil !== 0 ||
    def.version !== store["0"] ||
    def.definition !== `trunk:${metadata.digest}`
  )
    throw Error("Incompatible PIL definition");
  function fields(source: Record<string, any>): Record<string, unknown> {
    return Object.fromEntries(
      Object.entries(source).map(([key, f]) => [
        key,
        {
          name: f["0"],
          domain: f["1"],
          values: f["2"],
          object: f["3"] ? fields(f["3"]) : undefined,
          array: f["4"] ? fields(f["4"]) : undefined,
          encoding: f["6"],
        },
      ]),
    );
  }
  const schema = compileFields(fields(store["1"]), store["3"] ?? {});
  const keys = [...schema.keys.keys()].sort((a, b) => a - b);
  if (def.fields.length !== keys.length + 2)
    throw Error("Incompatible PIL field count");
  const codes = keys.map((key, i) => {
    const field = schema.keys.get(key)!;
    const entries = [...field.enums].sort((a, b) => a[0] - b[0]);
    const mapped =
      entries.length &&
      new Set(entries.map(([, v]) => v)).size === entries.length &&
      !field.uuid &&
      field.name !== "duration";
    const type = field.uuid
      ? "UUID"
      : field.name === "duration"
        ? "U64"
        : mapped
          ? "STRING"
          : "CBOR";
    const p = def.fields[i];
    if (
      p.name !== field.name ||
      p.type !== type ||
      JSON.stringify(p.values ?? []) !==
        JSON.stringify(mapped ? entries.map(([, v]) => v) : [])
    )
      throw Error("Incompatible PIL field mapping");
    return mapped ? entries.map(([k]) => k) : [];
  });
  for (const [i, base, type] of [
    [keys.length, "trunk_lineage", "ARRAY<U16>"],
    [keys.length + 1, "trunk_fallback", "CBOR"],
  ] as const) {
    let name = base as string;
    while (schema.names.has(name)) name += "_";
    if (def.fields[i].name !== name || def.fields[i].type !== type)
      throw Error("Incompatible PIL metadata fields");
  }
  const id = schema.names.get("id");
  if (!id?.uuid) throw Error("PIL events require UUIDv7 id");
  const path: number[] = store["5"] ?? [];
  if (path.some((k) => !schema.keys.has(k))) throw Error("Invalid trie path");
  return {
    definition: def,
    keys,
    codes,
    dictionary: {
      digest: metadata.digest,
      tap,
      version: store["0"],
      fields: schema,
      id,
      path: new Set(path),
      headers,
    },
  };
}
export class Choices {
  private mappings: Mapping[];
  constructor(
    metadata: Metadata[],
    bindings: Binding[],
    headers: string[] = [],
  ) {
    const defs = new Map(metadata.map((m) => [m.digest, m]));
    this.mappings = bindings.map((b) =>
      compile(defs.get(b.digest)!, b.tap, headers),
    );
  }
  choices(names: readonly string[]): Record<string, string[]> {
    return Object.fromEntries(
      names.map((name) => [
        name,
        [
          ...new Set(
            this.mappings.flatMap((m) => [
              ...(m.dictionary.fields.names.get(name)?.enums.values() ?? []),
            ]),
          ),
        ].sort(),
      ]),
    );
  }
}
