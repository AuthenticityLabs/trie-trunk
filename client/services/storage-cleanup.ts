import { transact } from './database.ts';
export const profileDatabase='snoot-profile';
/** Remove the abandoned event database. Preserve explicit user preferences only. */
export async function clearObsoleteStorage():Promise<void>{
 for(const info of await indexedDB.databases()){
  if(!info.name||!(info.name==='snoot'||info.name.startsWith('snoot-events-')))continue;
  if(info.name==='snoot'){
   const old=await new Promise<IDBDatabase>((resolve,reject)=>{const r=indexedDB.open(info.name!);r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error);});
   try{
    if(old.objectStoreNames.contains('preferences')){
     const settings=await new Promise<[IDBValidKey,unknown][]>((resolve,reject)=>{
      const tx=old.transaction('preferences','readonly'),s=tx.objectStore('preferences'),keys=s.getAllKeys(),values=s.getAll();
      tx.oncomplete=()=>resolve(keys.result.map((k,i)=>[k,values.result[i]]));tx.onabort=()=>reject(tx.error);
     });
     for(const [key,value] of settings){
      if(key==='theme')await transact('preferences','readwrite',s=>s.put(value,key));
      else if(typeof key==='string'&&key.startsWith('dashboard-services:')){
       const v=value as {revision:number;subscriptions:unknown[]};
       await transact('preferences','readwrite',s=>s.put({revision:v.revision,subscriptions:v.subscriptions},key));
      }
     }
    }
    if(old.objectStoreNames.contains('indicators')){
     const rules=await new Promise<unknown[]>((resolve,reject)=>{const r=old.transaction('indicators','readonly').objectStore('indicators').getAll();r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error);});
     if(rules.length)await transact('preferences','readwrite',s=>s.put(rules,'event-highlights'));
    }
   }finally{old.close();}
  }
  await new Promise<void>((resolve,reject)=>{const r=indexedDB.deleteDatabase(info.name!);r.onsuccess=()=>resolve();r.onerror=()=>reject(r.error);r.onblocked=()=>reject(Error('Close older Snoot tabs, then reload to remove obsolete storage.'));});
 }
 // Rename UI settings without retaining binary result checkpoints.
 for(const key of Object.keys(sessionStorage)){
  let name:string|undefined;
  if(key==='snoot-tab-v1')name='snoot:workspace';
  else if(key==='snoot-collection')name='snoot:search:collection';
  else if(key.startsWith('snoot-alert-tab:'))name=key.replace('snoot-alert-tab:','snoot:alerts:view:');
  else if(key.startsWith('snoot-research:')){const parts=key.slice('snoot-research:'.length).split(':');const mode=parts.pop()!;name=`snoot:${mode==='Object History'?'collections':mode.toLowerCase()}:workspace:${parts.join(':')}`;}
  if(name){if(sessionStorage.getItem(name)===null)sessionStorage.setItem(name,sessionStorage.getItem(key)!);sessionStorage.removeItem(key);}
  else if(key==='snoot-search-v0'||key.startsWith('snoot-search-v0:'))sessionStorage.removeItem(key);
 }
 localStorage.removeItem('snoot.querySyntax');
 await navigator.locks.request('trie-trunk-files-v1',async()=>{
  const root=await navigator.storage.getDirectory();
  try{await root.removeEntry('trie-trunk-v2',{recursive:true});}
  catch(error){if(!(error instanceof DOMException&&error.name==='NotFoundError'))throw error;}
 });
}
