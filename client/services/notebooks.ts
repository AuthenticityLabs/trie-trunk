import { transact } from "./database.ts";
import { eventId, type LogEvent } from "./dictionary.ts";

export type Notebook = {
  id: string;
  name: string;
  createdAt: number;
  lastUsed: number;
};

export type EventReference = {
  notebook: string;
  tap: string;
  id: string;
  version: number;
  digest: string;
  savedAt: number;
};

function reference(notebook: string, event: LogEvent): EventReference {
  return {
    notebook,
    tap: event.dictionary.tap,
    id: eventId(event),
    version: event.dictionary.version,
    digest: event.dictionary.digest,
    savedAt: Date.now(),
  };
}

export async function listNotebooks(): Promise<Notebook[]> {
  const notebooks: Notebook[] = await transact(
    "notebooks",
    "readonly",
    (store) => store.getAll(),
  );
  return notebooks.sort(
    (a, b) =>
      b.lastUsed - a.lastUsed ||
      b.createdAt - a.createdAt ||
      a.id.localeCompare(b.id),
  );
}

export async function savedNotebooks(event: LogEvent): Promise<Set<string>> {
  const keys = await transact("notebook_events", "readonly", (store) =>
    store.index("event").getAllKeys([event.dictionary.tap, eventId(event)]),
  );
  return new Set(keys.map((key) => (key as string[])[0]));
}

export async function createNotebook(
  name: string,
  event: LogEvent,
): Promise<Notebook> {
  const title = name.trim();
  if (!title) throw new Error("Enter a notebook name.");
  const now = Date.now();
  const notebook: Notebook = {
    id: crypto.randomUUID(),
    name: title,
    createdAt: now,
    lastUsed: now,
  };
  await transact(
    ["notebooks", "notebook_events"],
    "readwrite",
    (store, transaction) => {
      transaction
        .objectStore("notebook_events")
        .add(reference(notebook.id, event));
      return store.add(notebook);
    },
  );
  return notebook;
}

export async function saveToNotebook(
  notebook: string,
  event: LogEvent,
): Promise<void> {
  await transact(
    ["notebooks", "notebook_events"],
    "readwrite",
    (store, transaction) => {
      const request = store.get(notebook);
      request.onsuccess = () => {
        if (!request.result) {
          transaction.abort();
          return;
        }
        store.put({ ...request.result, lastUsed: Date.now() });
        transaction
          .objectStore("notebook_events")
          .put(reference(notebook, event));
      };
      return request;
    },
  );
}
