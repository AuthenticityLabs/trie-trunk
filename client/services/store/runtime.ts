import { StoreCodec, type FileIO } from "./wasm.ts";
import { compile, type Metadata, type Binding } from "../pil/metadata.ts";
import { read, span } from "../pil/layout.ts";
import type { Command, ArchiveReply } from "./protocol.ts";
import type { Selection } from "../events.ts";
import {
  EventIndex,
  row,
  day,
  scope,
  type Entry,
  type EventKey,
  type Catalog,
  type SavedCollection,
} from "./index.ts";
import { directory, handle, bytes, immutable, recover } from "./files.ts";
import { migrate } from "./legacy.ts";

const encoder = new TextEncoder(),
  decoder = new TextDecoder();
const hex = (b: ArrayBuffer | Uint8Array) =>
  [
    ...new Uint8Array(
      b instanceof Uint8Array
        ? b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength)
        : b,
    ),
  ]
    .map((n) => n.toString(16).padStart(2, "0"))
    .join("");
const hash = async (b: Uint8Array) =>
  hex(await crypto.subtle.digest("SHA-256", b as Uint8Array<ArrayBuffer>));
// Only the UI's explicit string equality controls are planned locally for now.
// Unknown CLI-style expressions stay on the server, never get guessed.
export function conditions(
  selection: Selection,
): [string, string][] | undefined {
  if (!selection.where.length) return [];
  if (selection.where.length !== 1) return undefined;
  let rest = selection.where[0].trim();
  const out: [string, string][] = [];
  while (rest) {
    const m =
      /^(level|service|resource|operation)==("(?:[^"\\]|\\.)*")(?:\s+|$)/.exec(
        rest,
      );
    if (!m) return undefined;
    out.push([m[1], JSON.parse(m[2])]);
    rest = rest.slice(m[0].length);
  }
  return out;
}
export function createArchive(modulePromise: Promise<WebAssembly.Module>) {
  const codecPromise = modulePromise.then((m) => StoreCodec.create(m));
  codecPromise.catch(() => {});
  const contexts = new Map<string, number>();
  const catalogs = new Map<
    string,
    { metadata: Metadata; mapping: ReturnType<typeof compile> }
  >();
  const databases = new Map<string, Promise<EventIndex>>();
  type Cursor = {
    namespace: string;
    collection: string;
    bindings: Map<string, Binding>;
    after?: IDBValidKey;
    selection?: Selection;
    taps: string[];
    tap: number;
    filters?: [string, string][];
    leaves: Map<string, Map<number, boolean>>;
  };
  const cursors = new Map<string, Cursor>();
  async function catalog(
    root: FileSystemDirectoryHandle,
    key: string,
    metadata?: Metadata,
    tap?: string,
  ) {
    if (!/^[a-f0-9]{64}$/.test(key)) throw Error("Invalid local catalog");
    const dir = await directory(root, "taps", key);
    const existing = catalogs.get(root.name + key);
    if (existing) {
      if (
        metadata &&
        (existing.metadata.digest !== metadata.digest ||
          existing.metadata.pil.length !== metadata.pil.length ||
          existing.metadata.pil.some((b, i) => b !== metadata.pil[i]))
      )
        throw Error("Conflicting cached PIL definition");
      return { ...existing, dir };
    }
    if (metadata) {
      // Definitions become visible only after all immutable metadata is durable.
      let ready = false;
      try {
        ready =
          decoder.decode(
            await (
              await (await dir.getFileHandle("ready")).getFile()
            ).arrayBuffer(),
          ) === "1";
      } catch {}
      if (!ready)
        for (const name of ["log.map", "log.pil", "tap"]) {
          const h = await handle(dir, name);
          try {
            h.truncate(0);
            h.flush();
          } finally {
            h.close();
          }
        }
      if ((await hash(metadata.store)) !== metadata.digest)
        throw Error("Invalid cached tap schema");
      const mapping = compile(metadata, tap!);
      await immutable(dir, "log.map", metadata.store);
      await immutable(dir, "log.pil", metadata.pil);
      await immutable(dir, "tap", encoder.encode(tap!));
      await immutable(dir, "ready", encoder.encode("1"));
      catalogs.set(root.name + key, { metadata, mapping });
    }
    let cat = catalogs.get(root.name + key);
    if (!cat) {
      const store = await bytes(dir, "log.map"),
        pil = await bytes(dir, "log.pil"),
        name = decoder.decode(await bytes(dir, "tap"));
      const metadata = { store, pil, digest: await hash(store) };
      cat = { metadata, mapping: compile(metadata, name) };
      catalogs.set(root.name + key, cat);
    }
    return { ...cat, dir };
  }
  async function withStore<T>(
    root: FileSystemDirectoryHandle,
    db: EventIndex,
    key: string,
    epochDay: number,
    action: (codec: StoreCodec, id: number, files: FileIO[]) => T | Promise<T>,
  ) {
    const cat = await catalog(root, key),
      date = new Date(epochDay * 86400000),
      year = date.getUTCFullYear();
    const ordinal =
      Math.floor((date.getTime() - Date.UTC(year, 0, 1)) / 86400000) + 1;
    const dir = await directory(
      cat.dir,
      String(year),
      String(ordinal).padStart(3, "0"),
    );
    const codec = await codecPromise,
      files: FileIO[] = [],
      cacheKey = `${root.name}/${key}/${epochDay}`;
    try {
      const triEnd = await db.get<number>("files", [key, "tri"]),
        popEnd = await db.get<number>("files", [key, epochDay]);
      files.push(await handle(cat.dir, "log.tri"));
      files.push(await handle(dir, "log.pop"));
      recover(files[0], triEnd);
      recover(files[1], popEnd);
      codec.files = files;
      let id = contexts.get(cacheKey);
      if (id === undefined) {
        id = codec.open(epochDay, cat.metadata.store, cat.metadata.pil);
        contexts.set(cacheKey, id);
      } else {
        codec.refresh(id);
        contexts.delete(cacheKey);
        contexts.set(cacheKey, id);
      }
      if (contexts.size > 8) {
        const [old, h] = contexts.entries().next().value!;
        codec.close(h);
        contexts.delete(old);
      }
      return await action(codec, id, files);
    } catch (error) {
      const id = contexts.get(cacheKey);
      if (id !== undefined) {
        codec.close(id);
        contexts.delete(cacheKey);
      }
      throw error;
    } finally {
      codec.files = [];
      for (const f of files) f.close();
    }
  }
  async function define(
    root: FileSystemDirectoryHandle,
    db: EventIndex,
    key: string,
    metadata?: Metadata,
    tap?: string,
  ) {
    const cat = await catalog(root, key, metadata, tap);
    await db.set("catalogs", key, {
      key,
      tap: cat.mapping.dictionary.tap,
      digest: cat.metadata.digest,
    } satisfies Catalog);
    return cat;
  }
  async function prepared(namespace: string) {
    const root = await directory(
      await navigator.storage.getDirectory(),
      "trie-trunk-v2",
      namespace,
    );
    let db = databases.get(namespace);
    if (!db) {
      db = EventIndex.open(namespace).then(async (db) => {
        try {
          await migrate(root, db, await codecPromise);
          return db;
        } catch (e) {
          db.close();
          throw e;
        }
      });
      databases.set(namespace, db);
      db.catch(() => databases.delete(namespace));
    }
    return { root, db: await db };
  }
  async function openCursor(
    namespace: string,
    root: FileSystemDirectoryHandle,
    db: EventIndex,
    collection: string,
    selection?: Selection,
  ) {
    const definitions: Metadata[] = [],
      bindings = new Map<string, Binding>(),
      taps = new Set<string>();
    for (const c of await db.all<Catalog>("catalogs")) {
      if (selection?.tap && selection.tap !== c.tap) continue;
      const cat = await catalog(root, c.key);
      bindings.set(c.key, {
        stream: bindings.size,
        digest: c.digest,
        tap: c.tap,
      });
      taps.add(c.tap);
      if (!definitions.some((d) => d.digest === c.digest))
        definitions.push(cat.metadata);
    }
    const cursor = crypto.randomUUID();
    cursors.set(cursor, {
      namespace,
      collection,
      bindings,
      taps: [...taps].sort(),
      tap: 0,
      selection,
      filters: selection ? conditions(selection) : undefined,
      leaves: new Map(),
    });
    return { cursor, definitions, bindings: [...bindings.values()] };
  }
  async function next(
    root: FileSystemDirectoryHandle,
    db: EventIndex,
    cursor: Cursor,
  ) {
    let events: Entry[],
      done = false;
    if (cursor.selection) {
      events = [];
      while (cursor.tap < cursor.taps.length && !events.length) {
        events = await db.page(
          cursor.taps[cursor.tap],
          Date.parse(cursor.selection.from),
          Date.parse(cursor.selection.to!),
          cursor.after,
        );
        cursor.after = events.at(-1)?.key;
        if (events.length < 512) {
          cursor.tap++;
          cursor.after = undefined;
        }
      }
      done = cursor.tap === cursor.taps.length;
    } else {
      const page = await db.members(cursor.collection, cursor.after);
      events = page.entries;
      cursor.after = page.last;
      done = events.length < 512;
    }
    // Read population by file/block, then restore UUID order within this bounded batch.
    const ordered = events.map((entry, order) => ({ entry, order }));
    ordered.sort(
      (a, b) =>
        a.entry.value.catalog.localeCompare(b.entry.value.catalog) ||
        day(a.entry.key) - day(b.entry.key) ||
        Number(
          new DataView(
            a.entry.value.pointer.buffer,
            a.entry.value.pointer.byteOffset,
          ).getBigUint64(a.entry.value.pointer.byteLength - 8) -
            new DataView(
              b.entry.value.pointer.buffer,
              b.entry.value.pointer.byteOffset,
            ).getBigUint64(b.entry.value.pointer.byteLength - 8),
        ),
    );
    const frames: {
      frame: Uint8Array;
      stream: number;
      order: number;
      key: EventKey;
    }[] = [];
    for (let at = 0; at < ordered.length; ) {
      const first = ordered[at].entry,
        key = first.value.catalog,
        epochDay = day(first.key),
        group: typeof ordered = [];
      while (
        at < ordered.length &&
        ordered[at].entry.value.catalog === key &&
        day(ordered[at].entry.key) === epochDay
      )
        group.push(ordered[at++]);
      const cat = await catalog(root, key);
      await withStore(root, db, key, epochDay, (codec, id) => {
        let leaves = cursor.leaves.get(key);
        if (!leaves) {
          leaves = new Map();
          cursor.leaves.set(key, leaves);
        }
        for (const { entry, order } of group) {
          const packed = row(entry),
            leaf = packed.length === 42
              ? new DataView(packed.buffer).getUint16(16)
              : new DataView(packed.buffer).getUint32(16);
          let frame: Uint8Array | undefined;
          if (cursor.filters?.length) {
            let match = leaves.get(leaf);
            // Every filter must occur in the actual lineage before a per-leaf answer
            // is reusable. Sparse/non-trie fields are read from this PIL frame only.
            if (match === false) continue;
            const lineage =
                match === true ? new Uint8Array() : codec.lineage(id, leaf),
              view = new DataView(lineage.buffer);
            const values = new Map<number, number>();
            for (let i = 0; i < lineage.length; i += 10)
              values.set(view.getUint16(i), Number(view.getBigUint64(i + 2)));
            const allInTrie =
              match !== undefined ||
              cursor.filters.every(([name]) =>
                values.has(
                  cat.mapping.dictionary.fields.names.get(name)?.key ?? -1,
                ),
              );
            if (match === undefined || !allInTrie) {
              match = cursor.filters.every(([name, wanted]) => {
                const field = cat.mapping.dictionary.fields.names.get(name);
                if (!field) return false;
                if (values.has(field.key))
                  return field.enums.get(values.get(field.key)!) === wanted;
                frame ??= codec.get(id, packed);
                const position = cat.mapping.keys.indexOf(field.key),
                  state = span(frame, cat.mapping.definition, position).state;
                const value =
                  state === 0
                    ? (
                        read(
                          frame,
                          cat.mapping.definition,
                          cat.mapping.keys.length + 1,
                        ) as Record<string, unknown> | undefined
                      )?.[field.key]
                    : read(frame, cat.mapping.definition, position);
                if (typeof value === "number") {
                  const code =
                    state === 2 && cat.mapping.codes[position].length
                      ? cat.mapping.codes[position][value]
                      : value;
                  return field.enums.get(code) === wanted;
                }
                return value === wanted;
              });
              if (allInTrie) {
                if (leaves.size >= 4096)
                  leaves.delete(leaves.keys().next().value!);
                leaves.set(leaf, match);
              }
            }
            if (!match) continue;
          }
          frame ??= codec.get(id, packed);
          frames.push({
            frame,
            stream: cursor.bindings.get(key)!.stream,
            order,
            key: entry.key,
          });
        }
      });
    }
    frames.sort((a, b) => a.order - b.order);
    if (cursor.selection && frames.length)
      await db.publish(
        [],
        [],
        cursor.collection,
        frames.map((f) => f.key),
      );
    const payload = new Uint8Array(
        frames.reduce((n, f) => n + f.frame.length, 0),
      ),
      rows = new Uint32Array(frames.length * 3);
    let at = 0;
    frames.forEach((f, i) => {
      rows.set([f.stream, at, at + f.frame.length], i * 3);
      payload.set(f.frame, at);
      at += f.frame.length;
    });
    return { batch: { bytes: payload.buffer, rows }, done };
  }
  return async function execute(
    namespace: string,
    command: Command,
  ): Promise<ArchiveReply> {
    const { root, db } = await prepared(namespace);
    if (command.kind === "definitions") {
      const definitions: Metadata[] = [];
      for (const c of await db.all<Catalog>("catalogs")) {
        const cat = await catalog(root, c.key);
        if (!definitions.some((d) => d.digest === c.digest))
          definitions.push(cat.metadata);
      }
      return { definitions };
    }
    if (command.kind === "remember") {
      const definitions = new Map(
        command.definitions.map((d) => [d.digest, d]),
      );
      for (const b of command.bindings) {
        const m = definitions.get(b.digest);
        if (!m) throw Error("Missing cached definition");
        await define(
          root,
          db,
          await hash(encoder.encode(JSON.stringify([b.tap, b.digest]))),
          m,
          b.tap,
        );
      }
      return {};
    }
    if (command.kind === "release") {
      cursors.delete(command.cursor);
      return {};
    }
    if (command.kind === "begin") {
      await db.begin(command.collection);
      return {};
    }
    if (command.kind === "save") {
      if (!(await db.get("collections", command.collection)))
        throw Error("Unknown collection");
      const defs = new Map(command.definitions.map((d) => [d.digest, d]));
      const bindings = new Map<
        number,
        { key: string; mapping: ReturnType<typeof compile>; tap: string }
      >();
      for (const b of command.bindings) {
        const key = await hash(
            encoder.encode(JSON.stringify([b.tap, b.digest])),
          ),
          cat = await define(root, db, key, defs.get(b.digest), b.tap);
        bindings.set(b.stream, { key, mapping: cat.mapping, tap: b.tap });
      }
      const { bytes: buffer, rows } = command.batch,
        candidates: { at: number; key: EventKey; catalog: string }[] = [],
        seen = new Set<string>();
      for (let i = 0; i < rows.length; i += 3) {
        const b = bindings.get(rows[i]);
        if (!b) throw Error("Unknown local stream");
        const frame = new Uint8Array(
          buffer,
          rows[i + 1],
          rows[i + 2] - rows[i + 1],
        );
        const uuid = read(
          frame,
          b.mapping.definition,
          b.mapping.keys.indexOf(b.mapping.dictionary.id.key),
        );
        if (
          !(uuid instanceof Uint8Array) ||
          uuid.length !== 16 ||
          uuid[6] >> 4 !== 7 ||
          uuid[8] >> 6 !== 2
        )
          throw Error("Invalid UUIDv7");
        const identity = JSON.stringify([b.tap, hex(uuid)]);
        if (seen.has(identity)) continue;
        seen.add(identity);
        candidates.push({
          at: i,
          key: [b.tap, uuid.slice().buffer as ArrayBuffer],
          catalog: b.key,
        });
      }
      const existing = await db.exists(candidates.map((c) => c.key)),
        groups = new Map<string, typeof candidates>();
      candidates.forEach((c, i) => {
        if (existing[i]) return;
        const name = `${c.catalog}/${day(c.key)}`;
        const group = groups.get(name) ?? [];
        group.push(c);
        groups.set(name, group);
      });
      // Publish each bounded batch with its file ends. On a failed IDB transaction,
      // the next opener truncates uncommitted appends before reusing trie node IDs.
      for (const group of groups.values()) {
        const key = group[0].catalog,
          epochDay = day(group[0].key);
        await withStore(root, db, key, epochDay, async (codec, id, files) => {
          codec.upload(new Uint8Array(buffer));
          for (const c of group) codec.put(id, rows[c.at + 1], rows[c.at + 2]);
          const packed = codec.commit(id),
            entries: Entry[] = [];
          if (packed.length % 42) throw Error("Invalid packed event pointers");
          for (let at = 0; at < packed.length; at += 42)
            entries.push({
              key: [group[0].key[0], packed.slice(at, at + 16).buffer],
              value: { catalog: key, pointer: packed.slice(at + 16, at + 42) },
            });
          await db.publish(
            entries,
            [
              { key: [key, "tri"], end: files[0].getSize() },
              { key: [key, epochDay], end: files[1].getSize() },
            ],
            command.collection,
            entries.map((e) => e.key),
          );
        });
      }
      await db.publish(
        [],
        [],
        command.collection,
        candidates.filter((_, i) => existing[i]).map((c) => c.key),
      );
      return { batch: command.batch };
    }
    if (command.kind === "finish") {
      await db.finish(command.collection, command.selection, command.started);
      return {};
    }
    if (command.kind === "lookup") {
      const s = command.selection;
      if (
        s.follow ||
        s.limit !== null ||
        !s.to ||
        conditions(s) === undefined ||
        !Number.isFinite(Date.parse(s.from)) ||
        Date.parse(s.from) < 0 ||
        !(Date.parse(s.from) < Date.parse(s.to))
      )
        return {};
      const saved = await db.get<SavedCollection>(
        "collections",
        command.collection,
      );
      if (!saved || saved.scope !== scope(s))
        throw Error("Collection scope changed");
      const gaps = await db.missing(s, command.started);
      return {
        ...(await openCursor(namespace, root, db, command.collection, s)),
        gaps,
      };
    }
    if (command.kind === "restore") {
      let saved = command.collection
        ? await db.get<SavedCollection>("collections", command.collection)
        : undefined;
      if (!saved)
        saved = (await db.all<SavedCollection>("collections")).sort(
          (a, b) => b.collection.created - a.collection.created,
        )[0];
      if (!saved) return { done: true };
      return {
        ...(await openCursor(namespace, root, db, saved.collection.id)),
        collection: saved.collection,
        coverage: saved.coverage,
      };
    }
    const cursor = cursors.get(command.cursor);
    if (!cursor || cursor.namespace !== namespace)
      throw Error("Invalid local collection cursor");
    const reply = await next(root, db, cursor);
    if (reply.done) cursors.delete(command.cursor);
    return reply;
  };
}
