import { restoreCacheError } from "./errors.ts";
import type { Archive } from "./protocol.ts";
// The tab transfers this channel between the SharedWorker and its dedicated
// file worker. PIL batches never pass through the UI on their way to storage.
export function storagePort(port: MessagePort): Archive {
  let next = 0;
  const pending = new Map<
    number,
    { resolve: (value: any) => void; reject: (reason: any) => void }
  >();
  port.onmessage = ({ data }) => {
    const job = pending.get(data.id);
    if (!job) return;
    pending.delete(data.id);
    data.error ? job.reject(restoreCacheError(data.error,data.cacheIssue)) : job.resolve(data.reply);
  };
  port.start();
  let closed = false;
  const archive: Archive = (token, command) =>
    new Promise((resolve, reject) => {
      if (closed) {
        reject(Error("Local storage connection closed"));
        return;
      }
      const id = ++next;
      pending.set(id, { resolve, reject });
      port.postMessage(
        { id, token, command },
        command.kind === "save"
          ? [command.batch.bytes, command.batch.rows.buffer as ArrayBuffer]
          : [],
      );
    });
  archive.close = () => {
    closed = true;
    for (const job of pending.values())
      job.reject(Error("Local storage connection closed"));
    pending.clear();
    port.close();
  };
  return archive;
}
