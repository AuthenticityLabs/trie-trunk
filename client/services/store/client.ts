import { restoreCacheError } from "./errors.ts";
import type { Archive, Command } from "./protocol.ts";
// The tab transfers this channel between the SharedWorker and its dedicated
// file worker. PIL batches never pass through the UI on their way to storage.
// Native PAC buffers are owned by the decoder's delivery callback. A queued
// storage command consumes them; shared definitions/index chunks remain borrowed.
export function storageTransfers(command:Command):ArrayBuffer[]{
 if(command.kind==='save')return [command.batch.bytes,command.batch.rows.buffer as ArrayBuffer];
 if(command.kind!=='native-records'&&command.kind!=='native-finish')return [];
 const owned=new Set<ArrayBuffer>();
 for(const {record} of command.records??[])if(record.kind==='pac')for(const bytes of [record.bytes,record.bounds]){
  if(bytes.buffer instanceof ArrayBuffer&&bytes.byteOffset===0&&bytes.byteLength===bytes.buffer.byteLength)owned.add(bytes.buffer);
 }
 return [...owned];
}
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
        storageTransfers(command),
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
