import { nativeCacheIssue } from "./errors.ts";
import { createArchive } from "./runtime.ts";
import type { Command } from "./protocol.ts";
const execute = createArchive();
let queue = Promise.resolve();
globalThis.onmessage = (event: MessageEvent) => {
  if (event.data?.kind !== "connect" || !event.ports[0]) return;
  const port = event.ports[0];
  port.onmessage = ({
    data,
  }: {
    data: { id: number; token: string; command: Command };
  }) => {
    queue = queue.then(async () => {
      try {
        if (!navigator.storage?.getDirectory || !navigator.locks)
          throw Error("Local event storage is unavailable in this browser");
        const hash = await crypto.subtle.digest(
          "SHA-256",
          new TextEncoder().encode(data.token),
        );
        const namespace = [...new Uint8Array(hash)]
          .map((n) => n.toString(16).padStart(2, "0"))
          .join("");
        // Serialize file publication across tabs.
        const reply = await navigator.locks.request("trie-trunk-files-v1", () =>
          execute(namespace, data.command),
        );
        port.postMessage(
          { id: data.id, reply },
          reply.batch
            ? [reply.batch.bytes, reply.batch.rows.buffer as ArrayBuffer]
            : reply.nativePage ? [reply.nativePage.rows.buffer as ArrayBuffer,reply.nativePage.population.buffer as ArrayBuffer,reply.nativePage.offsets.buffer as ArrayBuffer] : [],
        );
      } catch (error) {
        port.postMessage({
          id: data.id,
          cacheIssue: nativeCacheIssue(error),
          error: `Local event store: ${error instanceof Error ? error.message : String(error)}`,
        });
      }
    });
  };
  port.start();
};
