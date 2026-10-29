import { decodeObject } from "./control.ts";
import { compileFields, type Dictionary, type Fields } from "../dictionary.ts";
import { resourceBinding, type PILDefinition } from "./layout.ts";
export type Metadata = { digest: string; store: Uint8Array; pil: Uint8Array };
export type Binding = { stream: number; digest: string; tap: string };
export type Mapping = {
  definition: PILDefinition;
  dictionary: Dictionary;
  keys: number[];
  codes: number[][];
  native?: boolean;
};
// Definition structure is immutable by digest. Binding/UI context is cheap and
// remains per call; field dictionaries and schema validation are shared.
const compiled=new Map<string,{store:Uint8Array;pil:Uint8Array;mapping:Mapping}>();
export function compile(metadata:Metadata,tap:string,headers:string[]=[]):Mapping {
  let cached=compiled.get(metadata.digest);
  if(cached){
    if(cached.store.length!==metadata.store.length||cached.pil.length!==metadata.pil.length||
       cached.store.some((v,i)=>v!==metadata.store[i])||cached.pil.some((v,i)=>v!==metadata.pil[i]))throw Error("Immutable PIL definition changed");
  }else{
    cached={store:metadata.store.slice(),pil:metadata.pil.slice(),mapping:compileDefinition(metadata,"",[])};
    if(compiled.size>=1024)compiled.delete(compiled.keys().next().value!);
    compiled.set(metadata.digest,cached);
  }
  return {...cached.mapping,dictionary:{...cached.mapping.dictionary,tap,headers}};
}
function compileDefinition(
  metadata: Metadata,
  tap: string,
  headers: string[] = [],
): Mapping {
  const store = decodeObject(metadata.store) as Record<string, any>;
  const def = decodeObject(metadata.pil) as PILDefinition;
  const resource = resourceBinding(store);
  if (resource) {
    if (
      metadata.store.length !== metadata.pil.length ||
      metadata.store.some((byte, i) => byte !== metadata.pil[i])
    ) throw Error("PIL/resource maps must be identical");
    const adaptive = def.adaptive_integers === "16" || def.adaptive_integers === "32";
    if (def.pil !== (adaptive ? 1 : 0) || !Array.isArray(def.fields)) {
      throw Error("Unsupported PIL/resource definition");
    }
    const keys = def.fields.map((_, i) => i);
    const fields: Fields = { keys: new Map(), names: new Map() };
    for (const [i, f] of def.fields.entries()) {
      const field = {
        key: i,
        name: f.name,
        uuid: f.type === "UUID",
        enums: new Map((f.values ?? []).map((v, j) => [j, v])),
      };
      fields.keys.set(i, field);
      fields.names.set(f.name, field);
    }
    const id = fields.names.get(resource.roles.event_id);
    if (!id?.uuid) throw Error("Missing event identity");
    const path = new Set<number>();
    for (const name of resource.path ?? []) {
      const field = fields.names.get(name);
      if (!field) throw Error("Invalid trie path");
      path.add(field.key);
    }
    return {
      native: true,
      definition: def,
      keys,
      codes: def.fields.map((f) => (f.values ?? []).map((_, i) => i)),
      dictionary: {
        tap, digest: metadata.digest, version: def.version,
        fields, id, path, headers,
        revisionIdentity: resource.roles.revision_id ? fields.names.get(resource.roles.revision_id) : undefined,
        objectIdentity: resource.roles.id && fields.names.has(resource.roles.id)
          ? {field:fields.names.get(resource.roles.id)!,type:def.fields[fields.names.get(resource.roles.id)!.key].type} : undefined,
      },
    };
  }

  if (
    (store["4"] !== "UTC" && !(typeof store["4"] === "string" && store["4"].length && [3, 4].includes(store["7"]?.["0"]))) ||
    def.pil !== (def.adaptive_integers === "16" || def.adaptive_integers === "32" ? 1 : 0) ||
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
          : "OBJECT";
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
    [keys.length + 1, "trunk_fallback", "OBJECT"],
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
  fields():import('../query').QueryField[]{
    const fields=new Map<string,import('../query').QueryField>();
    for(const m of this.mappings)for(const f of m.definition.fields){
      const duration=resourceBinding(m.definition)?.roles.duration===f.name||(!m.native&&f.name==='duration');
      const prior=fields.get(f.name);fields.set(f.name,{...(duration?{unit:'us' as const}:{}),name:f.name,type:prior&&prior.type!==f.type?'mixed':f.type,values:[...new Set([...(prior?.values??[]),...(f.values??[])])]});
    }
    return [...fields.values()].sort((a,b)=>a.name.localeCompare(b.name));
  }
  choices(names: readonly string[] = [...new Set(this.mappings.flatMap(m =>
    [...m.dictionary.fields.names].filter(([, field]) => field.enums.size).map(([name]) => name),
  ))]): Record<string, string[]> {
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
