// Test-only dedicated worker. Never imported by the application.
import wasmURL from "../../assets/trunk-store.wasm?url";
import { createArchive } from "./runtime.ts";
import { EventIndex } from "./index.ts";
import { nativeFiles } from "./native-files.ts";
import { directory, handle, write, entries } from "./files.ts";
const module = fetch(wasmURL).then((r) => WebAssembly.compileStreaming(r));
let execute = createArchive(module);
let queue = Promise.resolve();
self.onmessage = ({ data }) => {
  queue = queue.then(async () => {
    try {
      const result = await navigator.locks.request(
        "trie-trunk-files-v1",
        async () => {
          if (data.kind === "reload") {
            execute = createArchive(module);
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
          if (data.kind === "inspect") {
            const db = await EventIndex.open(data.namespace);
            try {
              return {
                events: await db.all("events"),
                files: await db.all("files"),
                collections: await db.all("collections"),
                members: await db.all("members"),
              };
            } finally {
              db.close();
            }
          }
          if(data.kind==="compact-native-state"){
            const db=await EventIndex.open(data.namespace);
            try{
              const root=await directory(await navigator.storage.getDirectory(),"trie-trunk-v2",data.namespace);
              const start=performance.now();
              const compacted=await nativeFiles(root,db).compactState(data.source,true);
              return {compacted,elapsedMs:performance.now()-start};
            }finally{db.close();}
          }
          if (data.kind === "fail-publication") {
            const publish = EventIndex.prototype.publish;
            EventIndex.prototype.publish = function (
              records,
              ends,
              collection,
              members,
            ) {
              if (records.length) {
                EventIndex.prototype.publish = publish;
                return Promise.reject(
                  Error("simulated IDB abort after file flush"),
                );
              }
              return publish.call(this, records, ends, collection, members);
            };
            return {};
          }
          if(data.kind==="fail-native-publication"){
            const transaction=EventIndex.prototype.transaction;
            EventIndex.prototype.transaction=function(this:EventIndex,names,mode,action){
              if(mode==="readwrite"&&names.length===1&&names[0]==="state"){
                EventIndex.prototype.transaction=transaction;
                return Promise.reject(Error("simulated native IDB abort after file flush"));
              }
              return transaction.call(this,names,mode,action) as ReturnType<typeof transaction>;
            } as typeof transaction;
            return {};
          }
          if (data.kind === "abort-transaction") {
            const db = await EventIndex.open(data.namespace);
            try {
              await db.transaction(["state"], "readwrite", (tx) => {
                tx.objectStore("state").put(true, "aborted");
                tx.abort();
                return () => {};
              });
            } catch {
              return {
                aborted: (await db.get("state", "aborted")) === undefined,
              };
            } finally {
              db.close();
            }
          }
          return execute(data.namespace, data.command);
        },
      );
      self.postMessage({ id: data.id, result });
    } catch (e) {
      self.postMessage({
        id: data.id,
        error: e instanceof Error ? e.message : String(e),
      });
    }
  });
};
