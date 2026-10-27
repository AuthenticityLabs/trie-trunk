import {NativeCacheError,nativeCacheIssue} from "./store/errors.ts";
const objectUUID=n=>`00000000-0000-7000-8000-${n.toString(16).padStart(12,'0')}`;
const objectBytes=n=>Uint8Array.from(Buffer.from(objectUUID(n).replaceAll('-',''),'hex'));
import {indexUUID} from './pil/pac-native.ts';
import {pacHeader} from "./pil/pac.ts";
import {pacPlacement,pacLocalRows,nativePAC} from "./pil/pac-native.ts";
import {test} from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {NativeStore,missing,resolve,committedRanges} from "./store/native.ts";
import {Codec} from "./pil/wasm.ts";
import {PILStream} from "./pil/stream.ts";
import {NativeTrie,NativeEvent} from "./pil/native-event.ts";
import {compile} from "./pil/metadata.ts";
import {valueAt} from "./dictionary.ts";
import {collectNative} from "./pil/native-collect.ts";
import { cbor } from "./pil/cbor.ts";
import { cb, le } from "./pil-fixtures.mjs";
const wire=new Uint8Array(readFileSync(new URL("./fixtures/native-ranges/ranges.pil",import.meta.url)));
const wasm=await WebAssembly.compile(readFileSync(new URL("../assets/pil.wasm",import.meta.url)));
async function fixture(){
 const records=[],definitions=new Map(),s=new PILStream(new Codec(await WebAssembly.instantiate(wasm)),new Map(),d=>definitions.set(d.digest,d),()=>{},async()=>{throw Error("expanded")},async r=>records.push(r));
 try{await s.feed(wire);s.finish();return {records,definitions};}finally{s.close();}
}
import { Backend } from "./native-backend-fixture.mjs";
function rebound(records){
 for(const r of records)if(r.kind==="manifest"&&r.manifest.TimeTrie)r.manifest.TimeTrie.set(r.manifest.Generation,48);
 const ids=new Map(records.filter(r=>r.kind==="manifest").map(r=>[r.binding.stream,r.manifest.Generation]));
 for(const r of records)if(r.kind==="pac"){
  const h=pacHeader(r.bytes,0,64<<20,new Set([1,nativePAC]));
  const source=h.extensions.find(e=>e.type===1).bytes,next=new TextEncoder().encode(r.binding.tap);
  if(source.length!==next.length)throw Error("test source length");source.set(next);
  const gen=ids.get(r.binding.stream);if(gen)h.extensions.find(e=>e.type===nativePAC).bytes.set(gen,0);
 }
 return records;
}
function parts(f){const p=f.records.find(r=>r.kind==="pac"),h=pacHeader(p.bytes,0,64<<20,new Set([1,nativePAC])),place=pacPlacement(h),idx=f.records.find(r=>r.kind==="chunk"&&r.file==="index");
 return {index:pacLocalRows(place,idx.bytes,Uint32Array.from({length:h.count},(_,i)=>p.bounds[i*2+1]-p.bounds[i*2])),bytes:p.bytes.subarray(h.start,h.end),start:Number(place.memberStart),common:h.common,record:p};}
const ingest=async(store,session,f,records=f.records)=>{for(const r of rebound(records))await store.record(session,r,f.definitions.get(r.binding.digest));};
function generationRecords(f,sequence,variant=0){
 const records=structuredClone(f.records);
 for(const record of records)if(record.kind==="manifest"){
  record.manifest.GenerationSequence=sequence;record.manifest.Generation[0]^=variant;
 }
 return records;
}
test("explicit scoped rebase survives restart, cannot resurrect stale sessions, and never reuses old row references",async()=>{
 const f=await fixture(),backend=new Backend(),store=new NativeStore(backend),first=store.begin();
 const original=generationRecords(f,3);
 await ingest(store,first,f,original);await store.finish(first);
 const [old]=await backend.list();
 const elsewhere=structuredClone(original);for(const record of elsewhere)record.binding.tap="test/payment/transactions.v1/write";
 const other=store.begin();await ingest(store,other,f,elsewhere);await store.finish(other);
 const pending=store.begin();await ingest(store,pending,f,original);
 const notYetStarted=store.begin();
 backend.fail=true;await assert.rejects(store.rebase(old.tap),/crash/);
 assert.equal(await store.serial(),0);assert.equal((await store.coverage()).length,2);
 assert.equal(await store.rebase(old.tap),1);assert.equal(await store.serial(),1);
 assert.equal((await store.coverage()).length,1);
 await assert.rejects(store.finish(pending),/cache rebased/);
 await assert.rejects(ingest(store,notYetStarted,f,original),/cache rebased/);
 await assert.rejects(store.dependencies(old.key),/retired/);
 await store.maintain();
 const restored=new NativeStore(backend),fresh=restored.begin(await restored.serial());
 // A deliberate server rollback can reuse a physical generation and lower its
 // sequence. Only this explicit reset permits it; new local addresses differ.
 await ingest(restored,fresh,f,generationRecords(f,1));await restored.finish(fresh);
 const next=(await backend.list()).find(s=>s.tap===old.tap&&!s.retiredBy);
 assert.notEqual(next.key,old.key);assert.equal(next.generation,old.generation);
 assert.equal(next.generationSequence,1);assert.equal((await restored.coverage()).length,2);
 await assert.rejects(restored.page(old.key,0,Date.now()),/retired/);
 const normal=restored.begin(await restored.serial());await ingest(restored,normal,f,elsewhere);await restored.finish(normal);
 assert.equal((await restored.coverage()).length,2,"unrelated cache kept its stable source key");
});
test("sequenced generations retire atomically, reject delayed responses, and reclaim without rebinding rows",async()=>{
 const f=await fixture(),b=new Backend(),store=new NativeStore(b);
 const first=store.begin();await ingest(store,first,f,generationRecords(f,1));await store.finish(first);
 const old=(await b.list())[0],pending=store.begin();
 await ingest(store,pending,f,generationRecords(f,1));
 const replacement=store.begin();await ingest(store,replacement,f,generationRecords(f,2,1));
 b.fail=true;await assert.rejects(store.finish(replacement),/crash/);
 assert.equal((await store.coverage()).length,1);assert.equal((await b.get(old.key)).retiredBy,undefined);
 await store.finish(replacement);
 const current=(await b.list()).find(s=>s.generationSequence===2);
 assert.equal((await b.get(old.key)).retiredBy,current.key);
 assert.equal((await store.coverage()).length,1);
 await assert.rejects(store.finish(pending),/retired/);
 await assert.rejects(store.dependencies(old.key),/retired/);
 await assert.rejects(store.page(old.key,0,Date.now()),/retired/);
 assert.deepEqual(await store.activeSelections([{source:old.key},{source:current.key}]),[{source:current.key}]);
 const remove=b.removeFiles.bind(b);b.removeFiles=async()=>{throw Error("temporary deletion failure");};
 await assert.rejects(store.reclaim(),/temporary deletion/);
 assert.equal((await store.coverage()).length,1);
 await assert.rejects(store.dependencies(old.key),/retired/);
 b.removeFiles=remove;
 assert.equal(await store.reclaim(),1);assert.equal(await store.reclaim(),0);
 assert.equal([...b.files.keys()].some(k=>k.startsWith(old.key)),false);
 assert.equal((await b.get(old.key)).reclaimed,true);
 const retry=new NativeStore(b),stale=retry.begin();
 await assert.rejects(ingest(retry,stale,f,generationRecords(f,1)),/retired/);
 assert.equal((await retry.coverage()).length,1);
});
test("generation ordering rejects mixed responses, equal sequence forks and unsequenced downgrades",async()=>{
 const f=await fixture();
 for(const sequence of [2,undefined]){
  const b=new Backend(),store=new NativeStore(b),first=store.begin();
  await ingest(store,first,f,generationRecords(f,2));await store.finish(first);
  const other=store.begin();await ingest(store,other,f,generationRecords(f,sequence,1));
  await assert.rejects(store.finish(other),/Stale or conflicting|Unordered/);
  assert.equal((await store.coverage()).length,1);
 }
 const b=new Backend(),store=new NativeStore(b),mixed=store.begin();
 await ingest(store,mixed,f,generationRecords(f,1));await ingest(store,mixed,f,generationRecords(f,2,1));
 await assert.rejects(store.finish(mixed),/Generation changed during/);
 assert.deepEqual(await store.coverage(),[]);
});
async function stateRecords(f, pages) {
 const result=[];
 for(const record of f.records){
  if(record.kind==="manifest"){
   const copy=structuredClone(record);
   if(pages[0]?.Sequence!==undefined)copy.manifest.StateSequence=pages[0].Sequence;
   if(pages[0]?.Epoch!==undefined)copy.manifest.StateEpoch=pages[0].Epoch;
   result.push(copy);continue;
  }
  if(record.kind!=="chunk"||record.file!=="state"){result.push(record);continue;}
  const old=cbor(record.bytes);
  for(const {start=0,...page} of pages){
   const bytes=cb({...old,...page});
   result.push({...record,start,bytes,digest:Buffer.from(await crypto.subtle.digest("SHA-256",bytes)).toString("hex")});
  }
 }
 return result;
}
test("state compaction preserves tokens and rejects in-flight file offsets, including failed publication and cleanup",async()=>{
 const f=await fixture(),backend=new Backend(),store=new NativeStore(backend);
 const first=store.begin();await ingest(store,first,f);await store.finish(first);
 const second=store.begin(),newer=await stateRecords(f,[{Paged:true,Final:true,Sequence:2,Objects:[{ID:objectBytes(42),Token:2}]}]);
 await ingest(store,second,f,newer);await store.finish(second);
 const [source]=await backend.list(),query={source:source.tap,id:objectUUID(42)};
 const observed=await store.objectState(query),coverage=await store.coverage();
 const pending=store.begin();await ingest(store,pending,f,newer);
 backend.fail=true;await assert.rejects(store.compactState(source.key,true),/crash/);
 assert.deepEqual(await store.objectState(query),observed);
 assert.equal((await backend.get(source.key)).stateFileRevision,undefined);
 const clean=backend.removeOldStateFiles.bind(backend);backend.removeOldStateFiles=async()=>{throw Error("deletion failure");};
 await assert.rejects(store.compactState(source.key,true),/deletion/);
 assert.equal((await backend.get(source.key)).stateFileRevision,1);
 assert.deepEqual(await store.objectState(query),observed);
 await assert.rejects(store.finish(pending),/state compacted/);
 backend.removeOldStateFiles=clean;
 const reloaded=new NativeStore(backend);await reloaded.maintain();
 assert.equal([...backend.files.keys()].filter(k=>k.startsWith(source.key+"state")).length,1);
 assert.ok((await backend.get(source.key)).files.state.end<source.files.state.end);
 assert.deepEqual(await reloaded.coverage(),coverage);
 const retry=reloaded.begin();await ingest(reloaded,retry,f,newer);await reloaded.finish(retry);
 assert.deepEqual(await reloaded.objectState(query),observed);
 const unchanged=await stateRecords(f,[{Paged:true,Final:true,Unchanged:true,Sequence:2,Objects:null}]);
 const warm=reloaded.begin();await ingest(reloaded,warm,f,unchanged);await reloaded.finish(warm);
 assert.deepEqual(await reloaded.objectState(query),observed);
});
test("automatic state maintenance compacts only above the stale-byte threshold",async()=>{
 const f=await fixture(),backend=new Backend(),store=new NativeStore(backend);
 const session=store.begin();await ingest(store,session,f);await store.finish(session);
 const [source]=await backend.list(),query={source:source.tap,id:objectUUID(42)};
 const token=await store.objectState(query),file=await backend.file(source.key,"state");
 file.truncate(1<<20);source.files.state.end=1<<20;await backend.put([source]);
 await store.maintain();assert.equal((await backend.get(source.key)).stateFileRevision,undefined);
 file.truncate((1<<20)+1);source.files.state.end=(1<<20)+1;await backend.put([source]);
 await store.maintain();assert.equal((await backend.get(source.key)).stateFileRevision,1);
 assert.deepEqual(await store.objectState(query),token);
 assert.ok((await backend.get(source.key)).files.state.end<1024);
});
test("paged invalidations publish atomically, survive reload, and never regress on a late completion",async()=>{
 const f=await fixture(),b=new Backend(),s=new NativeStore(b),first=s.begin();
 const query={source:f.records[0].binding.tap,id:objectUUID(42)};
 await ingest(s,first,f);
 await assert.rejects(s.objectState(query),/No committed/);
 await s.finish(first);
 const original=await s.objectState(query);
 const newer=await stateRecords(f,[
  {Paged:true,Final:false,Sequence:3,Objects:[{ID:objectBytes(42),Token:2}]},
  {Paged:true,Final:true,Sequence:3,start:1,Objects:[{ID:objectBytes(43),Token:3}]},
 ]);
 const old=await stateRecords(f,[{Paged:true,Final:true,Sequence:1,Objects:[{ID:objectBytes(42),Token:1}]}]);
 const a=s.begin(),z=s.begin();await ingest(s,a,f,newer);await ingest(s,z,f,old);
 assert.equal((await s.objectState(query)).token,original.token);
 await s.finish(a);await s.finish(z);
 const restarted=new NativeStore(b),observed=await restarted.objectState(query);
 assert.equal(observed.sequence,3);assert.notEqual(observed.token,original.token);
 assert.equal(observed.token.slice(-16),"0200000000000000");
 assert.equal((await restarted.objectState({...query,id:objectUUID(43)})).token.slice(-16),"0300000000000000");
 assert.equal((await restarted.objectState({...query,id:objectUUID(44)})).token.slice(-16),"0000000000000000");
 const [coverage]=await restarted.coverage();assert.equal(coverage.StateKnown,true);assert.equal(coverage.StateSequence,3);
 const unchanged=await stateRecords(f,[{Paged:true,Final:true,Unchanged:true,Sequence:3,Objects:null}]);
 const sizes=(await b.list()).map(s=>s.files.state.end);
 const retry=restarted.begin();await ingest(restarted,retry,f,unchanged);await restarted.finish(retry);
 assert.deepEqual((await b.list()).map(s=>s.files.state.end),sizes);
});
test("state pages reject missing, conflicting, duplicate or unbound snapshots",async()=>{
 const f=await fixture();
 for(const pages of [
  [{Paged:true,Final:false,Sequence:1,Objects:[{ID:objectBytes(42),Token:1}]}],
  [{Paged:true,Final:true,Unchanged:true,Sequence:1,Objects:null}],
  [{Paged:true,Final:true,Sequence:1,Objects:[{ID:objectBytes(42),Token:2}]}],
  [{Paged:true,Final:false,Sequence:2,Objects:[{ID:objectBytes(42),Token:1}]},{Paged:true,Final:true,Sequence:2,start:1,Objects:[{ID:objectBytes(42),Token:2}]}],
  [{Paged:true,Final:false,Sequence:2,Objects:[{ID:objectBytes(42),Token:1}]},{Paged:true,Final:true,Sequence:3,start:1,Objects:[{ID:objectBytes(43),Token:3}]}],
 ]){
  const b=new Backend(),s=new NativeStore(b),session=s.begin(),records=await stateRecords(f,pages);
  await assert.rejects(async()=>{await ingest(s,session,f,records);await s.finish(session);});
  assert.deepEqual(await s.coverage(),[]);
  await assert.rejects(s.objectState({source:f.records[0].binding.tap,id:objectUUID(42)}),/No committed/);
 }
});
test("Go paged state protocol crosses WASM and commits exact object tokens across hourly packages",async()=>{
 const bytes=new Uint8Array(readFileSync(new URL("./fixtures/state-pages/ranges.pil",import.meta.url)));
 const b=new Backend(),store=new NativeStore(b),session=store.begin(),definitions=new Map();
 const stream=new PILStream(new Codec(await WebAssembly.instantiate(wasm)),definitions,()=>{},()=>{},async()=>{throw Error("expanded event");},
  async record=>store.record(session,record,definitions.get(record.binding.digest)));
 try{
  for(let i=0;i<bytes.length;i+=173)await stream.feed(bytes.subarray(i,i+173));
  stream.finish();await store.finish(session);
 }finally{stream.close();}
 const [source]=await b.list();assert.ok(source.timeTrie);assert.equal(source.state.pages.length,2);
 const state=await store.objectState({source:source.tap,id:objectUUID(42)});
 assert.equal(state.sequence,1200);assert.equal(state.token.slice(-16),"2b00000000000000");
 assert.equal(source.files.state.blobs.length,2); // second hour reuses the same snapshot
});
test("native file ranges persist original bytes and only publish completed response coverage",async()=>{
 const f=await fixture(),b=new Backend(),store=new NativeStore(b),session=store.begin();
 await ingest(store,session,f);
 assert.deepEqual(await store.coverage(),[]);
 await store.finish(session);
 const [source]=await b.list(),native=parts(f),index={bytes:native.index},pop=native;
 assert.deepEqual(await store.read(source.key,"index",{Start:16,End:16+index.bytes.length}),index.bytes);
 assert.deepEqual(await store.read(source.key,"population",{Start:pop.start,End:pop.start+pop.bytes.length}),pop.bytes);
 const reloaded=new NativeStore(b),[coverage]=await reloaded.coverage();
 assert.deepEqual(coverage.Index,[{Start:1,End:7}]);
 assert.equal(source.files.population.end,(1<<20)+8); // no fake holes before lazy payload
 const size=[...b.files.values()].reduce((n,v)=>n+v.getSize(),0),retry=reloaded.begin();
 await ingest(reloaded,retry,f);await reloaded.finish(retry);
 assert.equal([...b.files.values()].reduce((n,v)=>n+v.getSize(),0),size); // retry is byte-for-byte deduped
});
test("interrupted response may reuse durable bytes but cannot certify a package",async()=>{
 const f=await fixture(),b=new Backend(),s=new NativeStore(b),pending=s.begin();
 await ingest(s,pending,f,f.records.slice(0,-1));
 await assert.rejects(s.finish(pending),/Incomplete/);
 assert.deepEqual(await new NativeStore(b).coverage(),[]);
 const size=[...b.files.values()].reduce((n,v)=>n+v.getSize(),0),retry=s.begin();
 await ingest(s,retry,f);await s.finish(retry);
 assert.equal([...b.files.values()].reduce((n,v)=>n+v.getSize(),0),size);
});
test("failed metadata publication recovers the orphan file tail",async()=>{
 const f=await fixture(),b=new Backend(),s=new NativeStore(b),session=s.begin();
 await ingest(s,session,f,[f.records[0]]);
 const before=f.records.slice(1).filter(r=>r.kind!=="pac"&&r.kind!=="package-end");await ingest(s,session,f,before);
 const pac=f.records.find(r=>r.kind==="pac");
 b.fail=true;await assert.rejects(s.record(session,pac),/crash/);
 const [source]=await b.list();assert.equal(source.files.population.end,0);
 await s.record(session,pac);
 assert.equal((await b.file(source.key,"population")).getSize(),(1<<20)+8);

});
test("interleaved responses reload shared metadata and retain both commits",async()=>{
 const f=await fixture(),b=new Backend(),a=new NativeStore(b),other=new NativeStore(b),sa=a.begin(),sb=other.begin();
 for(const r of f.records){await a.record(sa,r,f.definitions.get(r.binding.digest));await other.record(sb,r,f.definitions.get(r.binding.digest));}
 await a.finish(sa);await other.finish(sb);
 const [source]=await b.list();assert.equal(source.files.index.end,(1<<20)+8);assert.equal(source.contexts.length,1);
});
test("same generation conflicting bytes reject; new generation isolates references",async()=>{
 const f=await fixture(),b=new Backend(),s=new NativeStore(b),session=s.begin();
 await ingest(s,session,f);await s.finish(session);
 const second=s.begin();await ingest(s,second,f,[f.records[0]]);
 const index=structuredClone(f.records.find(r=>r.kind==="chunk"&&r.file==="index"));index.bytes[15]^=1;
 index.digest=Buffer.from(await crypto.subtle.digest("SHA-256",index.bytes)).toString("hex");
 await s.record(second,index);
 await assert.rejects(s.record(second,f.records.find(r=>r.kind==="pac")),/generation change|member start mismatch/);
 const changed=structuredClone(f.records);changed[0].manifest.Generation[0]^=1;changed[0].manifest.GenerationSequence+=1;
 const fresh=s.begin();await ingest(s,fresh,f,changed);await s.finish(fresh);
 assert.equal((await b.list()).length,2);
 assert.equal((await s.coverage()).length,1);
 assert.equal((await s.coverage("demo/payment/*/write")).length,1);
 assert.equal((await s.coverage("demo/customer/*/*")).length,0);
});
test("manifest cannot certify unsent rows or missing dependencies",async()=>{
 const f=await fixture(),b=new Backend(),s=new NativeStore(b),session=s.begin();
 await assert.rejects(ingest(s,session,f,f.records.filter(r=>r.kind!=="chunk"||r.file!=="index")),/gap|Missing PAC index/);
 await assert.rejects(s.finish(session),/Incomplete|unfinished|gap/i);assert.deepEqual(await s.coverage(),[]);
});
test("interval difference and extent resolution support interlaced gap fills",()=>{
 assert.deepEqual(missing([{Start:0,End:10}],[{Start:2,End:4},{Start:6,End:8}]),[{Start:0,End:2},{Start:4,End:6},{Start:8,End:10}]);
 assert.deepEqual(resolve([{Start:10,End:20,local:0},{Start:0,End:10,local:10}],{Start:5,End:15}),[{Start:5,End:10,local:15},{Start:10,End:15,local:0}]);
 assert.throws(()=>resolve([{Start:0,End:3,local:0}],{Start:0,End:4}),/gap/);
});
test("native view reads index identity, shared trie values and original PIL population without assembly",async()=>{
 const f=await fixture(),binding=f.records[0].binding,metadata=f.definitions.get(binding.digest),mapping=compile(metadata,binding.tap);
 const trie=new NativeTrie(mapping);
 for(const r of f.records)if(r.kind==="chunk"&&r.file==="trie")trie.append(r.start,r.bytes);
 const index=f.records.find(r=>r.kind==="chunk"&&r.file==="index").bytes;
 const native=parts(f),bounds=native.record.bounds;const pop={bytes:native.record.bytes.subarray(bounds[4],bounds[5])};
 const codec=new Codec(await WebAssembly.instantiate(wasm)),h=codec.define(metadata.pil);
 codec.upload(pop.bytes);assert.equal(codec.scan(h,0,pop.bytes.length),pop.bytes.length);
 // Lazy population is the third event (20 minutes into the hour).
 const row=index.subarray(2*16,3*16),event=new NativeEvent(row,pop.bytes,trie,native.common,Date.UTC(2026,9,16,12));
 assert.equal(event.millis,Date.UTC(2026,9,16,12,20));
 assert.equal(event.uuid,String.fromCharCode(...indexUUID(row,Date.UTC(2026,9,16,12))));
 assert.equal(valueAt(event,"state"),"pending");
 assert.equal(valueAt(event,"message"),"request completed");
 assert.equal(valueAt(event,"duration"),13);
 assert.equal(valueAt(event,"id"),objectUUID(42));
 assert.ok(event.orderedKeys().includes(mapping.dictionary.id.key));
 assert.ok(event.orderedKeys().length>3);
 assert.equal(row.length,16); // No implicit trie reference in the index.
 codec.release(h);
});
test("native pages binary-search cached index rows and demand only exact payload ranges",async()=>{
 const f=await fixture(),b=new Backend(),store=new NativeStore(b),session=store.begin();
 await ingest(store,session,f);await store.finish(session);
 const [source]=await b.list(),from=Date.UTC(2026,9,16,12,12),to=Date.UTC(2026,9,16,12,28);
 const page=await store.page(source.key,from,to);
 assert.equal(page.rows.length,16);assert.equal(page.after,3);assert.equal(page.done,true);
 const native=parts(f);assert.deepEqual(page.population,native.record.bytes.subarray(native.record.bounds[4],native.record.bounds[5]));
 const deps=await store.dependencies(source.key),trie=new NativeTrie(compile(deps.definition,source.tap));
 for(const chunk of deps.trie)trie.append(chunk.start,chunk.bytes);
 assert.equal(valueAt(new NativeEvent(page.rows,page.population,trie,page.common[0],page.bases[0]),"revision_id"),3);
 const next=await store.page(source.key,from,to,page.after);assert.equal(next.rows.length,0);assert.equal(next.done,true);
 assert.equal((await store.page(source.key,from-12*60000,to)).rows.length,3*16);
});
test("Record starts cross sparse page boundaries with a retained header",async()=>{
 const f=await fixture(),backend=new Backend(),store=new NativeStore(backend),session=store.begin();
 await ingest(store,session,f);await store.finish(session);
 const [source]=await backend.list(),from=Date.UTC(2026,9,16,12),to=Date.UTC(2026,9,16,13);
 const expected=await store.page(source.key,from,to);
 assert.equal(source.contexts.length,1);
 assert.equal(source.contexts[0].placement.firstRow,1n);
 const tail=committedRanges(source).index.at(-1).End,raw=await store.read(source.key,"index",{Start:0,End:tail*16});
 const header=new DataView(raw.buffer,raw.byteOffset,16);
 assert.equal(header.getBigInt64(4,true),source.contexts[0].placement.from);
 assert.equal(header.getUint8(12),2);
 // Place the first event on a logical 1 MiB boundary, with its header in the
 // preceding page. Reverse physical pages to model out-of-order collection.
 const size=1<<20,shift=size/16-1,start=shift*16,index=await backend.file(source.key,"index");
 index.truncate(2*size+8);
 index.data.fill(0);
 new DataView(index.data.buffer).setBigUint64(0,BigInt(start),true);
 index.write(raw.subarray(0,16),{at:8+2*size-16});
 index.write(raw.subarray(16),{at:8});
 source.files.index={...source.files.index,end:2*size+8,retired:start,pages:{0:size+8,1:8},extents:[
  {Start:start,End:size,local:8+2*size-16},
  {Start:size,End:start+raw.length,local:8},
 ]};
 source.retiredRows=shift;
 source.contexts[0].placement.firstRow+=BigInt(shift);
 await backend.put([source]);
 const actual=await new NativeStore(backend).page(source.key,from,to);
 assert.deepEqual(actual.rows,expected.rows);
 assert.deepEqual(actual.population,expected.population);
 assert.deepEqual(actual.offsets,expected.offsets);
 assert.equal(actual.after,expected.after+shift);
});
test("known-empty packages and provisional status survive reload without inventing rows",async()=>{
 const f=await fixture(),records=structuredClone(f.records.filter(r=>r.kind!=="pac"&&(r.kind!=="chunk"||!["index","population"].includes(r.file))));
 records[0].manifest.Index=[];records[0].manifest.Population=[];records[0].manifest.IndexBases=[];records[0].manifest.Provisional=true;delete records[0].manifest.TimeTrie;
 const backend=new Backend(),store=new NativeStore(backend),session=store.begin();
 await ingest(store,session,f,records);await store.finish(session);
 const [source]=await backend.list();assert.equal(source.timeTrie,undefined);assert.deepEqual(committedRanges(source),{index:[],sods:[]});
 const reloaded=new NativeStore(backend),page=await reloaded.page(source.key,Date.UTC(2026,9,16,12),Date.UTC(2026,9,16,13));
 assert.equal(page.rows.length,0);assert.equal(page.done,true);assert.equal((await reloaded.coverage()).length,1);
});
test("native collector performs one GET and commits coverage only after wire success",async()=>{
 const backend=new Backend(),store=new NativeStore(backend),sessions=new Map(),codec=new Codec(await WebAssembly.instantiate(wasm)),definitions=new Map();
 let compactBeforeFinish=0;
 const calls=[];
 const archive=async(token,command)=>{
  calls.push(command.kind);
  if(command.kind==="native-begin")sessions.set(command.session,store.begin());
  for(const item of command.records??[])await store.record(sessions.get(command.session),item.record,item.definition);
  if(command.kind==="native-record")await store.record(sessions.get(command.session),command.record,command.definition);
  if(command.kind==="native-finish"){
   if(compactBeforeFinish>0){compactBeforeFinish--;await store.compactState((await backend.list())[0].key,true);}
   const session=sessions.get(command.session);await store.finish(session);sessions.delete(command.session);
   return {nativeSources:[...new Set(session.packages.map(p=>p.key))]};
  }
  if(command.kind==="native-abort")sessions.delete(command.session);
  if(command.kind==="native-coverage")return {nativeCoverage:await store.coverage(command.tap)};
  return {};
 };
 let requests=0,truncated=true;
 const options={tap:"demo/payment/transactions.v1/write",from:"2026-10-16T12:12:00Z",to:"2026-10-16T12:28:00Z",token:"",signal:new AbortController().signal,codec,definitions,archive,fetcher:async(url,init)=>{
  requests++;assert.equal(init.method,"GET");assert.match(url,/format=native/);assert.equal(init.headers["PIL-Features"],"native-ranges,native-state-pages,adaptive-integers");
  assert.equal(init.headers["Definition-Version"],"");
  return new Response(truncated?wire.subarray(0,wire.length-1):wire,{headers:{"Content-Type":"application/pil","PIL-Version":"2","Definition-Version":"a".repeat(64),"Trunk-Format":"native-pac/0","Trunk-Completion":"body"}});
 }};
 await assert.rejects(collectNative(options),/completion/);
 assert.equal(requests,1);assert.equal(calls.at(-1),"native-abort");assert.deepEqual(await store.coverage(),[]);
 truncated=false;const sources=await collectNative(options);
 assert.ok(!calls.includes("native-record"));assert.ok(!calls.includes("native-records"),"small responses travel with finish as one bounded batch");
 assert.equal(requests,2);assert.equal(sources.length,1);assert.equal(calls.at(-1),"native-finish");assert.equal((await store.coverage()).length,1);assert.equal(sessions.size,0);
 assert.equal((await new NativeStore(backend).definitions()).length,1);
 compactBeforeFinish=1;
 assert.deepEqual(await collectNative(options),sources);assert.equal(requests,4);assert.equal(sessions.size,0);
 compactBeforeFinish=2;
 await assert.rejects(collectNative(options),/state compacted/);assert.equal(requests,6);assert.equal(sessions.size,0);
});

test("committed sources retain a time trie without package manifests or redundant coverage",async()=>{
 const f=await fixture(),backend=new Backend(),store=new NativeStore(backend),session=store.begin();
 const records=f.records.map(record=>{
  if(record.kind!=="manifest")return record;
  const m={...record.manifest},buffer=new Uint8Array(2<<20);let offset=123;
  for(const key of ["Definition","Generation","StateEpoch"]){
   if(!(m[key] instanceof Uint8Array))continue;
   const length=m[key].length;buffer.set(m[key],offset);m[key]=buffer.subarray(offset,offset+length);offset+=length;
  }
  return {...record,manifest:m};
 });
 await ingest(store,session,f,records);await store.finish(session);
 for(const source of await backend.list()){
  assert.equal(source.packages,undefined);assert.equal(source.index,undefined);assert.equal(source.population,undefined);
  assert.equal(source.timeTrie.buffer.byteLength,source.timeTrie.byteLength);
 }
 const restored=new NativeStore(backend);assert((await restored.coverage()).length>0);
});

test("positional completeness publishes atomically and distinguishes empty from unknown",async()=>{
 const f=await fixture(),backend=new Backend(),store=new NativeStore(backend),session=store.begin();
 await ingest(store,session,f);
 const pending=(await backend.list())[0],m=f.records.find(r=>r.kind==="manifest").manifest;
 assert.equal((await store.missingTime(pending.key,m.From*1000,m.To*1000)).length,1);
 backend.fail=true;await assert.rejects(store.finish(session),/crash/);
 assert.equal((await store.missingTime(pending.key,m.From*1000,m.To*1000)).length,1);
 await store.finish(session);
 assert.deepEqual(await new NativeStore(backend).missingTime(pending.key,m.From*1000,m.To*1000),[]);
 const records=structuredClone(f.records.filter(r=>r.kind!=="pac"&&(r.kind!=="chunk"||!["index","population"].includes(r.file))));
 const empty=records.find(r=>r.kind==="manifest").manifest;
 empty.Index=[];empty.Population=[];empty.Tail=0;
 const patch=empty.TimeTrie,v=new DataView(patch.buffer,patch.byteOffset,patch.byteLength);
 v.setBigUint64(24,0n,true);patch.fill(0,80);
 const stride=patch.slice(40,44).reduce((a,b)=>a+b,0)+1;for(let at=80;at<patch.length;at+=stride)patch[at+stride-1]=2;
 const other=new Backend(),target=new NativeStore(other),tx=target.begin();
 await ingest(target,tx,f,records);await target.finish(tx);
 const source=(await other.list())[0];
 assert.deepEqual(await target.missingTime(source.key,m.From*1000,m.To*1000),[]);
 assert.equal((await target.missingTime(source.key,m.To*1000,(m.To+3600)*1000)).length,1);
});


test("equal generation sequence conflicts carry evidence and scoped recovery keeps other resources",async()=>{
 const f=await fixture(),backend=new Backend(),store=new NativeStore(backend);
 const original=generationRecords(f,1),first=store.begin();await ingest(store,first,f,original);await store.finish(first);
 const [old]=await backend.list();
 const elsewhere=structuredClone(original);for(const r of elsewhere)r.binding.tap="test/payment/transactions.v1/write";
 const other=store.begin();await ingest(store,other,f,elsewhere);await store.finish(other);
 const fresh=generationRecords(f,1,1),conflict=store.begin();await ingest(store,conflict,f,fresh);
 await assert.rejects(store.finish(conflict),error=>{
  const issue=nativeCacheIssue(error);assert.equal(issue.code,'native-cache-conflict');assert.equal(issue.source,old.tap);
  assert.equal(issue.reason,'generation-fork');assert.equal(issue.detail.cachedSequence,1);assert.equal(issue.detail.incomingSequence,1);
  assert.notEqual(issue.detail.cachedGeneration,issue.detail.incomingGeneration);return true;
 });
 assert.equal((await backend.get(old.key)).retiredBy,undefined,'failed collection damaged published cache');
 await store.rebase(old.tap);const retry=store.begin(await store.serial());await ingest(store,retry,f,fresh);await store.finish(retry);
 const active=(await backend.list()).filter(s=>!s.retiredBy&&s.state);
 assert.equal(active.length,2);assert.ok(active.some(s=>s.tap==='test/payment/transactions.v1/write'));
 assert.notEqual(active.find(s=>s.tap===old.tap).key,old.key);
});

test("collector retries publication races once but never loops or resets a conflicting cache",async()=>{
 const codec=new Codec(await WebAssembly.instantiate(wasm));
 for(const code of ['native-cache-race','native-cache-conflict']){
  let requests=0,aborts=0,finishes=0;
  const issue={code,source:'demo/payment/transactions.v1/write',reason:'injected'};
  const options={tap:issue.source,from:'2026-10-16T12:00:00Z',to:'2026-10-16T13:00:00Z',token:'',signal:new AbortController().signal,codec,definitions:new Map(),
   archive:async(_,c)=>{assert.notEqual(c.kind,'native-rebase');if(c.kind==='native-abort')aborts++;if(c.kind==='native-finish'){finishes++;throw new NativeCacheError('injected',issue)}return {};},
   fetcher:async()=>{requests++;return new Response(wire,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Definition-Version':'a'.repeat(64),'Trunk-Format':'native-pac/0','Trunk-Completion':'body'}})}};
  await assert.rejects(collectNative(options),e=>nativeCacheIssue(e)?.code===code);
  assert.equal(requests,code==='native-cache-race'?2:1);assert.equal(aborts,requests);assert.equal(finishes,requests);
 }
});

test('batch cache refresh commits every scope atomically and preserves healthy resources',async()=>{
 const f=await fixture(),backend=new Backend(),store=new NativeStore(backend);
 const sources=['demo/payment/transactions.v1/write','test/payment/transactions.v1/write','else/payment/transactions.v1/write'];
 for(const source of sources){
  const records=generationRecords(f,1);for(const r of records)r.binding.tap=source;
  const session=store.begin();await ingest(store,session,f,records);await store.finish(session);
 }
 const before=await backend.list(),pending=store.begin();
 const targets=[sources[0],sources[1],sources[0].replace('/write','/read')];
 await assert.rejects(store.rebaseMany([...targets,'invalid']),/source|Source|collection|Collection/);
 assert.equal(await store.serial(),0);assert.equal((await store.coverage()).length,3);
 backend.fail=true;await assert.rejects(store.rebaseMany(targets),/crash/);
 assert.equal(await store.serial(),0);assert.equal((await store.coverage()).length,3);
 assert.equal(await store.rebaseMany(targets),2);assert.equal(await store.serial(),1);
 assert.deepEqual((await backend.control()).scopes,{'demo/payment/transactions.v1':1,'test/payment/transactions.v1':1});
 assert.equal((await store.coverage()).length,1);
 assert.deepEqual(await backend.get(before.find(s=>s.tap===sources[2]).key),before.find(s=>s.tap===sources[2]));
 await assert.rejects(ingest(store,pending,f),/cache rebased/);
 for(const source of sources.slice(0,2)){
  const records=generationRecords(f,1,1);for(const r of records)r.binding.tap=source;
  const session=store.begin(await store.serial());await ingest(store,session,f,records);await store.finish(session);
 }
 assert.equal((await store.coverage()).length,3);
});

test('range collection publishes every confirmed zero hour after durable data writes',async()=>{
 const raw=new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/multi-hour.pil',import.meta.url)));
 const backend=new Backend(),store=new NativeStore(backend),session=store.begin(),definitions=new Map();
 const stream=new PILStream(new Codec(await WebAssembly.instantiate(wasm)),definitions,d=>definitions.set(d.digest,d),()=>{},async()=>{throw Error('expanded')},r=>store.record(session,r,definitions.get(r.binding.digest)));
 try{await stream.feed(raw);stream.finish();}finally{stream.close();}
 assert.deepEqual(await store.coverage(),[],'received metadata is not committed coverage');
 await store.finish(session);
 const [source]=await backend.list();const from=Date.parse('2026-10-01T00:00:00Z'),to=from+6*3600000;
 const {missingHours}=await import('./store/time-trie.ts');
 assert.deepEqual(missingHours(source.timeTrie,from,to),[],'including the three zero-event hours');
 assert.equal(missingHours(source.timeTrie,to,to+3600000).length,1,'unreceived hour stays blank');
});
