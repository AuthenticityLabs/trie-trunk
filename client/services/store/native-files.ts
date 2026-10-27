import { NativeStore, type NativeBackend, type NativeSource, type NativeControl } from './native.ts';
import { cbor, encodeCBOR } from '../pil/cbor.ts';
import { directory, handle, entries } from './files.ts';

type Commit = { serial:number; sources:Record<string,number>; control:NativeControl };
const empty = ():Commit=>({serial:0,sources:{},control:{serial:0,scopes:{}}});
const optional=async(dir:FileSystemDirectoryHandle,name:string):Promise<Uint8Array|undefined>=>{
 try{return new Uint8Array(await (await (await dir.getFileHandle(name)).getFile()).arrayBuffer());}
 catch(e){if(e instanceof DOMException&&e.name==='NotFoundError')return;throw e;}
};
export async function atomicFile(dir:FileSystemDirectoryHandle,name:string,bytes:Uint8Array){
 const file=await dir.getFileHandle(name,{create:true}), writer=await file.createWritable();
 try{await writer.write(bytes as Uint8Array<ArrayBuffer>);await writer.close();}
 catch(e){await writer.abort().catch(()=>{});throw e;}
}
/** The surrounding worker holds the origin-wide file lease. Immutable branch
 * snapshots are published together by one atomic commit-root replacement.
 * Create a backend per leased command; cached roots never cross a lease.
 * branch.tri is the raw time trie; layout.cbor stores allocation and decoding
 * context only. No package manifests or duplicate coverage ranges are retained. */
export function nativeBackend(root:FileSystemDirectoryHandle):NativeBackend {
 let committed:Promise<Commit>|undefined;
 const load=():Promise<Commit>=>committed??=(async()=>{const raw=await optional(root,'commit.cbor');return raw?.length?cbor(raw) as Commit:empty();})();
 const dirs=new Map<string,Promise<FileSystemDirectoryHandle>>();
 const branch=(key:string)=>{if(!/^[a-f0-9]{64}$/.test(key))throw Error('Invalid native source');let dir=dirs.get(key);if(!dir){dir=directory(root,'branches',key);dirs.set(key,dir);}return dir;};
 const snapshots=new Map<string,Promise<NativeSource>>();
 const readFileSource=async(key:string,revision:number):Promise<NativeSource>=>{
  const dir=await branch(key),raw=await optional(dir,`layout.${revision}.cbor`);
  if(!raw)throw Error('Missing committed branch layout');
  const source=cbor(raw) as NativeSource;
  if(source.key!==key)throw Error('Branch identity mismatch');
  const trie=await optional(dir,`branch.${revision}.tri`);if(!trie)throw Error('Missing committed time trie');if(trie.length)source.timeTrie=trie;
  for(const context of source.contexts??[]){const p=context.placement;
   for(const name of ['from','to','firstRow','rows','memberStart','memberBytes','retiredRows','retiredMembers','retiredPopulation'] as const)
    if(name in p)(p as unknown as Record<string,unknown>)[name]=BigInt((p as unknown as Record<string,number|bigint>)[name]);
   for(const page of p.pages){page.row=BigInt(page.row);page.page=BigInt(page.page);}
  }
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
    await atomicFile(dir,`layout.${next.serial}.cbor`,encodeCBOR(layout));
    await atomicFile(dir,`branch.${next.serial}.tri`,timeTrie??new Uint8Array());
    next.sources[source.key]=next.serial;
   }
   await atomicFile(root,'commit.cbor',encodeCBOR(next));
   committed=Promise.resolve(next);
   for(const source of sources)snapshots.set(`${source.key}:${next.serial}`,Promise.resolve(structuredClone(source)));
   // Only remove unreachable metadata after publication. Data-file reclamation
   // remains ordered by the selected branch state.
   for(const source of sources){
    const dir=await branch(source.key),keep=next.sources[source.key];
    for await(const entry of entries(dir))if(/^(layout\.\d+\.cbor|branch\.\d+\.tri)$/.test(entry.name)&&entry.name!==`layout.${keep}.cbor`&&entry.name!==`branch.${keep}.tri`)
     await dir.removeEntry(entry.name).catch(()=>{});
   }
  },
  async file(key,kind,revision=0){
   const names={map:'resource.map',trie:'resource.tri',state:'collection.state',index:'events.idx',population:'events.sods'};
   return handle(await branch(key),names[kind]+(kind==='state'&&revision?`.${revision}`:''));
  },
  async removeFiles(key){const dir=await branch(key);for await(const entry of entries(dir))if(!/^(layout\.\d+\.cbor|branch\.\d+\.tri)$/.test(entry.name))await dir.removeEntry(entry.name,{recursive:true});},
  async removeOldStateFiles(key,revision){const dir=await branch(key),keep=revision?`collection.state.${revision}`:'collection.state';for await(const entry of entries(dir))if(/^collection\.state(?:\.\d+)?$/.test(entry.name)&&entry.name!==keep)await dir.removeEntry(entry.name);},
 };
}
export const nativeFiles=(root:FileSystemDirectoryHandle)=>new NativeStore(nativeBackend(root));
