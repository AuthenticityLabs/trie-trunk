import {storageStep} from './io-error.ts';

type Movable = FileSystemFileHandle & {move(parent:FileSystemDirectoryHandle,name:string):Promise<void>};
const previousName=(name:string)=>`.previous-${name}`;
async function optional(dir:FileSystemDirectoryHandle,name:string):Promise<Movable|undefined>{
 try{return await dir.getFileHandle(name) as Movable}
 catch(error){if(error instanceof DOMException&&error.name==='NotFoundError')return;throw error}
}
/** All publication and reads run under the origin's storage Web Lock. The
 * previous name is a recovery record, not an alternate storage format. */
export async function recoverPublication(dir:FileSystemDirectoryHandle,name:string):Promise<FileSystemFileHandle|undefined>{
 const current=await optional(dir,name);
 if(current)return current;
 const previous=await optional(dir,previousName(name));
 if(!previous)return;
 await storageStep(`Recover Local File ${name}`,()=>previous.move(dir,name));
 return previous;
}
export async function readPublishedFile(dir:FileSystemDirectoryHandle,name:string):Promise<Uint8Array|undefined>{
 return storageStep(`Read Local File ${name}`,async()=>{
  const file=await recoverPublication(dir,name);
  return file?new Uint8Array(await(await file.getFile()).arrayBuffer()):undefined;
 });
}
export async function publishFile(dir:FileSystemDirectoryHandle,file:Movable,name:string){
 const previous=previousName(name),current=await recoverPublication(dir,name);
 // A prior successful commit may have been interrupted before cleanup. Never
 // move onto an occupied name: older WebKit cannot reliably replace it.
 if(await optional(dir,previous))await storageStep(`Remove Previous Local File ${name}`,()=>dir.removeEntry(previous));
 if(current)await storageStep(`Preserve Local File ${name}`,()=>(current as Movable).move(dir,previous));
 try{await storageStep(`Publish Local File ${name}`,()=>file.move(dir,name))}
 catch(error){
  // If recovery also fails, leave the previous file intact for the next open.
  await recoverPublication(dir,name).catch(()=>{});
  throw error;
 }
 // Publication already succeeded. A cleanup error must not report a failed
 // transaction; the next publication reclaims this one bounded recovery file.
 if(current)await dir.removeEntry(previous).catch(()=>{});
}
export async function recoverPublications(dir:FileSystemDirectoryHandle){
 const names:string[]=[];
 for await(const entry of (dir as FileSystemDirectoryHandle & {values():AsyncIterable<FileSystemHandle>}).values()){
  if(entry.kind==='file'&&entry.name.startsWith('.previous-'))names.push(entry.name.slice(10));
 }
 for(const name of names){
  await recoverPublication(dir,name);
  // The live name is now authoritative, whether restored or newly published.
  await dir.removeEntry(previousName(name)).catch(error=>{if(!(error instanceof DOMException&&error.name==='NotFoundError'))throw error});
 }
}
export async function removePublishedFile(dir:FileSystemDirectoryHandle,name:string){
 // Remove recovery first so explicitly deleted metadata cannot be resurrected.
 for(const entry of [previousName(name),name]){
  try{await dir.removeEntry(entry,{recursive:true})}
  catch(error){if(!(error instanceof DOMException&&error.name==='NotFoundError'))throw error}
 }
}
