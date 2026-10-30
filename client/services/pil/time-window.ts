import { createAbortScope } from '../abort.ts';
import type {TimeReceipt} from '../store/native.ts';
import type {Archive} from '../store/protocol.ts';
import type {TrunkBootstrap} from './trunk.ts';
import {registeredPath} from './trunk.ts';
import {matchesSource} from '../source.ts';
import {HOUR,mergeTimeWindow,timeLeaf} from '../store/time-trie.ts';
export type TimeWindowRequest={trunk:TrunkBootstrap;scope:string;from:number;to:number;diagnostics?:boolean};
export type TimeWindowResult={from:number;to:number;counts:number[];errors:number[];count:number;receivedHours:number;receipts?:TimeReceipt[]};
export function compactHour(ms:number){
 const d=new Date(ms),year=d.getUTCFullYear();
 if(!Number.isSafeInteger(ms)||ms%HOUR||year<2000||year>2099)throw Error('Time range must use UTC hours in 2000–2099');
 const day=Math.floor((ms-Date.UTC(year,0,1))/86400000)+1;
 return `${String(year-2000).padStart(2,'0')}${String(day).padStart(3,'0')}${String(d.getUTCHours()).padStart(2,'0')}`;
}
export function branchTargets(trunk:TrunkBootstrap,scope:string,diagnostics=false){
 return trunk.catalog.files.filter(f=>f.kind==='tri'&&!f.bootstrap).flatMap(file=>{
  if(!registeredPath(file.path))throw Error('Invalid branch path');
  const branch=file.path.split('/').at(-1)!.slice(0,-4);
  // These are catalog bindings, not constructed download paths. Definitions in
  // multiple minor folders remain separate targets for the same logical source.
  const bindings=trunk.bindings.filter(b=>b.digest===file.definition&&b.tap.endsWith('/'+branch)&&file.path.startsWith(b.tap.slice(0,b.tap.lastIndexOf('/'))+'/'));
  const taps=[...new Set(bindings.map(b=>b.tap))];
  if(taps.length!==1)throw Error('Ambiguous time trie catalog binding');
  if(!matchesSource(scope,taps[0])||!diagnostics&&(branch==='debug'||branch==='trace'))return [];
  const definition=trunk.definitions.find(d=>d.digest===file.definition);if(!definition)throw Error('Missing time trie definition');
  return [{file,tap:taps[0],definition}];
 });
}
export async function loadTimeWindow(q:TimeWindowRequest,token:string,signal:AbortSignal,archive:Archive|undefined,fetcher:typeof fetch=fetch):Promise<TimeWindowResult>{
 const from=Math.floor(q.from/HOUR)*HOUR,to=Math.ceil(q.to/HOUR)*HOUR;
 compactHour(from);compactHour(to);
 if(from>=to||to-from>8*366*24*HOUR)throw Error('Invalid time range');
 const canceled=createAbortScope([signal]);signal=canceled.signal;
 try {
 const result:TimeWindowResult={from,to,counts:Array((to-from)/HOUR).fill(0),errors:Array((to-from)/HOUR).fill(0),count:0,receivedHours:0};
 const targets=branchTargets(q.trunk,q.scope,q.diagnostics);
 signal.throwIfAborted();
 const receipts=archive&&targets.length?(await archive(token,{kind:'time-read-many',queries:targets.map(t=>({tap:t.tap,digest:t.definition.digest,from,to}))})).times:undefined;
 if(archive&&targets.length&&receipts?.length!==targets.length)throw Error('Missing time trie receipts');
 if(receipts?.every(r=>r.missing.length===0))result.receipts=receipts;
 let cursor=0;
 const consume=async()=>{while(cursor<targets.length){
  const i=cursor++,{file,tap,definition}=targets[i];signal.throwIfAborted();
  const saved=receipts?.[i];
  const rows=new Map((saved?.rows??[]).map(r=>[r.time,r]));
  for(const gap of saved?.missing??[{from,to}]){
   signal.throwIfAborted();
   const response=await fetcher(`/${file.path}?begin=${compactHour(gap.from)}&end=${compactHour(gap.to)}`,{signal,cache:'no-store',headers:{Accept:'application/octet-stream',...(token?{Authorization:`Bearer ${token}`}:{})}});
   if(!response.ok)throw Error(`Time trie request failed (${response.status})`);
   const h=response.headers,generation=h.get('Trunk-Generation')??'',sequence=Number(h.get('Trunk-Generation-Sequence'));
   if(h.get('Trunk-Format')!=='time-trie/0'||h.get('Trunk-Path')!==file.path||h.get('Trunk-Source')!==tap||h.get('Definition-Version')!==definition.digest||!/^[a-f0-9]{32}$/.test(generation)||!Number.isSafeInteger(sequence)||sequence<1)throw Error('Invalid time trie response binding');
   if(response.status===204)continue;
   const bytes=new Uint8Array(await response.arrayBuffer());signal.throwIfAborted();
   const identity=Uint8Array.from(generation.match(/../g)!,v=>parseInt(v,16)),tree=mergeTimeWindow(undefined,bytes,identity);
   const bounds=new DataView(bytes.buffer,bytes.byteOffset),a=Number(bounds.getBigUint64(64,true)),z=Number(bounds.getBigUint64(72,true));
   if(a<gap.from||z>gap.to)throw Error('Time trie response outside requested window');
   if(saved?.generation&&saved.generation!==generation)throw Error('Time trie generation changed; refresh the local cache');
   if(archive){await archive(token,{kind:'time-save',input:{tap,definition,generation,sequence,serial:saved?.serial??0,bytes}});}
   for(let at=a;at<z;at+=HOUR){const leaf=timeLeaf(tree,at);if(leaf)rows.set(at,{time:at,count:leaf.count,flags:leaf.flags});}
  }
  signal.throwIfAborted();
  for(const row of rows.values()){
   const i=(row.time-from)/HOUR;if(i<0||i>=result.counts.length)continue;
   result.counts[i]+=row.count;result.count+=row.count;result.receivedHours++;
   if(tap.endsWith('/error'))result.errors[i]+=row.count;
  }
 }};
 const tasks=Array.from({length:Math.min(4,targets.length)},consume);
 try{await Promise.all(tasks)}catch(error){canceled.abort(error);await Promise.allSettled(tasks);throw error;}
 return result;
 } finally { canceled.dispose(); }
}
