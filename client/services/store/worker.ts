import wasmURL from "../../assets/trunk-store.wasm?url";
import { createArchive } from "./runtime.ts";
import type { Command } from "./protocol.ts";
const modulePromise = fetch(wasmURL).then((r) => {
  if (!r.ok) throw Error("Could not load local store codec");
  return WebAssembly.compileStreaming(r);
});
modulePromise.catch(() => {});
const execute = createArchive(modulePromise);
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
        // Keep the old lock name: migration must also exclude older bundles.
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
          error: `Local event store: ${error instanceof Error ? error.message : String(error)}`,
        });
      }
    });
  };
  port.start();
};
