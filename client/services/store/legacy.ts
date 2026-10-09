// Read-only import into v2: an older open bundle can never recreate an idx in v2.
import {
  EventIndex,
  scope,
  day,
  type Catalog,
  type Entry,
  type EventKey,
  type SavedCollection,
} from "./index.ts";
import {
  bytes,
  directory,
  entries,
  handle,
  immutable,
  readFile,
  write,
} from "./files.ts";
import { StoreCodec, type FileIO } from "./wasm.ts";
import type { Collection } from "./protocol.ts";
const decoder = new TextDecoder();
async function existing(root: FileSystemDirectoryHandle, name: string) {
  try {
    return await root.getDirectoryHandle(name);
  } catch (e) {
    if (e instanceof DOMException && e.name === "NotFoundError")
      return undefined;
    throw e;
  }
}
function block(
  codec: StoreCodec,
  file: FileIO,
  at: number,
): Uint8Array | undefined {
  if (at + 8 > file.getSize()) return undefined;
  const h = new DataView(readFile(file, at, 8).buffer),
    n = h.getUint32(0);
  if (!n || n > 16 * 1024 * 1024) throw Error("Invalid legacy block length");
  if (at + 8 + n > file.getSize()) return undefined;
  const b = readFile(file, at + 8, n);
  if (codec.checksum(b) !== h.getUint32(4))
    throw Error("Legacy store checksum mismatch");
  return b;
}
function header(file: FileIO, magic: string) {
  const h = readFile(file, 0, 8);
  if (
    decoder.decode(h.subarray(0, 4)) !== magic ||
    h[4] ||
    h[5] ||
    h[6] ||
    h[7] !== 2
  )
    throw Error("Invalid legacy file header");
}
async function copy(
  src: FileIO,
  dst: FileSystemDirectoryHandle,
  name: string,
  end: number,
) {
  const out = await handle(dst, name);
  try {
    out.truncate(0);
    for (let at = 0; at < end; at += 1024 * 1024)
      write(out, readFile(src, at, Math.min(1024 * 1024, end - at)), at);
    out.flush();
  } finally {
    out.close();
  }
}
export async function migrate(
  root: FileSystemDirectoryHandle,
  db: EventIndex,
  codec: StoreCodec,
) {
  if (await db.get("state", "legacy-migrated")) return;
  const storage = await navigator.storage.getDirectory(),
    base = await existing(storage, "trie-trunk-v1");
  const old = base && (await existing(base, root.name));
  if (!old) {
    await db.set("state", "legacy-migrated", true);
    return;
  }
  const taps = await existing(old, "taps");
  if (taps)
    for await (const entry of entries(taps)) {
      if (entry.kind !== "directory" || !/^[a-f0-9]{64}$/.test(entry.name))
        continue;
      const src = await taps.getDirectoryHandle(entry.name);
      try {
        if (decoder.decode(await bytes(src, "ready")) !== "1") continue;
      } catch (e) {
        if (e instanceof DOMException && e.name === "NotFoundError") continue;
        throw e;
      }
      const dst = await directory(root, "taps", entry.name);
      for (const name of ["log.map", "log.pil", "tap", "ready"])
        await immutable(dst, name, await bytes(src, name));
      const tap = decoder.decode(await bytes(src, "tap")),
        schema = await bytes(src, "log.map");
      const digest = [
        ...new Uint8Array(
          await crypto.subtle.digest(
            "SHA-256",
            schema as Uint8Array<ArrayBuffer>,
          ),
        ),
      ]
        .map((n) => n.toString(16).padStart(2, "0"))
        .join("");
      await db.set("catalogs", entry.name, {
        key: entry.name,
        tap,
        digest,
      } satisfies Catalog);
      const tri = await handle(src, "log.tri");
      let triEnd = 8;
      try {
        if (tri.getSize()) {
          header(tri, "TTRI");
          for (;;) {
            const b = block(codec, tri, triEnd);
            if (!b) break;
            triEnd += 8 + b.length;
          }
        } else triEnd = 0;
        await copy(tri, dst, "log.tri", triEnd);
      } finally {
        tri.close();
      }
      for await (const y of entries(src)) {
        if (y.kind !== "directory" || !/^\d{4}$/.test(y.name)) continue;
        const year = await src.getDirectoryHandle(y.name);
        for await (const d of entries(year)) {
          if (d.kind !== "directory" || !/^\d{3}$/.test(d.name)) continue;
          const epochDay = Math.floor(
            Date.UTC(Number(y.name), 0, Number(d.name)) / 86400000,
          );
          const source = await year.getDirectoryHandle(d.name),
            target = await directory(dst, y.name, d.name),
            idx = await handle(source, "log.idx"),
            pop = await handle(source, "log.pop");
          try {
            if (!idx.getSize()) continue;
            header(idx, "TIDX");
            header(pop, "TPOP");
            let at = 8,
              popEnd = 8;
            const pending: Entry[] = [];
            async function publish() {
              const exists = await db.exists(pending.map((e) => e.key)),
                seen = new Set<string>();
              await db.publish(
                pending.filter((e, i) => {
                  const id = String(new Uint8Array(e.key[1]));
                  if (exists[i] || seen.has(id)) return false;
                  seen.add(id);
                  return true;
                }),
                [],
              );
              pending.length = 0;
            }
            for (;;) {
              const b = block(codec, idx, at);
              if (!b) break;
              if (b.length < 4) throw Error("Invalid legacy index run");
              const count = new DataView(b.buffer).getUint32(0);
              if (!count || b.length !== 4 + 36 * count)
                throw Error("Invalid legacy index run");
              const payload = block(codec, pop, popEnd);
              if (!payload) throw Error("Committed legacy population missing");
              let next = popEnd + 8,
                previous: Uint8Array | undefined;
              for (let i = 0; i < count; i++) {
                const r = b.subarray(4 + i * 36, 40 + i * 36),
                  view = new DataView(r.buffer, r.byteOffset, r.byteLength),
                  uuid = r.slice(0, 16);
                const start = Number(view.getBigUint64(20)),
                  end = Number(view.getBigUint64(28));
                if (
                  uuid[6] >> 4 !== 7 ||
                  uuid[8] >> 6 !== 2 ||
                  day([tap, uuid.buffer]) !== epochDay ||
                  start !== next ||
                  end <= start ||
                  end - start > 65535 ||
                  end > popEnd + 8 + payload.length ||
                  (previous && indexedDB.cmp(previous, uuid) >= 0)
                )
                  throw Error("Invalid legacy event pointer");
                previous = uuid;
                next = end;
              }
              if (next !== popEnd + 8 + payload.length)
                throw Error("Legacy population size mismatch");
              popEnd = next;
              at += 8 + b.length;
            }
            await copy(pop, target, "log.pop", popEnd);
            let blockAt = 8;
            const idxEnd = at;
            at = 8;
            while (at < idxEnd) {
              const b = block(codec, idx, at)!,
                count = new DataView(b.buffer).getUint32(0);
              for (let i = 0; i < count; i++) {
                const r = b.subarray(4 + i * 36, 40 + i * 36),
                  pointer = new Uint8Array(28);
                pointer.set(r.subarray(16));
                new DataView(pointer.buffer).setBigUint64(20, BigInt(blockAt));
                pending.push({
                  key: [tap, r.slice(0, 16).buffer],
                  value: { catalog: entry.name, pointer },
                });
                if (pending.length === 512) await publish();
              }
              blockAt = Number(
                new DataView(b.buffer).getBigUint64(b.length - 8),
              );
              at += 8 + b.length;
            }
            await publish();
            await db.publish(
              [],
              [
                { key: [entry.name, "tri"], end: triEnd },
                { key: [entry.name, epochDay], end: popEnd },
              ],
            );
          } finally {
            idx.close();
            pop.close();
          }
        }
      }
      await db.publish([], [{ key: [entry.name, "tri"], end: triEnd }]);
    }
  const collections = await existing(old, "collections");
  if (collections)
    for await (const e of entries(collections)) {
      if (e.kind !== "directory") continue;
      const dir = await collections.getDirectoryHandle(e.name);
      let collection: Collection;
      try {
        collection = JSON.parse(decoder.decode(await bytes(dir, "meta")));
      } catch (e) {
        if (
          e instanceof SyntaxError ||
          (e instanceof DOMException && e.name === "NotFoundError")
        )
          continue;
        throw e;
      }
      if (collection.id !== e.name) throw Error("Invalid legacy collection");
      const saved: SavedCollection = {
          collection,
          scope: scope(collection.selection),
          coverage: [],
        },
        file = await handle(dir, "members");
      try {
        let at = 0;
        for (;;) {
          const b = block(codec, file, at);
          if (!b) break;
          at += 8 + b.length;
          const record = JSON.parse(decoder.decode(b));
          if (record.coverage) saved.coverage.push(record.coverage);
          for (const group of record.refs ?? []) {
            const cat = await db.get<Catalog>("catalogs", group.catalog);
            if (!cat) throw Error("Missing legacy catalog");
            const date = new Date(group.day * 86400000),
              year = date.getUTCFullYear(),
              ordinal =
                Math.floor((date.getTime() - Date.UTC(year, 0, 1)) / 86400000) +
                1;
            const dir = await directory(
                taps!,
                group.catalog,
                String(year),
                String(ordinal).padStart(3, "0"),
              ),
              idx = await handle(dir, "log.idx");
            try {
              for (let i = 0; i < group.rows.length; i += 512) {
                const keys: EventKey[] = group.rows
                  .slice(i, i + 512)
                  .map((offset: number) => [
                    cat.tap,
                    readFile(idx, offset, 16).buffer,
                  ]);
                if ((await db.exists(keys)).some((v) => !v))
                  throw Error("Missing legacy collection event");
                await db.publish([], [], collection.id, keys);
              }
            } finally {
              idx.close();
            }
          }
        }
      } finally {
        file.close();
      }
      await db.set("collections", collection.id, saved);
    }
  await db.set("state", "legacy-migrated", true);
}
