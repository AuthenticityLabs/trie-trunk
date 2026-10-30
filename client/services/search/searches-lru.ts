import type {Predicate,QueryPlan} from '../query.ts';
import {formatQuery} from '../query.ts';

export const SEARCH_TTL_MS=2*60*60*1000;
export const SEARCH_LIMIT=20;
export const SEARCH_DATABASE='snoot-searches';
export const SEARCH_STORE='SearchesLRU';

export type SearchContext={source:string;scope:string;definitions:string[]};
export type SearchWindow={from:number;to:number};
export type SearchMeasurements={elapsedMs:number;matched?:number;loaded:number};
export type SearchEntry={
 signature:string;
 predicates:string;
 label:string;
 createdAt:number;
 updatedAt:number;
 lastUsedAt:number;
 recency:number;
 expiresAt:number;
 uses:number;
 activeRun?:string;
 status:'running'|'complete'|'failed'|'cancelled';
 // Context is a digest, never credentials. It qualifies the single checkpoint;
 // taxonomy, identity and window are deliberately absent from the signature.
 context:string;
 window:SearchWindow;
 checkpointEpochMs?:number;
 measurements?:SearchMeasurements;
};
export type SearchRun={signature:string;id:string;previous?:SearchEntry};
const compare=(a:unknown,b:unknown)=>{const left=JSON.stringify(a),right=JSON.stringify(b);return left<right?-1:left>right?1:0};

function predicate(value:Predicate):unknown{
 if(value.kind==='filter')return ['filter',value.filter.field,value.filter.op,value.filter.value.kind,value.filter.value.value];
 if(value.kind==='not')return ['not',predicate(value.value)];
 // These operators are commutative. Sorting does not change grouping or the
 // semantics of NOT/XOR, unlike flattening arbitrary Boolean expressions.
 return [value.kind,...[predicate(value.left),predicate(value.right)].sort(compare)];
}
export function searchPredicates(query?:QueryPlan):string{
 const filters=(query?.filters??[]).map(filter=>predicate({kind:'filter',filter}));
 return JSON.stringify({filters:filters.sort(compare),predicate:query?.predicate?predicate(query.predicate):null,search:query?.search?[query.search.mode,query.search.text]:null});
}
async function digest(value:string):Promise<string>{
 const bytes=new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(value)));
 return Array.from(bytes,n=>n.toString(16).padStart(2,'0')).join('');
}
export async function searchSignature(query?:QueryPlan):Promise<string>{return digest(searchPredicates(query))}

// A query has one common checkpoint. Inputs exist only during execution, before
// predicate filtering. A missing branch frontier cannot be guessed from matches.
export function newestCommonTimestamp(newest:readonly (number|undefined)[]):number|undefined{
 let common=Infinity;
 for(const value of newest){
  if(value===undefined||!Number.isSafeInteger(value)||value<0)return;
  if(value<common)common=value;
 }
 return Number.isFinite(common)?common:undefined;
}
export function resumeFromCheckpoint(entry:SearchEntry):number|undefined{
 return entry.checkpointEpochMs===undefined?undefined:Math.max(0,entry.checkpointEpochMs-1);
}

/** Bounded, advisory query metadata. Event bytes and results remain in the
 * native store. Callers must validate context/coverage before using a checkpoint
 * to narrow work; expiration is not a seal or late-arrival guarantee. */
export class SearchesLRU{
 private factory:IDBFactory|undefined;
 private now:()=>number;
 constructor(options:{indexedDB?:IDBFactory;now?:()=>number}={}){
  this.factory=options.indexedDB??globalThis.indexedDB;this.now=options.now??Date.now;
 }
 get available(){return !!this.factory}
 private async change<T>(action:(rows:SearchEntry[],store:IDBObjectStore,now:number)=>T):Promise<T>{
  if(!this.factory)throw Error('Search history storage is unavailable');
  const factory=this.factory;
  const db=await new Promise<IDBDatabase>((resolve,reject)=>{
   const request=factory.open(SEARCH_DATABASE,1);
   request.onupgradeneeded=()=>request.result.createObjectStore(SEARCH_STORE,{keyPath:'signature'});
   request.onerror=()=>reject(request.error);
   request.onsuccess=()=>resolve(request.result);
  });
  db.onversionchange=()=>db.close();
  try{return await new Promise<T>((resolve,reject)=>{
   const tx=db.transaction(SEARCH_STORE,'readwrite'),store=tx.objectStore(SEARCH_STORE),request=store.getAll();
   let result:T;
   tx.oncomplete=()=>resolve(result);tx.onabort=()=>reject(tx.error??Error('Search history transaction aborted'));
   tx.onerror=()=>{}; // onabort reports the failed transaction once.
   request.onsuccess=()=>{
    try{
     const now=this.now(),rows=(request.result as SearchEntry[]).filter(row=>{
      if(row.expiresAt>now)return true;store.delete(row.signature);return false;
     });
     result=action(rows,store,now);
     // getAll, touch/update and eviction share one transaction across tabs.
     rows.sort((a,b)=>b.recency-a.recency);
     for(const row of rows.slice(SEARCH_LIMIT))store.delete(row.signature);
    }catch(error){reject(error);tx.abort()}
   };
  })}finally{db.close()}
 }
 async start(query:QueryPlan|undefined,context:SearchContext,window:SearchWindow):Promise<SearchRun>{
  if(!Number.isSafeInteger(window.from)||!Number.isSafeInteger(window.to)||window.from>=window.to)throw Error('Invalid search metadata window');
  const predicates=searchPredicates(query),signature=await digest(predicates);
  const contextKey=await digest(JSON.stringify([context.source,context.scope,[...context.definitions].sort()]));
  const id=crypto.randomUUID();
  return this.change((rows,store,now)=>{
   const old=rows.find(row=>row.signature===signature);
   const previous=old?.context===contextKey&&old.window.from<=window.from&&old.window.to<=window.to?structuredClone(old):undefined;
   // A new scope/window cannot inherit progress merely because predicates match.
   // Changing the end also needs a fresh completeness check before advancement.
   const label=formatQuery({collection:'events',plan:{filters:[],...query,order:'asc'}},'pipeline').replace(/\s*\n\s*/g,' ');
   const row:SearchEntry={signature,predicates,label,createdAt:old?.createdAt??now,updatedAt:old?.updatedAt??now,lastUsedAt:now,recency:Math.max(0,...rows.map(row=>row.recency))+1,expiresAt:old?.expiresAt??now+SEARCH_TTL_MS,uses:(old?.uses??0)+1,activeRun:id,status:'running',context:contextKey,window:{...window},checkpointEpochMs:previous?.checkpointEpochMs,measurements:previous?.measurements};
   if(old)rows.splice(rows.indexOf(old),1,row);else rows.push(row);
   store.put(row);return {signature,id,previous};
  });
 }
 async finish(run:SearchRun,result:{status:'complete';progress?:{checkpointEpochMs?:number};window?:SearchWindow;measurements:SearchMeasurements}|{status:'failed'|'cancelled'}):Promise<void>{
  return this.change((rows,store,now)=>{
   const row=rows.find(row=>row.signature===run.signature);
   // A completed old request must not overwrite a newer query/scope or resurrect
   // an entry removed by expiration, eviction or Clear Local Copy.
   if(!row||row.activeRun!==run.id)return;
   delete row.activeRun;row.status=result.status;
   if(result.status==='complete'){
    if(result.window)row.window=result.window;
    const checkpoint=result.progress?.checkpointEpochMs;
    if(checkpoint!==undefined&&(!Number.isSafeInteger(checkpoint)||checkpoint<row.window.from||checkpoint>=row.window.to))throw Error('Invalid query checkpoint');
    row.updatedAt=now;row.expiresAt=now+SEARCH_TTL_MS;
    if(result.progress)row.checkpointEpochMs=result.progress.checkpointEpochMs;
    row.measurements=result.measurements;
   }
   store.put(row);
  });
 }
 async get(signature:string):Promise<SearchEntry|undefined>{
  return this.change((rows,store,now)=>{
   const row=rows.find(row=>row.signature===signature);
   if(row){row.lastUsedAt=now;row.recency=Math.max(0,...rows.map(row=>row.recency))+1;store.put(row)}return row;
  });
 }
 async list():Promise<SearchEntry[]>{return this.change(rows=>rows)}
 async clear(signature?:string):Promise<void>{
  return this.change((rows,store)=>{
   if(signature)store.delete(signature);else store.clear();
   rows.splice(0,rows.length,...(signature?rows.filter(row=>row.signature!==signature):[]));
  });
 }
}
