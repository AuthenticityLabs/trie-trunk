import {readPublishedFile} from './publication.ts';
import { decodeObject, encodeObject } from "../pil/control.ts";
import { NativeStore, type NativeBackend, type NativeSource, type NativeControl } from './native.ts';
import { directory, handle, entries } from './files.ts';
import {timeFileRanges,type ClientTimeTrie,type TimeRange} from './time-trie.ts';

/** Bounded decoded immutable revisions. The commit root is re-read under every
 * lease; a different directory identity or revision can never reuse a snapshot. */
/** One bounded LRU budget shared by connection-specific caches. */
export class NativeSnapshotPool {
 private entries=new Map<string,{source:NativeSource;size:number}>();private size=0;
 readonly limit:number;
 constructor(limit:number){this.limit=limit;}
 get(key:string){const entry=this.entries.get(key);if(entry){this.entries.delete(key);this.entries.set(key,entry);}return entry?.source;}
 put(key:string,source:NativeSource,size:number){
  this.remove(key);
  if(size>this.limit)return;
  this.entries.set(key,{source,size});this.size+=size;
  while(this.size>this.limit)this.remove(this.entries.keys().next().value!);
 }
 private remove(key:string){const old=this.entries.get(key);if(old){this.size-=old.size;this.entries.delete(key);}}
 retain(prefix:string,sources?:Record<string,number>){
  for(const id of this.entries.keys())if(id.startsWith(prefix)){
   const key=id.slice(prefix.length),at=key.lastIndexOf(':');
   if(!sources||sources[key.slice(0,at)]!==Number(key.slice(at+1)))this.remove(id);
  }
 }
}
let nextSnapshotScope=0;
export class NativeSnapshotCache {
 private pool:NativeSnapshotPool;private prefix=`${++nextSnapshotScope}/`;
 readonly limit:number;
 constructor(budget:number|NativeSnapshotPool=8*1024*1024){this.pool=typeof budget==='number'?new NativeSnapshotPool(budget):budget;this.limit=this.pool.limit;}
 get(key:string){return this.pool.get(this.prefix+key);}
 put(key:string,source:NativeSource,size:number){this.pool.put(this.prefix+key,source,size);}
 retain(sources:Record<string,number>){this.pool.retain(this.prefix,sources);}
 clear(){this.pool.retain(this.prefix);}
}
type Commit = { serial:number; sources:Record<string,number>; control:NativeControl };
const empty = ():Commit=>({serial:0,sources:{},control:{serial:0,scopes:{}}});
const optional=readPublishedFile;
// Compare detached working metadata with the immutable committed snapshot.
// Avoid serializing unchanged hour directories just to discover that no new
// bytes or completion flags were published. This never compares event payloads.
function sameMetadata(a:unknown,b:unknown):boolean {
 if(a===b)return true;
 if(a===null||b===null||typeof a!=='object'||typeof b!=='object')return false;
 if(a instanceof Uint8Array||b instanceof Uint8Array){
  if(!(a instanceof Uint8Array)||!(b instanceof Uint8Array)||a.length!==b.length)return false;
  for(let i=0;i<a.length;i++)if(a[i]!==b[i])return false;
  return true;
 }
 if(Array.isArray(a)||Array.isArray(b)){
  if(!Array.isArray(a)||!Array.isArray(b)||a.length!==b.length)return false;
  for(let i=0;i<a.length;i++)if(!sameMetadata(a[i],b[i]))return false;
  return true;
 }
 const ap=Object.getPrototypeOf(a),bp=Object.getPrototypeOf(b);
 if(ap!==null&&ap!==Object.prototype||bp!==null&&bp!==Object.prototype)return false;
 const left=a as Record<string,unknown>,right=b as Record<string,unknown>;
 for(const key in left)if(Object.hasOwn(left,key)&&(!Object.hasOwn(right,key)||!sameMetadata(left[key],right[key])))return false;
 for(const key in right)if(Object.hasOwn(right,key)&&!Object.hasOwn(left,key))return false;
 return true;
}
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
 const snapshot=(key:string,revision:number)=>{const id=`${key}:${revision}`;let source=snapshots.get(id);if(!source){source=readFileSource(key,revision);snapshots.set(id,source);}return source;};
 const read=(key:string,revision:number)=>snapshot(key,revision).then(s=>structuredClone(s));
 return {
  async get(key){const state=await load();return state.sources[key]===undefined?undefined:read(key,state.sources[key]);},
  async list(){const state=await load();return Promise.all(Object.entries(state.sources).map(([key,n])=>read(key,n)));},
  async control(){return structuredClone((await load()).control); },
  async put(sources,control){
   const old=await load(),changed:NativeSource[]=[];
   for(const source of sources){
    const revision=old.sources[source.key];
    if(revision===undefined||!sameMetadata(source,await snapshot(source.key,revision)))changed.push(source);
   }
   if(!changed.length&&(!control||sameMetadata(control,old.control)))return;
   const next:Commit={serial:old.serial+1,sources:{...old.sources},control:control??old.control};
   if(!Number.isSafeInteger(next.serial))throw Error('Local commit sequence exhausted');
   const weights=new Map<string,number>();
   for(const source of changed){
    const dir=await branch(source.key),{timeTrie,...layout}=source;
    const bytes=encodeObject({...layout,...(timeTrie?{timeRanges:timeTrie.ranges}:{})});
    await atomicFile(dir,`layout.${next.serial}.pil`,bytes);
    await atomicTimeFile(dir,`branch.${next.serial}.tri`,timeTrie);
    weights.set(source.key,bytes.byteLength*4+(timeTrie?.bytes.byteLength??0)+1024);
    next.sources[source.key]=next.serial;
   }
   await atomicFile(root,'commit.pil',encodeObject(next));
   committed=Promise.resolve(next);
   for(const source of changed){
    const id=`${source.key}:${next.serial}`,saved=structuredClone(source);
    snapshots.set(id,Promise.resolve(saved));cache?.put(id,saved,weights.get(source.key)!);
   }
   // Only remove unreachable metadata after publication. Data-file reclamation
   // remains ordered by the selected branch state.
   for(const source of changed){
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
