import {encodeObject,decodeObject} from '../pil/control.ts';
import {read} from '../pil/layout.ts';
import {compile} from '../pil/metadata.ts';
import {branchTargets} from '../pil/time-window.ts';
import type {TrunkBootstrap} from '../pil/trunk.ts';
import type {Archive} from '../store/protocol.ts';
import type {SearchRun} from './searches-lru.ts';
import {resumeFromCheckpoint,searchPredicates,SEARCH_TTL_MS,SEARCH_LIMIT} from './searches-lru.ts';
import type {ViewQuery} from '../pil/view.ts';
import {HOUR} from '../store/time-trie.ts';

export type LiveFrame={bytes:Uint8Array;time:number;ordinal:number};
type Receipt={Tap:string;Definition:string;Generation:string;Sequence:number;Revision:number;Sealed:number;Retained:number};
export type DeltaBranch={receipt:Receipt;frames:LiveFrame[]};
export type QueryDelta={epoch:string;from:number;to:number;requestTo:string;branches:Map<string,DeltaBranch>;expires:number;serial:number;bytes:number};
const branchKey=(tap:string,digest:string)=>tap+':'+digest;
const integer=(n:unknown):n is number=>typeof n==='number'&&Number.isSafeInteger(n)&&n>=0;

/** Only live frames are retained here. Durable hours stay in SODS; IDB holds
 * query metadata only. Losing this bounded cache always forces a fresh live
 * snapshot, even if the persisted query checkpoint survived a reload. */
export class QueryDeltas {
 private values=new Map<string,QueryDelta>();
 private key(trunk:TrunkBootstrap,q:ViewQuery,token:string){return JSON.stringify([trunk.version,token,q.tap,searchPredicates(q.query)])}
 clear(){this.values.clear()}
 commit(trunk:TrunkBootstrap,q:ViewQuery,token:string,value:QueryDelta){
  const key=this.key(trunk,q,token);this.values.delete(key);this.values.set(key,value);
  let bytes=[...this.values.values()].reduce((n,v)=>n+v.bytes,0);
  for(const [k,v] of this.values){if(v.expires<=Date.now()||this.values.size>SEARCH_LIMIT||bytes>64*1024*1024){this.values.delete(k);bytes-=v.bytes}}
 }
 async prepare(trunk:TrunkBootstrap,q:ViewQuery,token:string,signal:AbortSignal,archive:Archive,fetcher:typeof fetch,run?:SearchRun):Promise<QueryDelta>{
  const targets=branchTargets(trunk,q.tap,true),from=Date.parse(q.from),key=this.key(trunk,q,token);
  const reads=targets.map(t=>({tap:t.tap,digest:t.definition.digest,from:Math.floor(from/HOUR)*HOUR,to:Math.ceil(Date.parse(q.to)/HOUR)*HOUR}));
  if(!targets.length)return {epoch:'empty',from,to:Date.parse(q.to),requestTo:q.to,branches:new Map(),expires:Date.now()+SEARCH_TTL_MS,serial:0,bytes:0};
  const times=(await archive(token,{kind:'time-read-many',queries:reads})).times;
  if(times?.length!==targets.length)throw Error('Missing delta coverage');
  const serial=times[0]?.serial??0;
  const targetByKey=new Map(targets.map((target,i)=>[branchKey(target.tap,target.definition.digest),{target,time:times[i]}]));
  let prior=this.values.get(key);
  if(prior&&(prior.expires<=Date.now()||prior.from>from||prior.serial!==serial||targets.length!==prior.branches.size))prior=undefined;
  if(q.after){
   if(!prior||prior.from!==from||prior.requestTo!==q.to)throw Error('Live search snapshot expired; restart search');
   return prior;
  }
  // A checkpoint alone proves neither local bytes nor live snapshot coverage.
  const checkpoint=prior&&run?.previous?.status==='complete'?resumeFromCheckpoint(run.previous):undefined;
  const known=prior?[...prior.branches.values()].filter(b=>{
   const entry=targetByKey.get(branchKey(b.receipt.Tap,b.receipt.Definition));
   return entry&&(!entry.time.generation||entry.time.generation===b.receipt.Generation);
  }).map(b=>b.receipt):[];
  const response=await fetcher('/query-delta?'+new URLSearchParams({tap:q.tap}),{method:'POST',signal,cache:'no-store',headers:{'Content-Type':'application/pil',Accept:'application/pil',...(token?{Authorization:`Bearer ${token}`}:{})},body:encodeObject({From:from,To:Date.parse(q.to),Now:true,...(checkpoint===undefined?{}:{Checkpoint:checkpoint+1}),Epoch:prior?.epoch??'',PreviousTo:prior?.to??0,Known:known,Targets:targets.map(t=>({Tap:t.tap,Definition:t.definition.digest}))}) as BodyInit});
  if(!response.ok)throw Error(`Query refresh failed (${response.status}): ${await response.text()}`);
  if(response.headers.get('Trunk-Format')!=='query-delta/0')throw Error('Invalid query delta format');
  const wire=decodeObject(new Uint8Array(await response.arrayBuffer())) as any;
  signal.throwIfAborted();
  if(typeof wire.Epoch!=='string'||!integer(wire.To)||wire.To<=from||!Array.isArray(wire.Branches)||wire.Branches.length!==targets.length)throw Error('Invalid query delta');
  const result:QueryDelta={epoch:wire.Epoch,from,to:wire.To,requestTo:q.to,branches:new Map(),expires:Date.now()+SEARCH_TTL_MS,serial,bytes:0};
  const buffers=new Set<ArrayBufferLike>();
  for(const rawPart of wire.Branches){
   const part={...rawPart.Receipt,ReplaceFrom:rawPart.ReplaceFrom,TimeTrie:rawPart.TimeTrie,Frames:rawPart.Frames};
   const key=branchKey(part.Tap,part.Definition),target=targetByKey.get(key)?.target;
   if(!target||result.branches.has(key)||!/^[a-f0-9]{32}$/.test(part.Generation)||![part.Sequence,part.Revision,part.Sealed,part.Retained,part.ReplaceFrom].every(integer)||part.ReplaceFrom<from||part.ReplaceFrom>Math.max(wire.To,part.Sealed))throw Error('Invalid query delta binding');
   const previous=prior&&prior.epoch===wire.Epoch?prior.branches.get(key):undefined;
   const frames=previous&&previous.receipt.Generation===part.Generation?previous.frames.filter(f=>f.time>=Math.max(from,part.Sealed)&&f.time<part.ReplaceFrom&&f.time<wire.To):[];
   const layout=compile(target.definition,target.tap).definition;
   const timeIndex=layout.fields.findIndex(f=>f.name==='@time'),recordIndex=layout.fields.findIndex(f=>f.name==='@event_id');
   let last=-1,lastOrdinal=-1;
   for(const raw of part.Frames??[]){
    if(!(raw instanceof Uint8Array))throw Error('Invalid live PIL record');
    const time=read(raw,layout,timeIndex),ordinal=read(raw,layout,recordIndex);
    if(!integer(time)||!integer(ordinal)||ordinal>=2**24||time<Math.max(part.ReplaceFrom,from,part.Sealed)||time>=wire.To||time<last||time===last&&ordinal<=lastOrdinal)throw Error('Invalid live event coordinates');
    last=time;lastOrdinal=ordinal;frames.push({bytes:raw,time,ordinal});
   }
   // Kept frames precede ReplaceFrom; the native delta is already time/row ordered.
   for(let i=1;i<frames.length;i++)if(frames[i].time===frames[i-1].time&&frames[i].ordinal===frames[i-1].ordinal)throw Error('Duplicate live event');
   if(part.TimeTrie?.length){
    if(!(part.TimeTrie instanceof Uint8Array))throw Error('Invalid delta time trie');
    await archive(token,{kind:'time-save',input:{tap:target.tap,definition:target.definition,generation:part.Generation,sequence:part.Sequence,serial,bytes:part.TimeTrie}});
   }
   for(const frame of frames)if(!buffers.has(frame.bytes.buffer)){buffers.add(frame.bytes.buffer);result.bytes+=frame.bytes.buffer.byteLength}
   result.branches.set(key,{receipt:{Tap:part.Tap,Definition:part.Definition,Generation:part.Generation,Sequence:part.Sequence,Revision:part.Revision,Sealed:part.Sealed,Retained:part.Retained},frames});
  }
  signal.throwIfAborted();return result;
 }
}
export const deltaBranch=(delta:QueryDelta,tap:string,digest:string)=>delta.branches.get(branchKey(tap,digest));
