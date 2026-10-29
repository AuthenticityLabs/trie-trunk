import {write} from './files.ts';
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
  const file=await dir.getFileHandle(name,{create:true}),writer=await file.createWritable();
  try{await writer.truncate(size);for(const part of parts)await writer.write({type:'write',position:part.at,data:writableBytes(part.bytes)});await writer.close()}
  catch(error){await writer.abort().catch(()=>{});throw error}return;
 }
 const temporary=`.pending-${crypto.randomUUID()}`;
 try{
  const file=await dir.getFileHandle(temporary,{create:true}) as SyncFile;
  if(typeof file.createSyncAccessHandle!=='function'||typeof file.move!=='function')throw Error('Atomic local writes require a dedicated storage worker with file move support.');
  const handle=await file.createSyncAccessHandle();
  try{handle.truncate(size);for(const part of parts)write(handle,part.bytes,part.at);handle.flush()}finally{handle.close()}
  await file.move(dir,name);
 }finally{await dir.removeEntry(temporary).catch(error=>{if(!(error instanceof DOMException&&error.name==='NotFoundError'))throw error})}
}
export async function atomicFile(dir:FileSystemDirectoryHandle,name:string,bytes:Uint8Array){
 if(!syncWrites&&(typeof FileSystemFileHandle==='undefined'||typeof FileSystemFileHandle.prototype.createWritable==='function')){
  const file=await dir.getFileHandle(name,{create:true}),writer=await file.createWritable();
  try{await writer.write(writableBytes(bytes));await writer.close()}catch(error){await writer.abort().catch(()=>{});throw error}return;
 }
 return atomicReplace(dir,name,bytes.length,[{at:0,bytes}]);
}
