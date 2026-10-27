import type { NativeBackend, NativeSource, NativeControl } from './native.ts';
import type { FileIO } from './wasm.ts';
/** One origin lease owns the staged layouts and file handles. Flush the data
 * files once per bounded batch, then atomically publish their layout. No leaf
 * becomes visible before the underlying bytes are durable. */
export async function nativeTransaction<T>(backend:NativeBackend,action:(staged:NativeBackend)=>Promise<T>):Promise<T>{
 const loaded=new Map<string,NativeSource|undefined>(),dirty=new Map<string,NativeSource>();
 const files=new Map<string,{raw:FileIO;borrowed:FileIO;dirty:boolean;users:number}>();
 let control:NativeControl|undefined,changedControl:NativeControl|undefined;
 const staged:NativeBackend={
  ...backend,
  // The backend already gives this transaction detached snapshots. Re-cloning
  // the growing hour directory for every record makes long pulls quadratic.
  async get(key){if(!loaded.has(key))loaded.set(key,await backend.get(key));return loaded.get(key);},
  async list(){for(const source of await backend.list())if(!loaded.has(source.key))loaded.set(source.key,source);return [...loaded.values()].filter((s):s is NativeSource=>!!s);},
  async control(){return structuredClone(changedControl??(control??=await backend.control()));},
  async put(sources,nextControl){for(const source of sources){loaded.set(source.key,source);dirty.set(source.key,source);}if(nextControl)changedControl=structuredClone(nextControl);},
  async file(key,kind,revision=0){
   const id=`${key}:${kind}:${revision}`;let file=files.get(id);
   if(!file){
    // Long collections may cross many branches. Bound open descriptors while
    // keeping completed hourly writes durable before evicting their handles.
    if(files.size>=64){
     const idle=[...files].find(([,entry])=>entry.users===0);
     if(idle){const [key,entry]=idle;if(entry.dirty)entry.raw.flush();entry.raw.close();files.delete(key);}
    }
    const raw=await backend.file(key,kind,revision);
    const entry={raw,borrowed:undefined as unknown as FileIO,dirty:false,users:0};
    let size:number|undefined;
    entry.borrowed={getSize:()=>size??=raw.getSize(),read:(b,o)=>raw.read(b,o),
     write:(b,o)=>{entry.dirty=true;const n=raw.write(b,o);if(size!==undefined)size=Math.max(size,o.at+n);return n;},
     truncate:n=>{entry.dirty=true;raw.truncate(n);size=n;},
     flush:()=>{},close:()=>{entry.users--;}};
    file=entry;files.set(id,file);
   }
   file.users++;return file.borrowed;
  },
  async removeFiles(){throw Error('Reclaim files only after publication');},
  async removeOldStateFiles(){throw Error('Reclaim state only after publication');},
 };
 try{
  const result=await action(staged);
  for(const file of files.values())if(file.dirty)file.raw.flush();
  if(dirty.size||changedControl)await backend.put([...dirty.values()],changedControl);
  return result;
 }finally{for(const file of files.values())file.raw.close();}
}
