import {readPublishedFile} from './publication.ts';
import { decodeObject, encodeObject } from "../pil/control.ts";
import { NativeStore, type NativeBackend, type NativeSource, type NativeControl } from './native.ts';
import { directory, handle, entries } from './files.ts';
import {timeFileRanges,type ClientTimeTrie,type TimeRange} from './time-trie.ts';

/** Bounded decoded immutable revisions. The commit root is re-read under every
 * lease; a different directory identity or revision can never reuse a snapshot. */
export class NativeSnapshotCache {
 private entries=new Map<string,{source:NativeSource;size:number}>();private size=0;
 readonly limit:number;
 constructor(limit=8*1024*1024){this.limit=limit;}
 get(key:string){const entry=this.entries.get(key);if(entry){this.entries.delete(key);this.entries.set(key,entry);}return entry?.source;}
 put(key:string,source:NativeSource,size:number){
  const old=this.entries.get(key);if(old){this.size-=old.size;this.entries.delete(key);}
  if(size>this.limit)return;
  this.entries.set(key,{source,size});this.size+=size;
  while(this.size>this.limit){const first=this.entries.keys().next().value!;this.size-=this.entries.get(first)!.size;this.entries.delete(first);}
 }
 retain(sources:Record<string,number>){for(const [id,entry] of this.entries){const at=id.lastIndexOf(':');if(sources[id.slice(0,at)]!==Number(id.slice(at+1))){this.entries.delete(id);this.size-=entry.size;}}}
}
type Commit = { serial:number; sources:Record<string,number>; control:NativeControl };
const empty = ():Commit=>({serial:0,sources:{},control:{serial:0,scopes:{}}});
const optional=readPublishedFile;
export {atomicFile} from './atomic-file.ts';
import {atomicFile,atomicReplace} from './atomic-file.ts';
async function atomicTimeFile(dir:FileSystemDirectoryHandle,name:string,tree:ClientTimeTrie|undefined){
 return tree?atomicReplace(dir,name,tree.bytes.length,timeFileRanges(tree)):atomicFile(dir,name,new Uint8Array());
}
/** The surrounding worker holds the origin-wide file lease. Immutable branch
 * snapshots are published together by one atomic commit-root replacement.
 * Create a backend per leased command; commit roots never cross a lease.
 * Only immutable decoded snapshots can be reused after revision checks.
 * branch.tri is the raw time trie; layout.pil stores allocation and decoding
 * context only. No package manifests or duplicate coverage ranges are retained. */
export function nativeBackend(root:FileSystemDirectoryHandle,cache?:NativeSnapshotCache):NativeBackend {
 let committed:Promise<Commit>|undefined;
 const load=():Promise<Commit>=>committed??=(async()=>{const raw=await optional(root,'commit.pil'),state=raw?.length?decodeObject(raw) as Commit:empty();cache?.retain(state.sources);return state;})();
 const dirs=new Map<string,Promise<FileSystemDirectoryHandle>>();
 const branch=(key:string)=>{if(!/^[a-f0-9]{64}$/.test(key))throw Error('Invalid native source');let dir=dirs.get(key);if(!dir){dir=directory(root,'branches',key);dirs.set(key,dir);}return dir;};
 const snapshots=new Map<string,Promise<NativeSource>>();
 const readFileSource=async(key:string,revision:number):Promise<NativeSource>=>{
  const cached=cache?.get(`${key}:${revision}`);if(cached)return cached;
  const dir=await branch(key),raw=await optional(dir,`layout.${revision}.pil`);
  if(!raw)throw Error('Missing committed branch layout');
  const {timeRanges,...source}=decodeObject(raw) as NativeSource & {timeRanges?:TimeRange[]};
  if(source.key!==key)throw Error('Branch identity mismatch');
  const trie=await optional(dir,`branch.${revision}.tri`);if(!trie)throw Error('Missing committed time trie');if(trie.length)source.timeTrie={bytes:trie,ranges:timeRanges??[]};
  for(const context of source.contexts??[]){const p=context.placement;
   for(const name of ['from','to','firstRow','rows','memberStart','memberBytes','retiredRows','retiredMembers','retiredPods'] as const)
    if(name in p)(p as unknown as Record<string,unknown>)[name]=BigInt((p as unknown as Record<string,number|bigint>)[name]);
   for(const page of p.pages){page.row=BigInt(page.row);page.page=BigInt(page.page);}
  }
  cache?.put(`${key}:${revision}`,source,raw.byteLength*4+(trie?.byteLength??0)+1024);
  return source;
 };
 const read=(key:string,revision:number)=>{const id=`${key}:${revision}`;let source=snapshots.get(id);if(!source){source=readFileSource(key,revision);snapshots.set(id,source);}return source.then(s=>structuredClone(s));};
 return {
  async get(key){const state=await load();return state.sources[key]===undefined?undefined:read(key,state.sources[key]);},
  async list(){const state=await load();return Promise.all(Object.entries(state.sources).map(([key,n])=>read(key,n)));},
  async control(){return structuredClone((await load()).control); },
  async put(sources,control){
   const old=await load(),next:Commit={serial:old.serial+1,sources:{...old.sources},control:control??old.control};
   if(!Number.isSafeInteger(next.serial))throw Error('Local commit sequence exhausted');
   for(const source of sources){
    const dir=await branch(source.key),{timeTrie,...layout}=source;
    await atomicFile(dir,`layout.${next.serial}.pil`,encodeObject({...layout,...(timeTrie?{timeRanges:timeTrie.ranges}:{})}));
    await atomicTimeFile(dir,`branch.${next.serial}.tri`,timeTrie);
    next.sources[source.key]=next.serial;
   }
   await atomicFile(root,'commit.pil',encodeObject(next));
   committed=Promise.resolve(next);
   for(const source of sources)snapshots.set(`${source.key}:${next.serial}`,Promise.resolve(structuredClone(source)));
   // Only remove unreachable metadata after publication. Data-file reclamation
   // remains ordered by the selected branch state.
   for(const source of sources){
    const dir=await branch(source.key),keep=next.sources[source.key];
    for await(const entry of entries(dir))if(/^(layout\.\d+\.pil|branch\.\d+\.tri)$/.test(entry.name)&&entry.name!==`layout.${keep}.pil`&&entry.name!==`branch.${keep}.tri`)
     await dir.removeEntry(entry.name).catch(()=>{});
   }
  },
  async file(key,kind,revision=0){
   const names={map:'resource.rx',trie:'state.tri',state:'collection.state',index:'events.idx',sods:'events.sods',sum:'branch.sum'};
   return handle(await branch(key),names[kind]+(kind==='state'&&revision?`.${revision}`:''));
  },
  async removeFiles(key){const dir=await branch(key);for await(const entry of entries(dir))if(!/^(layout\.\d+\.pil|branch\.\d+\.tri)$/.test(entry.name))await dir.removeEntry(entry.name,{recursive:true});},
  async removeOldStateFiles(key,revision){const dir=await branch(key),keep=revision?`collection.state.${revision}`:'collection.state';for await(const entry of entries(dir))if(/^collection\.state(?:\.\d+)?$/.test(entry.name)&&entry.name!==keep)await dir.removeEntry(entry.name);},
 };
}
export const nativeFiles=(root:FileSystemDirectoryHandle)=>new NativeStore(nativeBackend(root));
