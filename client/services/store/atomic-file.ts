import {write,describeFileIO} from './files.ts';
import {storageStep} from './io-error.ts';
import {publishFile,recoverPublication} from './publication.ts';
import type {FileIO} from './wasm.ts';
let syncWrites=false;
export function useSyncFileWrites(){syncWrites=true}
type SyncFile=FileSystemFileHandle & {createSyncAccessHandle:()=>Promise<FileIO>;move:(parent:FileSystemDirectoryHandle,name:string)=>Promise<void>};
// WebKit's asynchronous OPFS writer can consume a view's entire backing
// buffer. Give it an exact buffer boundary; whole owned arrays stay zero-copy.
function writableBytes(bytes:Uint8Array):Uint8Array<ArrayBuffer>{
 return bytes.byteOffset===0&&bytes.byteLength===bytes.buffer.byteLength&&bytes.buffer instanceof ArrayBuffer
  ?bytes as Uint8Array<ArrayBuffer>:bytes.slice();
}
/** Publish only after every byte has been flushed. Never overwrite the live
 * commit in place: interrupted writes must leave the previous root readable. */
export async function atomicReplace(dir:FileSystemDirectoryHandle,name:string,size:number,parts:Iterable<{at:number;bytes:Uint8Array}>){
 if(!syncWrites&&(typeof FileSystemFileHandle==='undefined'||typeof FileSystemFileHandle.prototype.createWritable==='function')){
  await recoverPublication(dir,name);
  const file=await storageStep(`Open Local File ${name}`,()=>dir.getFileHandle(name,{create:true})),writer=await storageStep(`Open Local Writer ${name}`,()=>file.createWritable());
  try{await storageStep(`Resize Local File ${name} (${size} bytes)`,()=>writer.truncate(size));for(const part of parts)await storageStep(`Write Local File ${name} (${part.bytes.length} bytes at ${part.at})`,()=>writer.write({type:'write',position:part.at,data:writableBytes(part.bytes)}));await storageStep(`Commit Local File ${name}`,()=>writer.close())}
  catch(error){await writer.abort().catch(()=>{});throw error}return;
 }
 const temporary=`.pending-${crypto.randomUUID()}`;
 let created=false,published=false,failed=false;
 try{
  const file=await storageStep(`Create Temporary Local File for ${name}`,()=>dir.getFileHandle(temporary,{create:true})) as SyncFile;
  created=true;
  if(typeof file.createSyncAccessHandle!=='function'||typeof file.move!=='function')throw Error('Atomic local writes require a dedicated storage worker with file move support.');
  const handle=describeFileIO(await storageStep(`Open Local File Access for ${name}`,()=>file.createSyncAccessHandle()),name);
  try{handle.truncate(size);for(const part of parts)write(handle,part.bytes,part.at);handle.flush()}finally{handle.close()}
  await publishFile(dir,file,name);
  published=true;
 }catch(error){failed=true;throw error}
 finally{
  // move already removed the temporary name. Cleanup must neither fail an
  // already-published commit nor mask the original write/rename failure.
  if(created&&!published)await storageStep(`Remove Temporary Local File for ${name}`,()=>dir.removeEntry(temporary)).catch(error=>{
   if(!failed&&!(error instanceof DOMException&&error.name==='NotFoundError'))throw error;
  });
 }
}
export async function atomicFile(dir:FileSystemDirectoryHandle,name:string,bytes:Uint8Array){
 if(!syncWrites&&(typeof FileSystemFileHandle==='undefined'||typeof FileSystemFileHandle.prototype.createWritable==='function')){
  await recoverPublication(dir,name);
  const file=await storageStep(`Open Local File ${name}`,()=>dir.getFileHandle(name,{create:true})),writer=await storageStep(`Open Local Writer ${name}`,()=>file.createWritable());
  try{await storageStep(`Write Local File ${name} (${bytes.length} bytes)`,()=>writer.write(writableBytes(bytes)));await storageStep(`Commit Local File ${name}`,()=>writer.close())}catch(error){await writer.abort().catch(()=>{});throw error}return;
 }
 return atomicReplace(dir,name,bytes.length,[{at:0,bytes}]);
}
