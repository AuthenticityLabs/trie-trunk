// Test-only dedicated worker. Never imported by the application.
import { createArchive } from "./runtime.ts";
import { nativeBackend } from "./native-files.ts";
import { nativeFiles } from "./native-files.ts";
import { directory, handle, write, entries } from "./files.ts";
import {nativeCacheIssue} from './errors.ts';
let execute = createArchive();
let queue = Promise.resolve();
self.onmessage = ({ data }) => {
  queue = queue.then(async () => {
    try {
      const result = await navigator.locks.request(
        "trie-trunk-files-v1",
        async () => {
          if (data.kind === "reload") {
            execute = createArchive();
            return {};
          }
          if (data.kind === "seed") {
            const root = await navigator.storage.getDirectory();
            for (const [path, b] of data.files as [string, Uint8Array][]) {
              const names = path.split("/"),
                name = names.pop()!,
                dir = await directory(root, ...names),
                f = await handle(dir, name);
              try {
                f.truncate(0);
                write(f, Uint8Array.from(b), 0);
                f.flush();
              } finally {
                f.close();
              }
            }
            return {};
          }
          if (data.kind === "inventory") {
            const result: Record<string, number> = {};
            async function visit(dir: FileSystemDirectoryHandle, path: string) {
              for await (const e of entries(dir)) {
                const p = path + "/" + e.name;
                if (e.kind === "directory")
                  await visit(await dir.getDirectoryHandle(e.name), p);
                else
                  result[p] = (
                    await (await dir.getFileHandle(e.name)).getFile()
                  ).size;
              }
            }
            await visit(await navigator.storage.getDirectory(), "");
            return result;
          }
          if(data.kind==='inspect'||data.kind==='compact-native-state'){
            const root=await directory(await navigator.storage.getDirectory(),'snoot-store',data.namespace);
            if(data.kind==='inspect')return {sources:await nativeBackend(root).list()};
            const start=performance.now(),compacted=await nativeFiles(root).compactState(data.source,true);
            return {compacted,elapsedMs:performance.now()-start};
          }
          if(data.kind==='invalidate-native-span'){
            const root=await directory(await navigator.storage.getDirectory(),'snoot-store',data.namespace);
            const backend=nativeBackend(root),source=await backend.get(data.source);
            if(!source?.contexts?.length)throw Error('Missing test PAC context');
            source.contexts[0].placement.memberStart=0n;
            await backend.put([source]);return {};
          }
          if(data.kind==='fail-native-publication'){
            const original=FileSystemFileHandle.prototype.createWritable;
            FileSystemFileHandle.prototype.createWritable=async function(options){
              const writer=await original.call(this,options);
              if(this.name==='commit.pil'){
                FileSystemFileHandle.prototype.createWritable=original;
                writer.close=async()=>{await writer.abort();throw Error('simulated file commit failure');};
              }
              return writer;
            };
            return {};
          }
          return execute(data.namespace, data.command);
        },
      );
      self.postMessage({ id: data.id, result });
    } catch (e) {
      self.postMessage({
        id: data.id,
        error: e instanceof Error ? e.message : String(e),
        cacheIssue: nativeCacheIssue(e),
      });
    }
  });
};
