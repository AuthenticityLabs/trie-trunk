import { NativeStore, type NativeBackend, type NativeSource, type NativeControl } from "./native.ts";
import { EventIndex, type SavedCollection } from "./index.ts";
import { directory, handle, entries } from "./files.ts";
import { Coverage } from "../coverage.ts";

// The caller holds the same navigator.locks lease as legacy archive commands.
// Metadata records contain extents and coverage, never event values or UUIDs.
export function nativeFiles(root: FileSystemDirectoryHandle, db: EventIndex) {
  const backend: NativeBackend = {
    get: key=>db.get<NativeSource>("state",["native",key]),
    control: async()=>await db.get<NativeControl>("state",["native-control"])??{serial:0,scopes:{}},
    list: ()=>db.transaction(["state"],"readonly",tx=>{
      const request=tx.objectStore("state").getAll(IDBKeyRange.bound(["native"],["native",[]],false,true));
      return ()=>request.result as NativeSource[];
    }),
    put: (sources,control)=>{
      const retired=new Set(sources.filter(s=>s.retiredBy&&!s.reclaimed).map(s=>s.key));
      return db.transaction(retired.size?["state","collections"]:["state"],"readwrite",tx=>{
      for(const source of sources)tx.objectStore("state").put(source,["native",source.key]);
      if(control)tx.objectStore("state").put(control,["native-control"]);
      if(retired.size){
        const request=tx.objectStore("collections").getAll();
        request.onsuccess=()=>{
          for(const saved of request.result as SavedCollection[]){
            if(!saved.native?.some(s=>retired.has(s.source)))continue;
            saved.native=saved.native.filter(s=>!retired.has(s.source));
            const coverage=new Coverage();
            for(const s of saved.native)coverage.add({from:s.from,to:s.to});
            saved.coverage=[...coverage.intervals()];
            tx.objectStore("collections").put(saved,saved.collection.id);
          }
        };
      }
      return ()=>{};
      });
    },
    removeFiles: async key=>{
      if(!/^[a-f0-9]{64}$/.test(key))throw Error("Invalid native source");
      try{await (await directory(root,"native")).removeEntry(key,{recursive:true});}
      catch(error){if(!(error instanceof DOMException)||error.name!=="NotFoundError")throw error;}
    },
    removeOldStateFiles: async(key,keepRevision)=>{
      if(!/^[a-f0-9]{64}$/.test(key)||!Number.isSafeInteger(keepRevision)||keepRevision<0)throw Error("Invalid native state revision");
      const dir=await directory(root,"native",key),keep=keepRevision?`collection.state.${keepRevision}`:"collection.state";
      for await(const entry of entries(dir))if(entry.kind==="file"&&/^collection\.state(?:\.[1-9][0-9]*)?$/.test(entry.name)&&entry.name!==keep)await dir.removeEntry(entry.name);
    },
    file: async(key,kind,revision=0)=>{
      if(!/^[a-f0-9]{64}$/.test(key))throw Error("Invalid native source");
      if(!Number.isSafeInteger(revision)||revision<0)throw Error("Invalid native state revision");
      const names={map:"resource.map",trie:"resource.tri",state:"collection.state",index:"events.idx",population:"events.sods"};
      return handle(await directory(root,"native",key),names[kind]+(kind==="state"&&revision?`.${revision}`:""));
    },
  };
  return new NativeStore(backend);
}
