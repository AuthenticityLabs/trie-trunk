import {Seed,bindSeed} from '../seeds/index.ts';
import {compile} from '../pil/metadata.ts';
import {branchTargets} from '../pil/time-window.ts';
import {collectNative} from '../pil/native-collect.ts';
import {timeFromLowWord} from '../seed-identity.ts';
import type {TrunkBootstrap} from '../pil/trunk.ts';
import type {Archive} from '../store/protocol.ts';
import type {Codec} from '../pil/wasm.ts';
import type {StoredDefinition} from '../pil/stream.ts';
import type {SearchRun} from './searches-lru.ts';
import {resumeFromCheckpoint,searchPredicates,SEARCH_TTL_MS,SEARCH_LIMIT} from './searches-lru.ts';
import type {ViewQuery} from '../pil/view.ts';
import {HOUR,TIME_SODS} from '../store/time-trie.ts';

export type LiveFrame={bytes:Uint8Array;common:Uint8Array;row:Uint8Array;base:number;time:number;ordinal:number};
type Receipt={Tap:string;Definition:string;Generation:string;Sequence:number;Sealed:number;Retained:number};
export type WindowBranch={receipt:Receipt;frames:LiveFrame[]};
export type QueryWindow={from:number;to:number;requestTo:string;branches:Map<string,WindowBranch>;expires:number;serial:number;bytes:number;collectionMs:number};
const branchKey=(tap:string,digest:string)=>tap+':'+digest;
const integer=(n:unknown):n is number=>typeof n==='number'&&Number.isSafeInteger(n)&&n>=0;
const compare=(a:LiveFrame,b:LiveFrame)=>a.time-b.time||a.ordinal-b.ordinal;
function merge(a:LiveFrame[],b:LiveFrame[]):LiveFrame[]{
 const out:LiveFrame[]=[];let i=0,j=0;
 while(i<a.length||j<b.length){
  if(i===a.length){out.push(b[j++]);continue}if(j===b.length){out.push(a[i++]);continue}
  const order=compare(a[i],b[j]);if(order<0)out.push(a[i++]);else{if(order===0)i++;out.push(b[j++])}
 }
 return out;
}
/** The query window is unchanged when its retrieval start advances. Only live
 * rows live here; sealed hours are committed to SODS before replacing them. */
export class WindowCache {
 private values=new Map<string,QueryWindow>();
 private key(trunk:TrunkBootstrap,q:ViewQuery,token:string){return JSON.stringify([trunk.version,token,q.tap,searchPredicates(q.query)])}
 clear(){this.values.clear()}
 commit(trunk:TrunkBootstrap,q:ViewQuery,token:string,value:QueryWindow){
  const key=this.key(trunk,q,token);this.values.delete(key);this.values.set(key,value);
  let bytes=[...this.values.values()].reduce((n,v)=>n+v.bytes,0);
  for(const [k,v] of this.values)if(v.expires<=Date.now()||this.values.size>SEARCH_LIMIT||bytes>64*1024*1024){this.values.delete(k);bytes-=v.bytes}
 }
 async prepare(trunk:TrunkBootstrap,q:ViewQuery,token:string,signal:AbortSignal,archive:Archive,fetcher:typeof fetch,codec:Codec,definitions:Map<string,StoredDefinition>,run?:SearchRun):Promise<QueryWindow>{
  const targets=branchTargets(trunk,q.tap,true),from=Date.parse(q.from),key=this.key(trunk,q,token);
  if(!targets.length)return {from,to:Date.parse(q.to),requestTo:q.to,branches:new Map(),expires:Date.now()+SEARCH_TTL_MS,serial:0,bytes:0,collectionMs:0};
  const times=(await archive(token,{kind:'time-read-many',queries:targets.map(t=>({tap:t.tap,digest:t.definition.digest,from:Math.floor(from/HOUR)*HOUR,to:Math.ceil(Date.parse(q.to)/HOUR)*HOUR}))})).times;
  if(times?.length!==targets.length)throw Error('Missing local window coverage');
  const serial=times[0]?.serial??0;
  const selected=new Map(targets.map((target,i)=>[branchKey(target.tap,target.definition.digest),{target,time:times[i]}]));
  let prior=this.values.get(key);
  if(prior&&(prior.expires<=Date.now()||prior.from>from||prior.serial!==serial||targets.length!==prior.branches.size||[...prior.branches].some(([key,b])=>{const t=selected.get(key);return !t||t.time.generation&&t.time.generation!==b.receipt.Generation})))prior=undefined;
  if(q.after){if(!prior||prior.from!==from||prior.requestTo!==q.to)throw Error('Live search snapshot expired; restart search');return {...prior,collectionMs:0};}
  const checkpoint=prior&&run?.previous?.status==='complete'?resumeFromCheckpoint(run.previous):undefined;
  const collectFrom=checkpoint===undefined?from:Math.max(from,Math.min(checkpoint,prior!.to));
  const result:QueryWindow={from,to:Date.parse(q.to),requestTo:q.to,branches:new Map(),expires:Date.now()+SEARCH_TTL_MS,serial,bytes:0,collectionMs:0};
  // This is an ordinary collection range. No query state is sent to the server.
  const collectionStarted=performance.now();
  await collectNative({tap:q.tap,from:new Date(collectFrom).toISOString(),to:'now',token,signal,codec,definitions,archive,fetcher,
   onAttempt:()=>result.branches.clear(),onWindow:to=>{result.to=to},live:{
    window:part=>{
     const key=branchKey(part.binding.tap,part.binding.digest);
     if(!selected.has(key)||result.branches.has(key))throw Error('Unexpected collection branch');
     result.branches.set(key,{receipt:{Tap:part.binding.tap,Definition:part.binding.digest,Generation:part.generation,Sequence:part.sequence,Sealed:part.sealed,Retained:part.retained},frames:[]});
    },
    pack:async pack=>{
     const key=branchKey(pack.binding.tap,pack.binding.digest),branch=result.branches.get(key),target=selected.get(key)?.target;
     if(!branch||!target)throw Error('Missing live window binding');
     const binding=bindSeed(compile(target.definition,target.tap).definition),index=new DataView(pack.rows.buffer,pack.rows.byteOffset,pack.rows.byteLength);
     for(let i=0;i<pack.members.length;i++){
      const bytes=pack.members[i],row=pack.rows.subarray(i*6,i*6+6),time=timeFromLowWord(index.getUint32(i*6,true),pack.hour);
      const ordinal=new Seed(binding,bytes,{time,stateLeaf:0,common:pack.common}).eventId;
      if(!integer(ordinal)||ordinal>=2**24||time<Math.max(collectFrom,branch.receipt.Sealed))throw Error('Invalid live event coordinates');
      const frame={bytes,common:pack.common,row,base:pack.hour,time,ordinal},last=branch.frames.at(-1);
      if(last&&compare(last,frame)>=0)throw Error('Unordered live records');branch.frames.push(frame);
     }
    }
   }});
  result.collectionMs=performance.now()-collectionStarted;
  signal.throwIfAborted();
  if(result.branches.size!==selected.size)throw Error('Incomplete collection window');
  // Sealing can advance behind the incremental start. Fetch only newly sealed
  // hours that are not durably present before releasing their ephemeral copies.
  const promotions=[...result.branches].flatMap(([key,b])=>{
   const old=prior?.branches.get(key);if(!old||old.receipt.Generation!==b.receipt.Generation)return [];
   const a=Math.max(Math.floor(from/HOUR)*HOUR,old.receipt.Sealed,b.receipt.Retained),z=b.receipt.Sealed;
   return a<z?[{key,branch:b,from:a,to:z}]:[];
  });
  if(promotions.length){
   const coverage=(await archive(token,{kind:'time-read-many',queries:promotions.map(p=>({tap:p.branch.receipt.Tap,digest:p.branch.receipt.Definition,from:p.from,to:p.to}))})).times;
   if(coverage?.length!==promotions.length)throw Error('Missing sealed-hour coverage');
   for(let i=0;i<promotions.length;i++){
    const p=promotions[i],rows=new Map(coverage[i].rows.map(r=>[r.time,r]));let a:number|undefined,z=0;
    for(let hour=p.from;hour<p.to;hour+=HOUR)if(!((rows.get(hour)?.flags??0)&TIME_SODS)){a??=hour;z=hour+HOUR}
    if(a!==undefined){
     let provisional=false;const started=performance.now();
     await collectNative({tap:p.branch.receipt.Tap,from:new Date(a).toISOString(),to:new Date(z).toISOString(),token,signal,codec,definitions,archive,fetcher,onProvisional:()=>{provisional=true}});
     result.collectionMs+=performance.now()-started;
     if(provisional)throw Error('Sealed hour is not yet available; retry search');
    }
   }
  }
  const buffers=new Set<ArrayBufferLike>();
  for(const [key,branch] of result.branches){
   const previous=prior?.branches.get(key);
   if(previous&&previous.receipt.Generation!==branch.receipt.Generation){this.values.delete(this.key(trunk,q,token));throw Error('Storage generation changed; restart search')}
   if(branch.frames.some(f=>f.time>=result.to))throw Error('Event outside collection window');
   // native-finish above has committed complete hours before their live copies
   // are removed. Overlap is merged by stable provisional time/record identity.
   branch.frames=merge((previous?.frames??[]).filter(f=>f.time>=Math.max(from,branch.receipt.Sealed)&&f.time<result.to),branch.frames);
   for(const frame of branch.frames)for(const view of [frame.bytes,frame.common,frame.row])if(!buffers.has(view.buffer)){buffers.add(view.buffer);result.bytes+=view.buffer.byteLength}
  }
  return result;
 }
}
export const windowBranch=(window:QueryWindow,tap:string,digest:string)=>window.branches.get(branchKey(tap,digest));
