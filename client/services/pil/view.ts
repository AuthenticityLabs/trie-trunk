import {executeLocalSearch} from '../search/execute.ts';
import {queryLocalSummaries} from '../search/summaries.ts';
import {INDEX_ROW_BYTES} from './pac-native.ts';
import {branchTargets} from "./time-window.ts";
import type {TrunkBootstrap} from "./trunk.ts";
import { nativeCacheIssue, combineCacheErrors } from "../store/errors.ts";
import { SummaryDayCache, mergeDailyAnalytics, type DailyAnalytics } from './summaries.ts';
import type { Archive } from '../store/protocol.ts';
import { collectNative } from './native-collect.ts';
import { ViewCoverage } from './view-coverage.ts';
import { PILStream, consume, type StoredDefinition, type Batch } from './stream.ts';
import { Codec } from './wasm.ts';
import type { Metadata, Binding } from './metadata.ts';
export type ViewQuery={engine?:'local'|'tantivy';query?:import('../query').QueryPlan;tap:string;from:string;to:string;size:number;after?:string;include?:'summary'|'page'|'summary,page'|'summary,page,statistics'};
export type ViewStatistics={complete?:boolean;matched?:number;evaluated?:number;services:{name:string;count:number}[];levels:{name:string;count:number}[];duration:{count:number;average:number;min:number;max:number};operations:{errors:import('../operations').OperationRank[];latency:import('../operations').OperationRank[]}};
export type ViewSummary={from:number;to:number;step:number;counts:number[];errors:number[];count:number;status:'committed-snapshot'|'live-snapshot';statistics?:ViewStatistics};
export function validateStatistics(summary:ViewSummary):void {
 const s=summary.statistics;if(s===undefined)return;
 const count=(n:unknown):n is number=>typeof n==='number'&&Number.isSafeInteger(n)&&n>=0&&n<=summary.count;
 const scalar=(n:unknown):n is number=>typeof n==='number'&&Number.isFinite(n)&&n>=0;
 if(s.matched!==undefined||s.evaluated!==undefined||s.complete!==undefined){
  if(s.matched!==summary.count||!count(s.evaluated)||typeof s.complete!=='boolean'||s.complete!==(s.evaluated===s.matched))throw Error('Invalid statistics coverage');
 }
 const distribution=(rows:any)=>Array.isArray(rows)&&rows.length<=summary.count&&rows.every(r=>typeof r?.name==='string'&&count(r.count))&&new Set(rows.map(r=>r.name)).size===rows.length&&rows.reduce((n,r)=>n+r.count,0)===(s.evaluated??summary.count);
 const ranks=(rows:any,latency:boolean)=>Array.isArray(rows)&&rows.length<=10&&rows.every(r=>typeof r?.service==='string'&&typeof r.resource==='string'&&typeof r.operation==='string'&&scalar(r.value)&&count(r.samples)&&r.samples>0&&(latency?r.samples<=s.duration.count:r.value===r.samples));
 if(!s||!distribution(s.services)||!distribution(s.levels)||!s.duration||!count(s.duration.count)||![s.duration.average,s.duration.min,s.duration.max].every(scalar)||s.duration.min>s.duration.max||s.duration.average<s.duration.min||s.duration.average>s.duration.max||!s.operations||!ranks(s.operations.errors,false)||!ranks(s.operations.latency,true))throw Error('Invalid full-range statistics');
}
export type ViewReference={stream:number;row:number;key:string;cached:boolean;provisional?:boolean;native?:{from:number;to:number;generation:string}};
export type ViewBucket={key:string;stream:number;from:number;to:number;ranges:{Start:number;End:number}[];sealed:boolean};
export type ViewResult={notice?:string;analytics?:DailyAnalytics;buckets?:ViewBucket[];coverage?:{complete:number;partial:number};cursor:string;snapshot:string;summaryVersion:string;summary?:ViewSummary;page:ViewReference[];next?:string;committedThrough:number;provisional:boolean};
export type ViewTimings={planning?:number;request:number;response:number;summaries:number;events:number;client:number};
export type ViewResponse={timings?:ViewTimings;result:ViewResult;definitions:Metadata[];bindings:Binding[];batch:Batch;native?:(NativeViewRecord|undefined)[]};
export type NativeViewRecord={row:Uint8Array;base:number;trie:{start:number;bytes:Uint8Array}[]};
type PackedRecord={bytes:Uint8Array;common?:Uint8Array;native?:NativeViewRecord};
export class ViewCache {
 private trunk?:TrunkBootstrap;
 private localSummaries=new Map<string,ViewSummary>();
 setTrunk(trunk:TrunkBootstrap){this.trunk=trunk;}

 private summaryDays=new SummaryDayCache();
 private nativeHours=new Map<string,Promise<{source:string;trie:NativeViewRecord["trie"]}>>();
 private persisting?:Promise<void>;
 persistHours(save:(hour:{key:string;tap:string;from:number;to:number})=>Promise<void>):Promise<void>{
  // Tabs share this cache. Serialize promotions so simultaneous scroll requests
  // cannot collect the same canonical hour twice. Failed hours remain retryable.
  const previous=this.persisting??Promise.resolve();
  const next=previous.catch(()=>{}).then(async()=>{
   for(const hour of this.completeHours()){await save(hour);this.persistedHour(hour.key)}
  });
  this.persisting=next;
  void next.finally(()=>{if(this.persisting===next)this.persisting=undefined}).catch(()=>{});
  return next;
 }
 private persisted=new Set<string>();
 completeHours(){return this.coverage.complete().filter(h=>!this.persisted.has(h.key))}
 persistedHour(key:string){this.persisted.add(key);while(this.persisted.size>4096)this.persisted.delete(this.persisted.values().next().value!)}
 private events=new Map<string,PackedRecord>();private summaries=new Map<string,ViewSummary>();private bytes=0;
 private prefixes=new Map<Uint8Array,number>();
 private tries=new Map<NativeViewRecord["trie"],number>();
 private drop(key:string){
  const old=this.events.get(key);if(!old)return;
  this.events.delete(key);this.bytes-=old.bytes.length;
  if(old.native){this.bytes-=old.native.row.length;const n=this.tries.get(old.native.trie)!;if(n===1){this.tries.delete(old.native.trie);this.bytes-=old.native.trie.reduce((s,t)=>s+t.bytes.length,0)}else this.tries.set(old.native.trie,n-1)}
  if(old.common){const n=this.prefixes.get(old.common)!;if(n===1){this.prefixes.delete(old.common);this.bytes-=old.common.length}else this.prefixes.set(old.common,n-1)}
 }
 private put(key:string,record:PackedRecord){
  this.drop(key);this.events.set(key,record);this.bytes+=record.bytes.length;
  if(record.native){this.bytes+=record.native.row.length;const n=this.tries.get(record.native.trie)??0;if(!n)this.bytes+=record.native.trie.reduce((s,t)=>s+t.bytes.length,0);this.tries.set(record.native.trie,n+1)}
  if(record.common){const n=this.prefixes.get(record.common)??0;if(!n)this.bytes+=record.common.length;this.prefixes.set(record.common,n+1)}
 }
 private versions=new Map<string,string>();
 private pages=new Map<string,string[]>();
 private coverage=new ViewCoverage();
 private fetcher:typeof fetch;
 constructor(fetcher:typeof fetch=fetch){this.fetcher=fetcher}
 resetEvents(){
  this.localSummaries.clear();this.nativeHours.clear();this.events.clear();this.prefixes.clear();this.tries.clear();this.bytes=0;
  this.pages.clear();this.versions.clear();this.summaries.clear();this.persisted.clear();this.coverage=new ViewCoverage();
 }

 remember(view:ViewResponse) {
  this.coverage.validate(view.result.buckets??[],view.bindings,view.result.page);
  if(view.batch.rows.length!==view.result.page.length*3)throw Error('Invalid restored page');
  const frames:PackedRecord[]=[];let end=0;
  for(let i=0;i<view.batch.rows.length;i+=3){
   const [stream,start,to]=view.batch.rows.subarray(i,i+3);
   if(stream!==view.result.page[i/3].stream||start!==end||to<=start||to>view.batch.bytes.byteLength)throw Error('Invalid restored frame');
   frames.push({bytes:new Uint8Array(view.batch.bytes,start,to-start).slice(),common:view.batch.common?.[i/3],native:view.native?.[i/3]});end=to;
  }
  if(end!==view.batch.bytes.byteLength)throw Error('Invalid restored page length');
  frames.forEach((bytes,i)=>this.put(view.result.page[i].key,bytes));
  while(this.bytes>32*1024*1024){const key=this.events.keys().next().value!;this.drop(key);this.coverage.evict(key)}
  this.coverage.add(view.result.buckets??[],view.bindings,view.result.page,key=>this.events.has(key));
 }
 async query(query:ViewQuery,token:string,signal:AbortSignal,codec:Codec,definitions:Map<string,StoredDefinition>,archive?:Archive,background=false,projection?:(manifest:DailyAnalytics,days:import('./summaries.ts').DecodedDay[])=>void,onPage?:(view:ViewResponse)=>void):Promise<ViewResponse>{
  if(query.engine!=='tantivy'){
   if(!this.trunk||!archive)throw Error('Local search requires the trunk catalog and local storage. Reload the connection.');
   if(query.include==='summary'&&!query.query)return queryLocalSummaries(this.trunk,query,token,signal,archive,this.fetcher,projection);
   return executeLocalSearch(this.trunk,query,token,signal,codec,definitions,archive,this.fetcher,onPage,this.localSummaries);
  }
  if(query.query?.predicate)throw Error('Logical expressions require Local Search; turn off the Tantivy comparison.');
  const started=performance.now();
  const planningEnded=performance.now();
  const scope=JSON.stringify([query.tap,query.from,query.to,query.query]);
  const params=new URLSearchParams({tap:query.tap,from:query.from,to:query.to,page:String(query.size),include:query.include??'summary,page,statistics'});if(query.after)params.set('after',query.after);if(query.query)params.set('query',JSON.stringify(query.query));
  // Pin advertised bytes for this request, so concurrent eviction cannot make
  // an acknowledged cached event disappear while its response is in flight.
  const pageKey=JSON.stringify([scope,query.size,query.after??"",query.include??"summary,page,statistics"]);
  const held=new Map<string,PackedRecord>();
  for(const key of (this.pages.get(pageKey)??[]).slice(0,240)){const value=this.events.get(key);if(value)held.set(key,value)}
  const buckets=query.query&&(query.query.filters.length||query.query.search)?{keys:[],events:[]}:this.coverage.known(query.tap,Date.parse(query.from),Date.parse(query.to));
  const knownPage=[...held.keys()];
  for(const key of buckets.events){const value=this.events.get(key);if(value)held.set(key,value)}
  const known=this.versions.get(scope),heldSummary=known?this.summaries.get(known):undefined;
  const fetcher=this.fetcher;
  const requestStarted=performance.now();
  const response=await fetcher(`/events?${params}`,{signal,cache:'no-store',headers:{Accept:'application/pil','PIL-Version':'2','PIL-Features':'pac-arrays,adaptive-integers,view-buckets,daily-summaries','Definition-Known':[...definitions.keys()].slice(-128).join(','),'Event-Known':knownPage.join(','),...(buckets.keys.length?{'Bucket-Known':buckets.keys.join(',')}:{} ),...(!background&&!projection&&known&&heldSummary?{'Summary-Known':known}:{}),...(token?{Authorization:`Bearer ${token}`}:{})}});
  const requestEnded=performance.now();
  if(response.ok&&response.headers.get('Trunk-Format')!=='pac-view/0'){await response.body?.cancel();throw Error('Server does not support paged views; update the server to search')}
  let result:ViewResult|undefined;const bindings:Binding[]=[],fresh:({stream:number}&PackedRecord)[]=[];
  await consume(response,new PILStream(codec,definitions,()=>{},b=>bindings.push(b),async batch=>{
   for(let i=0;i<batch.rows.length;i+=3)fresh.push({stream:batch.rows[i],bytes:new Uint8Array(batch.bytes,batch.rows[i+1],batch.rows[i+2]-batch.rows[i+1]).slice(),common:batch.common?.[i/3]});
  },undefined,value=>{
   if(result)throw Error('Duplicate view metadata');result=value as ViewResult;
   if(!result||typeof result.cursor!=='string'||result.cursor.length>90000||!Array.isArray(result.page)||result.page.length>query.size||!/^[a-f0-9]{64}$/.test(result.snapshot)||!/^[a-f0-9]{64}$/.test(result.summaryVersion)||(result.next!==undefined&&(typeof result.next!=='string'||result.next.length>90000))||typeof result.provisional!=='boolean'||!Number.isSafeInteger(result.committedThrough))throw Error('Invalid view metadata');
  }),signal);
  const responseEnded=performance.now();
  if(!result)throw Error('Missing view metadata');const meta:ViewResult=result;
  if((background||projection) && (meta.page.length || fresh.length))throw Error('Summary collection returned events');
  let summary=meta.summary??(meta.summaryVersion===known?heldSummary:undefined);
  const collected=this.nativeHours;
  const nativeRecords=async(refs:ViewReference[],retry=true):Promise<PackedRecord[]>=>{
   const ref=refs[0];
   if(!archive||!ref.native)throw Error('Native archive required for sealed search');
   const binding=bindings.find(b=>b.stream===ref.stream)!;
   const n=ref.native;
   if(!Number.isSafeInteger(n.from)||!Number.isSafeInteger(n.to)||n.from>=n.to||!/^[a-f0-9]{32}$/.test(n.generation))throw Error('Invalid segment reference');
   const key=JSON.stringify([binding.tap,binding.digest,n.generation,n.from,n.to]);
   let pending=collected.get(key);
   if(!pending){pending=(async()=>{
    const local=(await archive(token,{kind:'native-resolve',tap:binding.tap,digest:binding.digest,generation:n.generation,from:n.from,to:n.to})).nativeDependencies;
    if(local)return {source:local.source.key,trie:local.trie};
    const target=this.trunk&&branchTargets(this.trunk,binding.tap).find(t=>t.definition.digest===binding.digest);
    const artifact=target?{path:target.file.path.slice(0,-3)+'pods',kind:'pods' as const}:undefined;
    const keys=await collectNative({artifact,tap:binding.tap,from:new Date(n.from).toISOString(),to:new Date(n.to).toISOString(),token,signal,codec,definitions,archive,fetcher,definitionVersion:binding.digest});
    for(const source of keys){const deps=(await archive(token,{kind:'native-dependencies',source})).nativeDependencies!;
     if(deps.source.digest===binding.digest&&deps.source.generation===n.generation)return {source,trie:deps.trie};}
    throw Error('Search source changed; restart search');
   })();collected.set(key,pending);void pending.catch(()=>collected.delete(key));while(collected.size>32)collected.delete(collected.keys().next().value!)}
   try {
    const {source,trie}=await pending;
    const page=(await archive(token,{kind:'native-rows',source,from:n.from,to:n.to,rows:refs.map(r=>r.row)})).nativePage!;
    if(page.after!==refs.at(-1)!.row||page.rows.length!==refs.length*INDEX_ROW_BYTES||page.bases.length!==refs.length||page.offsets.length!==refs.length*2)throw Error('Missing canonical search record');
    return refs.map((_,i)=>({bytes:page.payload.subarray(page.offsets[i*2],page.offsets[i*2+1]),common:page.common?.[i],native:{row:page.rows.subarray(i*INDEX_ROW_BYTES,(i+1)*INDEX_ROW_BYTES),base:page.bases[i],trie}}));
   } catch(error) {
    // Local retention can evict an hour after its promise was cached. Re-check
    // coverage once; a retired origin snapshot still fails explicitly.
    if(collected.get(key)===pending)collected.delete(key);
    if(retry&&!signal.aborted&&!nativeCacheIssue(error))return nativeRecords(refs,false);
    throw error;
   }
  };
  const frames=new Array<PackedRecord>(meta.page.length);let at=0,total=0;
  const seen=new Set<string>();
  const groups=new Map<string,{ref:ViewReference;position:number}[]>();
  // Validate the complete page before starting I/O. Group canonical rows by
  // source/hour, read in physical order, then restore the query's sort order.
  for(const [position,ref] of meta.page.entries()){
   if(!/^[a-f0-9]{64}$/.test(ref.key)||seen.has(ref.key)||!Number.isSafeInteger(ref.row)||ref.row<0||!bindings.some(b=>b.stream===ref.stream)||typeof ref.cached!=='boolean'||(ref.provisional!==undefined&&typeof ref.provisional!=='boolean'))throw Error('Invalid page reference');seen.add(ref.key);
   if(!ref.cached&&ref.native){
    const n=ref.native;
    if(!Number.isSafeInteger(n.from)||!Number.isSafeInteger(n.to)||n.from>=n.to||!/^[a-f0-9]{32}$/.test(n.generation))throw Error('Invalid segment reference');
    const key=JSON.stringify([ref.stream,n.generation,n.from,n.to]);
    const group=groups.get(key)??[];group.push({ref,position});groups.set(key,group);
   }else{
    const record=ref.cached?{stream:ref.stream,...held.get(ref.key)}:fresh[at++];
    if(!record?.bytes||record.stream!==ref.stream)throw Error('Missing PAC page member');
    frames[position]={bytes:record.bytes,common:record.common,native:record.native};
   }
  }
  const hours=[...groups.values()];let nextHour=0;
  const conflicts:unknown[]=[],failedResources=new Set<string>();
  const loadHours=async()=>{
   for(;;){
    signal.throwIfAborted();const group=hours[nextHour++];if(!group)return;
    const binding=bindings.find(b=>b.stream===group[0].ref.stream)!;
    const resource=binding.tap.split('/').slice(0,3).join('/');
    if(failedResources.has(resource))continue;
    group.sort((a,b)=>a.ref.row-b.ref.row);
    try{
     const records=await nativeRecords(group.map(item=>item.ref));
     group.forEach((item,i)=>{frames[item.position]=records[i]});
    }catch(error){
     if(nativeCacheIssue(error)?.code!=='native-cache-conflict')throw error;
     conflicts.push(error);failedResources.add(resource);
    }
   }
  };
  // Downloads can overlap; archive commands still serialize file publication.
  // Settle all workers before returning an error, so no query work is orphaned.
  const loads=await Promise.allSettled(Array.from({length:Math.min(4,hours.length)},loadHours));
  const failures=[...conflicts,...loads.flatMap(load=>load.status==='rejected'?[load.reason]:[])];
  if(failures.length)throw combineCacheErrors(failures);
  total=frames.reduce((bytes,frame)=>bytes+frame.bytes.length,0);
  const eventsEnded=performance.now();
  if(at!==fresh.length)throw Error('Unreferenced PAC member');
  this.coverage.validate(meta.buckets??[],bindings,meta.page);
  // Publish caches only after successful interchange completion and validation.
  frames.forEach((bytes,i)=>this.put(meta.page[i].key,bytes));
  while(this.bytes>32*1024*1024){const key=this.events.keys().next().value!;this.drop(key);this.coverage.evict(key)}
  this.coverage.add(meta.buckets??[],bindings,meta.page,key=>this.events.has(key));
  this.pages.set(pageKey,meta.page.map(r=>r.key));
  this.pages.set(JSON.stringify([scope,query.size,meta.cursor,query.include??"summary,page,statistics"]),meta.page.map(r=>r.key));
  while(this.pages.size>32)this.pages.delete(this.pages.keys().next().value!);
  const data=new Uint8Array(total),rows=new Uint32Array(frames.length*3);at=0;
  frames.forEach(({bytes},i)=>{data.set(bytes,at);rows.set([meta.page[i].stream,at,at+bytes.length],i*3);at+=bytes.length});
  const used=[...new Set(bindings.map(b=>b.digest))].map(digest=>{const {store,pil}=definitions.get(digest)!;return {digest,store,pil}});
  const pageTimings:ViewTimings={planning:planningEnded-started,request:requestEnded-requestStarted,response:responseEnded-requestEnded,summaries:0,events:eventsEnded-responseEnded,client:requestStarted-planningEnded+performance.now()-eventsEnded};
  const view:ViewResponse={timings:pageTimings,result:{...meta,summary:undefined,coverage:this.coverage.status(query.tap,Date.parse(query.from),Date.parse(query.to))},definitions:used,bindings,batch:{bytes:data.buffer,rows,common:frames.map(f=>f.common)},native:frames.map(f=>f.native)};
  // Publish a validated page from this exact snapshot before fetching chart data.
  // The receiver may transfer the batch buffers; only metadata is used below.
  signal.throwIfAborted();
  onPage?.(view);
  signal.throwIfAborted();
  const summariesStarted=performance.now();
  if(meta.analytics){
   if(!summary)throw Error('Missing summary base');
   const refs=meta.analytics.days;
   const days=await this.summaryDays.loadMany(refs,token,fetcher,signal,background,archive);
   summary=mergeDailyAnalytics(summary,meta.analytics,days);
   projection?.(meta.analytics,days);
  }
  if(query.include!=='page'&&!summary)throw Error('Missing full-range summary');
  if(summary){if(!Array.isArray(summary.counts)||!Array.isArray(summary.errors)||summary.counts.length!==summary.errors.length||summary.counts.length>128||![summary.from,summary.to,summary.step,summary.count,...summary.counts,...summary.errors].every(Number.isSafeInteger)||summary.step<=0||summary.to<summary.from||summary.count!==summary.counts.reduce((a,b)=>a+b,0)||summary.count<0||!['committed-snapshot','live-snapshot'].includes(summary.status)||summary.counts.some((n,i)=>n<0||summary.errors[i]<0||summary.errors[i]>n))throw Error('Invalid view summary')}
  if(summary)validateStatistics(summary);

  signal.throwIfAborted();
  if(summary){this.summaries.set(meta.summaryVersion,summary);this.versions.set(scope,meta.summaryVersion)}
  while(this.summaries.size>32)this.summaries.delete(this.summaries.keys().next().value!);
  while(this.versions.size>32)this.versions.delete(this.versions.keys().next().value!);
  view.result={...view.result,summary};
  view.timings={...pageTimings,summaries:performance.now()-summariesStarted};
  return view;
 }
}
