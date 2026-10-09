import type { Collection } from "../store/protocol.ts";
import { PILEvent } from "./event.ts";
import { compile, Choices, type Metadata, type Mapping } from "./metadata.ts";
import type { Request, Reply } from "./protocol.ts";
import type { Selection, LogEvent } from "../events.ts";
let transport: MessagePort | undefined;
let owner: SharedWorker | Worker | undefined;
let fileWorker: Worker | undefined;
let next = 1;
const requests = new Map<
  number,
  { receive: (reply: Reply) => void; fail: (error: Error) => void }
>();
function port(): MessagePort {
  if (transport) return transport;
  if (typeof SharedWorker !== "undefined") {
    owner = new SharedWorker(new URL("./worker.ts", import.meta.url), {
      type: "module",
    });
    transport = owner.port;
  } else {
    owner = new Worker(new URL("./worker.ts", import.meta.url), {
      type: "module",
    });
    const channel = new MessageChannel();
    transport = channel.port1;
    owner.postMessage({ kind: "connect" }, [channel.port2]);
  }
  fileWorker = new Worker(new URL("../store/worker.ts", import.meta.url), {
    type: "module",
  });
  const storage = new MessageChannel();
  fileWorker.postMessage({ kind: "connect" }, [storage.port1]);
  transport.postMessage({ kind: "connect-storage" }, [storage.port2]);
  const failure = () => {
    transport?.postMessage({ kind: "close" });
    for (const job of requests.values())
      job.fail(Error("PIL worker stopped. Reload the page to reconnect."));
    requests.clear();
  };
  owner.onerror = failure;
  fileWorker.onerror = failure;
  transport.onmessageerror = failure;
  transport.onmessage = (event: MessageEvent<Reply>) =>
    requests.get(event.data.id)?.receive(event.data);
  transport.start();
  addEventListener(
    "pagehide",
    () => {
      transport?.postMessage({ kind: "close" });
      transport?.close();
      transport = undefined;
      if (owner instanceof Worker) owner.terminate();
      owner = undefined;
      fileWorker?.terminate();
      fileWorker = undefined;
      failure();
    },
    { once: true },
  );
  return transport;
}
function request(
  command:
    | { kind: "schemas"; tap: string; token: string }
    | {
        kind: "events";
        selection: Selection;
        token: string;
        collection?: string;
      }
    | { kind: "begin"; collection: Collection; token: string }
    | { kind: "restore"; collection?: string; token: string },
  signal: AbortSignal,
  receive: (reply: Reply) => void,
): Promise<void> {
  signal.throwIfAborted();
  const connection = port(),
    id = next++;
  return new Promise((resolve, reject) => {
    const finish = (error?: unknown) => {
      requests.delete(id);
      signal.removeEventListener("abort", cancel);
      error ? reject(error) : resolve();
    };
    const cancel = () => {
      connection.postMessage({ kind: "cancel", id });
      finish(signal.reason ?? new DOMException("Canceled", "AbortError"));
    };
    signal.addEventListener("abort", cancel, { once: true });
    requests.set(id, {
      fail: finish,
      receive(reply) {
        try {
          if (reply.kind === "error") {
            finish(Error(reply.message));
            return;
          }
          if (reply.kind === "done") {
            finish();
            return;
          }
          receive(reply);
          if (reply.kind === "batch") {
            // Release the next batch on a new task, without capping throughput
            // to one network chunk per animation frame. Only one port batch
            // is in flight; the tab already owns these PIL buffers.
            setTimeout(() => {
              if (requests.has(id)) connection.postMessage({ kind: "ack", id });
            });
          }
        } catch (error) {
          connection.postMessage({ kind: "cancel", id });
          finish(error);
        }
      },
    });
    connection.postMessage({ ...command, id } satisfies Request);
  });
}
export async function loadDictionaries(
  tap: string,
  token: string,
  signal: AbortSignal,
): Promise<Choices> {
  let result: Choices | undefined;
  await request({ kind: "schemas", tap, token }, signal, (reply) => {
    if (reply.kind === "schemas")
      result = new Choices(reply.definitions, reply.bindings);
  });
  if (!result) throw Error("Missing PIL schema negotiation");
  return result;
}
export async function streamEvents(
  selection: Selection,
  token: string,
  signal: AbortSignal,
  receive: (events: LogEvent[]) => void,
  connected: () => void,
  onDictionaries?: (dictionaries: Choices) => void,
  collection?: string,
) {
  return receiveEvents(
    { kind: "events", selection, token, collection },
    signal,
    receive,
    connected,
    onDictionaries,
  );
}
async function receiveEvents(
  command:
    | {
        kind: "events";
        selection: Selection;
        token: string;
        collection?: string;
      }
    | { kind: "restore"; token: string; collection?: string },
  signal: AbortSignal,
  receive: (events: LogEvent[]) => void,
  connected: () => void,
  onDictionaries?: (dictionaries: Choices) => void,
  onCollection?: (
    collection: Collection,
    coverage: { from: number; to: number }[],
  ) => void,
) {
  const definitions = new Map<string, Metadata>();
  const streams = new Map<number, Mapping>();
  let headers: string[] = [];
  await request(command, signal, (reply) => {
    if (reply.kind === "collection")
      onCollection?.(reply.collection, reply.coverage);
    if (reply.kind === "schemas") {
      for (const d of reply.definitions) definitions.set(d.digest, d);
      onDictionaries?.(new Choices(reply.definitions, reply.bindings));
    } else if (reply.kind === "definition")
      definitions.set(reply.definition.digest, reply.definition);
    else if (reply.kind === "binding") {
      const b = reply.binding;
      const d = definitions.get(b.digest);
      if (!d) throw Error("Missing PIL dictionary");
      streams.set(b.stream, compile(d, b.tap, headers));
    } else if (reply.kind === "connected") {
      headers = reply.headers;
      connected();
    } else if (reply.kind === "batch") {
      const { bytes, rows } = reply.batch;
      const events: LogEvent[] = [];
      for (let i = 0; i < rows.length; i += 3) {
        const mapping = streams.get(rows[i]);
        if (!mapping) throw Error("Missing PIL stream binding");
        events.push(
          new PILEvent(
            new Uint8Array(bytes, rows[i + 1], rows[i + 2] - rows[i + 1]),
            mapping,
          ),
        );
      }
      receive(events);
    }
  });
  return true;
}

export function beginCollection(
  collection: Collection,
  token: string,
  signal: AbortSignal,
) {
  return request({ kind: "begin", collection, token }, signal, () => {});
}
export function restoreCollection(
  token: string,
  collection: string | undefined,
  signal: AbortSignal,
  receive: (events: LogEvent[]) => void,
  onCollection: (
    collection: Collection,
    coverage: { from: number; to: number }[],
  ) => void,
) {
  return receiveEvents(
    { kind: "restore", token, collection },
    signal,
    receive,
    () => {},
    undefined,
    onCollection,
  );
}
