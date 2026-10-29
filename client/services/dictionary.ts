export type Field = {
  key: number;
  name: string;
  uuid: boolean;
  enums: ReadonlyMap<number, string>;
  object?: Fields;
  array?: Fields;
};

export type Fields = {
  keys: Map<number, Field>;
  names: Map<string, Field>;
};

type Catalog = {
  version: number;
  fields: Fields;
  id: Field;
  path: ReadonlySet<number>;
};

type Binding = Catalog & { digest: string };

export type Dictionary = Binding & { tap: string; headers: readonly string[]; objectIdentity?: {field:Field;type:string}; revisionIdentity?: Field };

export type Layout = {
  keys: readonly number[];
  fields: readonly (Field | undefined)[];
  positions: ReadonlyMap<number, number>;
  constants?: Map<string, Values>;
};

export type Values =
  | {
      read?: never;
      readonly layout: Layout;
      readonly values: readonly unknown[];
    }
  | {
      read(key: number): unknown;
      orderedKeys(): readonly number[];
      fields: Fields;
    };

export type LogEvent = Values & {
  readonly dictionary: Dictionary;
  readonly uuid: string;
  readonly millis: number;
  readonly pil?: true;
  readonly stateLeaf?: number;
};

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new Error("Invalid dictionary object.");
  return value as Record<string, unknown>;
}

function key(value: unknown): number {
  if (
    typeof value !== "number" ||
    !Number.isInteger(value) ||
    value < 0 ||
    value > 65535
  )
    throw new Error("Invalid dictionary key.");
  return value;
}

function labels(value: unknown): Record<string, string> {
  const result = object(value ?? {});
  for (const [name, label] of Object.entries(result)) {
    if (String(key(Number(name))) !== name || typeof label !== "string")
      throw new Error("Invalid dictionary value.");
  }
  return result as Record<string, string>;
}

function uuidBytes(value: unknown): string {
  if (!(value instanceof Uint8Array) && typeof value !== "string")
    throw new Error("Invalid UUID bytes.");
  const bytes = value instanceof Uint8Array
    ? String.fromCharCode(...value)
    : atob(value as string);
  if (bytes.length !== 16) throw new Error("Invalid UUID bytes.");
  return bytes;
}

// Only the event identity carries time and requires UUIDv7. Object identities
// and other Definition UUID fields must retain their own UUID version.
function eventUUIDBytes(value: unknown): string {
  const bytes = uuidBytes(value);
  if (bytes.charCodeAt(6) >> 4 !== 7 || bytes.charCodeAt(8) >> 6 !== 2)
    throw new Error("Invalid UUIDv7 bytes.");
  return bytes;
}

const hex = Array.from({ length: 256 }, (_, i) =>
  i.toString(16).padStart(2, "0"),
);

export function eventId(event: LogEvent): string {
  return event.pil ? formatUUID(event.uuid) : formatUUID(eventUUIDBytes(event.uuid));
}

export function formatUUID(bytes: string): string {
  let text = "";
  for (let i = 0; i < 16; i++) {
    if (i === 4 || i === 6 || i === 8 || i === 10) text += "-";
    text += hex[bytes.charCodeAt(i)];
  }
  return text;
}

export function eventTime(event: LogEvent): string {
  return new Date(event.millis)
    .toISOString()
    .replace(/(\.\d*?[1-9])0+Z$/, "$1Z")
    .replace(".000Z", "Z");
}

export function compileFields(
  value: unknown,
  domains: Record<string, unknown>,
  depth = 0,
): Fields {
  if (depth > 32)
    throw new Error("Dictionary nesting exceeds the supported depth.");
  const keys = new Map<number, Field>();
  const names = new Map<string, Field>();
  for (const [number, definition] of Object.entries(object(value))) {
    const field = object(definition);
    const numberKey = key(Number(number));
    if (String(numberKey) !== number || typeof field.name !== "string")
      throw new Error("Invalid dictionary field.");
    if (names.has(field.name))
      throw new Error("Duplicate projected field name.");
    const enums = {
      ...labels(
        typeof field.domain === "string" ? domains[field.domain] : undefined,
      ),
      ...labels(field.values),
    };
    const compiled: Field = {
      key: numberKey,
      name: field.name,
      uuid: field.encoding === "uuidv7",
      enums: new Map(
        Object.entries(enums).map(([key, label]) => [Number(key), label]),
      ),
      object:
        field.object === undefined
          ? undefined
          : compileFields(field.object, domains, depth + 1),
      array:
        field.array === undefined
          ? undefined
          : compileFields(field.array, domains, depth + 1),
    };
    keys.set(numberKey, compiled);
    names.set(field.name, compiled);
  }
  return { keys, names };
}

const layouts = new WeakMap<Fields, Map<string, Layout>>();
const lastLayouts = new WeakMap<Fields, Layout>();

function layout(
  fields: Fields,
  values: readonly unknown[],
  stride: number,
): Layout {
  const last = lastLayouts.get(fields);
  let matches = last?.keys.length === values.length / stride;
  if (matches) {
    for (let i = 0; i < last!.keys.length; i++) {
      if (
        stride === 2
          ? last!.keys[i] !== values[i * stride]
          : String(last!.keys[i]) !== values[i]
      ) {
        matches = false;
        break;
      }
    }
    if (matches) return last!;
  }
  const keys: number[] = [];
  for (let i = 0; i < values.length; i += stride) {
    const number = stride === 1 ? Number(values[i]) : values[i];
    keys.push(key(number));
    if (stride === 1 && String(number) !== values[i])
      throw new Error("Invalid dictionary key.");
  }
  const signature = keys.join(",");
  let cache = layouts.get(fields);
  if (!cache) layouts.set(fields, (cache = new Map()));
  let result = cache.get(signature);
  if (!result) {
    if (new Set(keys).size !== keys.length)
      throw new Error("Duplicate projected field name.");
    const definitions = keys.map((key) => {
      const field = fields.keys.get(key);
      if (!field && fields.names.has(String(key)))
        throw new Error("Duplicate projected field name.");
      return field;
    });
    result = {
      keys,
      fields: definitions,
      positions: new Map(keys.map((key, index) => [key, index])),
    };
    if (
      definitions.length &&
      definitions.every((field) => field && field.enums.size)
    )
      result.constants = new Map();
    cache.set(signature, result);
    if (cache.size > 64) cache.delete(cache.keys().next().value!);
  }
  lastLayouts.set(fields, result);
  return result;
}

export function pack(value: unknown, field?: Field): unknown {
  if (
    value == null ||
    !field ||
    (typeof value === "number" && field.enums.has(value))
  )
    return value;
  if (field.uuid) uuidBytes(value);
  else if (field.object) return packObject(value, field.object);
  else if (field.array) {
    if (!Array.isArray(value))
      throw new Error("Expected a dictionary object array.");
    for (let i = 0; i < value.length; i++)
      value[i] = packObject(value[i], field.array);
  }
  return value;
}

function packObject(value: unknown, fields: Fields): Values {
  if(value instanceof Map){
    const source=Object.create(null);
    for(const [key,v] of value){const name=String(key);if(Object.hasOwn(source,name))throw Error("Duplicate projected field name.");source[name]=v}
    value=source;
  }
  const source = object(value);
  const shape = layout(fields, Object.keys(source), 1);
  const values = shape.keys.map((key, i) => pack(source[key], shape.fields[i]));
  if (
    shape.constants &&
    values.every(
      (value, i) =>
        typeof value === "number" && shape.fields[i]!.enums.has(value),
    )
  ) {
    const signature = values.join(",");
    const previous = shape.constants.get(signature);
    if (previous) return previous;
    const record = { layout: shape, values };
    shape.constants.set(signature, record);
    if (shape.constants.size > 64)
      shape.constants.delete(shape.constants.keys().next().value!);
    return record;
  }
  return { layout: shape, values };
}

export function rawValue(event: Values, field: Field | undefined): unknown {
  if (!field) return undefined;
  if (event.read) return event.read(field.key);
  const position = event.layout.positions.get(field.key);
  return position === undefined ? undefined : event.values[position];
}

export function decodeValue(value: unknown, field?: Field): unknown {
  if (value instanceof Uint8Array)
    return field?.uuid
      ? formatUUID(String.fromCharCode(...value))
      : base64(value);
  if (value == null || !field) return value;
  if (field.uuid) return formatUUID(uuidBytes(value));
  if (typeof value === "number" && field.enums.has(value))
    return field.enums.get(value);
  if (field.object) return expandObject(value);
  if (field.array)
    return (value as unknown[]).map((item) => expandObject(item));
  return value;
}

function expandObject(value: unknown): Record<string, unknown> {
  return Object.fromEntries(
    objectEntries(value as Values).map(([name, value, field]) => [
      name,
      decodeValue(value, field),
    ]),
  );
}

export function objectEntries(value: Values): [string, unknown, Field?][] {
  if (value.read)
    return value
      .orderedKeys()
      .map((key) => [
        value.fields.keys.get(key)?.name ?? String(key),
        value.read(key),
        value.fields.keys.get(key),
      ]);
  return value.values.map((raw, i) => [
    value.layout.fields[i]?.name ?? String(value.layout.keys[i]),
    raw,
    value.layout.fields[i],
  ]);
}

export function valueAt(event: LogEvent, name: string): unknown {
  if (name === event.dictionary.id.name) return eventId(event);
  if (name === "time") return eventTime(event);
  const field = event.dictionary.fields.names.get(name);
  if (field) return decodeValue(rawValue(event, field), field);
  if (event.read)
    return String(Number(name)) === name ? event.read(Number(name)) : undefined;
  const index = event.layout.positions.get(Number(name));
  if (
    index !== undefined &&
    !event.layout.fields[index] &&
    String(event.layout.keys[index]) === name
  )
    return event.values[index];
  return undefined;
}

export function* entries(
  event: LogEvent,
): Generator<[string, unknown, Field?]> {
  const { dictionary } = event;
  for (const name of dictionary.headers) {
    if (name === dictionary.id.name) yield [name, eventId(event)];
    else if (name === "time") yield [name, eventTime(event)];
    else {
      const field = dictionary.fields.names.get(name);
      const value = field ? rawValue(event, field) : valueAt(event, name);
      if (value !== undefined) yield [name, value, field];
    }
  }
  if (!dictionary.headers.includes(dictionary.id.name)) yield [dictionary.id.name, eventId(event)];
  if (!dictionary.headers.includes("time")) yield ["time", eventTime(event)];
  for (const [name, raw, field] of objectEntries(event)) {
    if (name !== dictionary.id.name && name !== "time" && !dictionary.headers.includes(name))
      yield [name, raw, field];
  }
}

export function expandEvent(event: LogEvent): Record<string, unknown> {
  return Object.fromEntries(
    Array.from(entries(event), ([name, value, field]) => [
      name,
      decodeValue(value, field),
    ]),
  );
}

export function bindPath(dictionary: Dictionary, path: readonly string[]) {
  const first = dictionary.fields.names.get(path[0]);
  const steps: ((value: unknown) => unknown)[] = [];
  let field = first;
  let element: Fields | undefined;
  for (const name of path.slice(1)) {
    const fields: Fields | undefined = element ?? field?.object;
    if (fields) {
      const next = fields.names.get(name);
      const key = next?.key ?? Number(name);
      const valid = !!next || (!fields.keys.has(key) && String(key) === name);
      steps.push((value) => {
        if (!value || typeof value !== "object" || !valid) return undefined;
        const record = value as Values;
        if (record.read) return record.read(key);
        const index = record.layout.positions.get(key);
        return index === undefined ? undefined : record.values[index];
      });
      field = next;
      element = undefined;
    } else {
      element = field?.array;
      steps.push((value) =>
        value !== null &&
        typeof value === "object" &&
        Object.hasOwn(value, name)
          ? (value as Record<string, unknown>)[name]
          : undefined,
      );
      field = undefined;
    }
  }
  return {
    field: path[0] === dictionary.id.name || path[0] === "time" ? undefined : field,
    read(event: LogEvent): unknown {
      let value =
        path[0] === dictionary.id.name || path[0] === "time"
          ? valueAt(event, path[0])
          : first
            ? rawValue(event, first)
            : valueAt(event, path[0]);
      for (const step of steps) value = step(value);
      return value;
    },
  };
}

const cache = new Map<string, Catalog>();
const cacheLimit = 64;

function catalogKey(tap: unknown, version: unknown): string {
  if (
    typeof tap !== "string" ||
    !/^[a-z0-9][a-z0-9._-]{0,63}$/.test(tap) ||
    typeof version !== "number" ||
    !Number.isInteger(version) ||
    version < 1 ||
    version > 0xffffffff
  )
    throw new Error("Invalid dictionary catalog.");
  return tap + "/" + version;
}

export class Dictionaries {
  negotiation: string;
  private cached = new Map(cache);
  private catalogs: Map<string, Binding>;
  private schemas = new Map<number, Dictionary>();
  private active?: Dictionary;

  constructor(previous?: Dictionaries) {
    this.negotiation = previous?.negotiation ?? "";
    this.catalogs = new Map(previous?.catalogs);
    for (const binding of this.catalogs.values())
      this.cached.set(binding.digest, binding);
  }

  choices(names: readonly string[]): Record<string, string[]> {
    const choices: Record<string, string[]> = {};
    for (const name of names) {
      const values = new Set<string>();
      for (const catalog of this.catalogs.values())
        for (const label of catalog.fields.names.get(name)?.enums.values() ??
          [])
          values.add(label);
      choices[name] = [...values].sort();
    }
    return choices;
  }

  known(): string[] {
    return [...this.cached.keys()].slice(-cacheLimit);
  }

  install(value: unknown): string {
    const response = object(value);
    if (
      typeof response.negotiation !== "string" ||
      !/^[a-f0-9]{64}$/.test(response.negotiation) ||
      !Array.isArray(response.schemas)
    )
      throw new Error("Invalid schema negotiation response.");
    for (const value of response.schemas) {
      const schema = object(value);
      const bindingKey = catalogKey(schema.tap, schema.version);
      const digest = schema.digest;
      if (typeof digest !== "string" || !/^[a-f0-9]{64}$/.test(digest))
        throw new Error("Invalid dictionary digest.");
      let compiled = this.cached.get(digest);
      if (schema.dictionary !== undefined) {
        const definition = object(schema.dictionary);
        if (
          definition.version !== schema.version ||
          definition.timezone !== "UTC"
        )
          throw new Error("Invalid dictionary version or timezone.");
        const fields = compileFields(
          definition.fields,
          object(definition.domains ?? {}),
        );
        const id = fields.names.get("id");
        if (!id?.uuid) throw new Error("Dictionary requires one UUIDv7 id.");
        const path = definition.path ?? [];
        if (
          !Array.isArray(path) ||
          path.some((value) => !fields.keys.has(key(value)))
        )
          throw new Error("Invalid dictionary trie path.");
        compiled = {
          version: schema.version as number,
          fields,
          id,
          path: new Set(path),
        };
      }
      if (!compiled || compiled.version !== schema.version)
        throw new Error("Negotiation did not provide a required dictionary.");
      const previous = this.catalogs.get(bindingKey);
      if (previous && previous.digest !== digest)
        throw new Error("Dictionary changed without a new schema version.");
      this.cached.set(digest, compiled);
      cache.delete(digest);
      cache.set(digest, compiled);
      if (cache.size > cacheLimit) cache.delete(cache.keys().next().value!);
      this.catalogs.set(bindingKey, { ...compiled, digest });
    }
    this.negotiation = response.negotiation;
    return this.negotiation;
  }

  needs(value: unknown): boolean {
    if (Array.isArray(value)) return false;
    const control = object(value);
    return (
      control.tap !== undefined &&
      !this.catalogs.has(catalogKey(control.tap, control.version))
    );
  }

  read(value: unknown): LogEvent | undefined {
    if (!Array.isArray(value)) {
      const control = object(value);
      if (control.map !== undefined || control.dictionary !== undefined)
        throw new Error(
          "Dictionaries must be negotiated before the event pull.",
        );
      const handle = control.schema;
      if (
        typeof handle !== "number" ||
        !Number.isSafeInteger(handle) ||
        handle < 0
      )
        throw new Error("Invalid dictionary stream handle.");
      if (control.tap !== undefined) {
        if (this.schemas.has(handle))
          throw new Error("Dictionary redefined within a stream.");
        const catalog = this.catalogs.get(
          catalogKey(control.tap, control.version),
        );
        if (!catalog) throw new Error("Schema has not been negotiated.");
        const headers = control.headers ?? [];
        if (
          !Array.isArray(headers) ||
          !headers.every((header) => typeof header === "string")
        )
          throw new Error("Invalid presentation headers.");
        this.schemas.set(handle, {
          ...catalog,
          tap: control.tap as string,
          headers: [...new Set(headers)],
        });
      }
      this.active = this.schemas.get(handle);
      if (!this.active) throw new Error("Unknown dictionary stream handle.");
      return;
    }
    const dictionary = this.active;
    if (!dictionary) throw new Error("Event received before its dictionary.");
    if (value.length % 2 !== 0) throw new Error("Incomplete dictionary event.");
    const shape = layout(dictionary.fields, value, 2);
    const idPosition = shape.positions.get(dictionary.id.key);
    if (idPosition === undefined)
      throw new Error("Event is missing its UUIDv7 id.");
    const uuid = value[idPosition * 2 + 1];
    const bytes = eventUUIDBytes(uuid);
    let millis = 0;
    for (let i = 0; i < 6; i++) millis = millis * 256 + bytes.charCodeAt(i);
    const values = new Array<unknown>(shape.keys.length);
    for (let i = 0; i < shape.keys.length; i++)
      values[i] =
        i === idPosition ? uuid : pack(value[i * 2 + 1], shape.fields[i]);
    return { dictionary, layout: shape, values, uuid: uuid as string, millis };
  }
}

// Preserve full-width integers and opaque bytes when deriving a human view.
export function jsonText(value: unknown, space?: number): string {
  return JSON.stringify(
    value,
    (_key, item) => {
      if (typeof item === "bigint") return item.toString();
      if (item instanceof Uint8Array) return base64(item);
      return item;
    },
    space,
  );
}

function base64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 8192)
    binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
  return btoa(binary);
}
