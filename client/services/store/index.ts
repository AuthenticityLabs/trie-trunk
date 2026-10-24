import type { Collection, NativeSelection } from "./protocol.ts";
import type { Selection } from "../events.ts";
import { Coverage, type Interval } from "../coverage.ts";

export type EventKey = [string, ArrayBuffer];
export type Pointer = { catalog: string; pointer: Uint8Array };
export type Entry = { key: EventKey; value: Pointer };
export type Catalog = { key: string; tap: string; digest: string };
export type SavedCollection = {
  collection: Collection;
  scope: string;
  coverage: Interval[];
  native?: NativeSelection[];
};
export const scope = (s: Selection) =>
  JSON.stringify([s.tap, [...s.where].sort()]);
const stores = [
  "events",
  "files",
  "catalogs",
  "collections",
  "members",
  "state",
];

// The event primary key is the ordered UUID index. No redundant UUID index,
// string UUIDs, event objects, or population bytes are stored here.
export class EventIndex {
  private db: IDBDatabase;
  private constructor(db: IDBDatabase) { this.db = db; }
  static open(namespace: string): Promise<EventIndex> {
    return new Promise((resolve, reject) => {
      const request = indexedDB.open(`snoot-events-${namespace}`, 2);
      let blocked = false;
      request.onupgradeneeded = event => {
        // Disposable pre-resource test data: invalidate pointers and coverage
        // together. The separate snoot preferences database is untouched.
        for (const name of Array.from(request.result.objectStoreNames)) request.result.deleteObjectStore(name);
        for (const name of stores) request.result.createObjectStore(name);
        if (event.oldVersion > 0) request.transaction!.objectStore("state").put(true, "reset-resource-files");
      };
      request.onerror = () => reject(request.error);
      request.onblocked = () => {
        blocked = true;
        reject(Error("Close older Snoot tabs to upgrade event storage"));
      };
      request.onsuccess = () => {
        const db = request.result;
        db.onversionchange = () => db.close();
        if (blocked) db.close();
        else resolve(new EventIndex(db));
      };
    });
  }
  close() {
    this.db.close();
  }
  // Queue all requests synchronously (or in a request callback). Never hold an
  // IDB transaction across file I/O, WASM work, network, or an unrelated await.
  transaction<T>(
    names: string[],
    mode: IDBTransactionMode,
    action: (tx: IDBTransaction) => () => T,
  ): Promise<T> {
    return new Promise((resolve, reject) => {
      const tx = this.db.transaction(
        names,
        mode,
        mode === "readwrite" ? { durability: "strict" } : undefined,
      );
      let result: () => T;
      tx.oncomplete = () => {
        try {
          resolve(result());
        } catch (e) {
          reject(e);
        }
      };
      tx.onabort = () =>
        reject(tx.error ?? Error("Event index transaction aborted"));
      try {
        result = action(tx);
      } catch (error) {
        tx.abort();
        reject(error);
      }
    });
  }
  get<T>(name: string, key: IDBValidKey): Promise<T | undefined> {
    return this.transaction([name], "readonly", (tx) => {
      const r = tx.objectStore(name).get(key);
      return () => r.result;
    });
  }
  all<T>(name: string): Promise<T[]> {
    return this.transaction([name], "readonly", (tx) => {
      const r = tx.objectStore(name).getAll();
      return () => r.result;
    });
  }
  set(name: string, key: IDBValidKey, value: unknown) {
    return this.transaction([name], "readwrite", (tx) => {
      tx.objectStore(name).put(value, key);
      return () => {};
    });
  }
  exists(keys: EventKey[]) {
    return this.transaction(["events"], "readonly", (tx) => {
      const requests = keys.map((k) => tx.objectStore("events").getKey(k));
      return () => requests.map((r) => r.result !== undefined);
    });
  }
  publish(
    entries: Entry[],
    ends: { key: IDBValidKey; end: number }[],
    collection?: string,
    members: EventKey[] = [],
  ) {
    return this.transaction(
      ["events", "files", "members"],
      "readwrite",
      (tx) => {
        for (const entry of entries)
          tx.objectStore("events").add(entry.value, entry.key);
        for (const { key, end } of ends) tx.objectStore("files").put(end, key);
        if (collection)
          for (const key of members)
            tx.objectStore("members").put(0, [collection, ...key]);
        return () => {};
      },
    );
  }
  page(
    tap: string,
    from: number,
    to: number,
    after?: IDBValidKey,
    limit = 512,
  ): Promise<Entry[]> {
    const range = IDBKeyRange.bound(
      after ?? [tap, timeKey(from)],
      [tap, timeKey(to)],
      !!after,
      true,
    );
    return this.transaction(["events"], "readonly", (tx) => {
      const s = tx.objectStore("events"),
        keys = s.getAllKeys(range, limit),
        values = s.getAll(range, limit);
      return () =>
        values.result.map((value, i) => ({
          key: keys.result[i] as EventKey,
          value,
        }));
    });
  }
  members(
    collection: string,
    after?: IDBValidKey,
    limit = 512,
  ): Promise<{ entries: Entry[]; last?: IDBValidKey }> {
    return this.transaction(["members", "events"], "readonly", (tx) => {
      const range = IDBKeyRange.bound(
        after ?? [collection],
        [collection, []],
        !!after,
        true,
      );
      const keys = tx.objectStore("members").getAllKeys(range, limit);
      const requests: { key: EventKey; request: IDBRequest }[] = [];
      keys.onsuccess = () => {
        for (const key of keys.result) {
          const k = (key as IDBValidKey[]).slice(1) as EventKey;
          requests.push({ key: k, request: tx.objectStore("events").get(k) });
        }
      };
      return () => ({
        entries: requests.map(({ key, request }) => {
          if (!request.result)
            throw Error("Collection references a missing event");
          return { key, value: request.result };
        }),
        last: keys.result.at(-1),
      });
    });
  }
  async begin(collection: Collection) {
    await this.set("collections", collection.id, {
      collection,
      scope: scope(collection.selection),
      coverage: [],
    } satisfies SavedCollection);
  }
  async finish(id: string, selection: Selection, started: number) {
    if (selection.follow || selection.limit !== null || !selection.to) return;
    const saved = await this.get<SavedCollection>("collections", id);
    if (!saved || saved.scope !== scope(selection))
      throw Error("Collection scope changed");
    const coverage = new Coverage();
    for (const range of saved.coverage) coverage.add(range);
    coverage.add({
      from: Date.parse(selection.from),
      to: Math.min(Date.parse(selection.to), started - 900_000),
    });
    saved.coverage = [...coverage.intervals()];
    await this.set("collections", id, saved);
  }
  async missing(selection: Selection, now: number) {
    const coverage = new Coverage();
    for (const saved of await this.all<SavedCollection>("collections"))
      if (!saved.native && saved.scope === scope(selection))
        for (const r of saved.coverage)
          coverage.add({ from: r.from, to: Math.min(r.to, now - 900_000) });
    return coverage.missing({
      from: Date.parse(selection.from),
      to: Date.parse(selection.to!),
    });
  }
}
// Half-open millisecond ranges include every UUID suffix at the lower boundary.
export function timeKey(millis: number): ArrayBuffer {
  if (!Number.isSafeInteger(millis) || millis < 0 || millis >= 2 ** 48)
    throw Error("Invalid UUID time boundary");
  const bytes = new Uint8Array(16);
  for (let i = 5; i >= 0; i--) {
    bytes[i] = millis % 256;
    millis = Math.floor(millis / 256);
  }
  return bytes.buffer;
}
export function row(entry: Entry): Uint8Array {
  if (![26, 28].includes(entry.value.pointer.length)) throw Error("Invalid event pointer");
  const b = new Uint8Array(16 + entry.value.pointer.length);
  b.set(new Uint8Array(entry.key[1]));
  b.set(entry.value.pointer, 16);
  return b;
}
export function day(key: EventKey): number {
  let n = 0;
  for (const b of new Uint8Array(key[1], 0, 6)) n = n * 256 + b;
  return Math.floor(n / 86400000);
}
