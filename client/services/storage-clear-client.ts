import type {StorageTarget} from './local-storage';
/** Cleanup owns the same file lease as replication, in a dedicated worker so
 * WebKit's synchronous OPFS interface is available. */
export function clearStorage(target:StorageTarget):Promise<void>{
 return new Promise((resolve,reject)=>{
  const worker=new Worker(new URL('./store/cleanup-worker.ts',import.meta.url),{type:'module'});
  const finish=(error?:Error)=>{worker.terminate();error?reject(error):resolve()};
  worker.onmessage=({data})=>finish(data.error?Error(data.error):undefined);
  worker.onerror=event=>{event.preventDefault();finish(Error(event.message||'Local storage cleanup failed.'))};
  worker.onmessageerror=()=>finish(Error('Could not read the storage cleanup result.'));
  worker.postMessage(target);
 });
}
