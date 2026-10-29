import {NativeCacheError,nativeCacheIssue} from "./store/errors.ts";
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Codec} from './pil/wasm.ts';
import {PILStream} from './pil/stream.ts';
import {ViewCache} from './pil/view.ts';
import {viewEvents} from './pil/view-events.ts';
import {NativeStore,committedRanges} from './store/native.ts';
import {Backend} from './native-backend-fixture.mjs';
import {bytes,frame,le,magic} from './pil-fixtures.mjs';
import {valueAt} from './dictionary.ts';
import {SearchSession} from './search-session.ts';
const wire=readFileSync(new URL('./fixtures/native-ranges/ranges.pil',import.meta.url));
const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
test('sealed views collect a complete hour and expose canonical bytes without server record assembly',async()=>{
 const codec=new Codec(await WebAssembly.instantiate(wasm)),definitions=new Map(),bindings=[],records=[];
 const parser=new PILStream(codec,definitions,()=>{},b=>bindings.push(b),async()=>assert.fail('expanded native event'),async r=>records.push(r));
 await parser.feed(wire);parser.finish();parser.close();
 const binding=bindings[0],manifest=records.find(r=>r.kind==='manifest').manifest;
 const generation=Buffer.from(manifest.Generation).toString('hex'),from=Date.UTC(2026,9,16,12),to=from+3600000;
 const backend=new Backend(),store=new NativeStore(backend),sessions=new Map();
 let evicted=false,reads=0;
 const archive=async(token,c)=>{
  if(c.kind==='native-rows'&&evicted){evicted=false;throw Error('Native source evicted')}
  if(c.kind==='native-begin')sessions.set(c.session,store.begin(await store.serial()));
  for(const item of c.records??[])await store.record(sessions.get(c.session),item.record,item.definition);
  if(c.kind==='native-record')await store.record(sessions.get(c.session),c.record,c.definition);
  if(c.kind==='native-finish'){const session=sessions.get(c.session);await store.finish(session);sessions.delete(c.session);return {nativeSources:[...new Set(session.packages.map(p=>p.key))]}}
  if(c.kind==='native-abort')sessions.delete(c.session);
  if(c.kind==='native-coverage')return {nativeCoverage:await store.coverage(c.tap)};
  if(c.kind==='native-dependencies')return {nativeDependencies:await store.dependencies(c.source)};
  if(c.kind==='native-rows'){reads++;return {nativePage:await store.records(c.source,c.from,c.to,c.rows)}};
  if(c.kind==='native-page')return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit)};
  return {};
 };
 const meta={cursor:'cursor',snapshot:'a'.repeat(64),summaryVersion:'a'.repeat(64),page:[{stream:0,row:3,key:'b'.repeat(64),cached:false,native:{from,to,generation}}],provisional:false,committedThrough:to};
 const text=new TextEncoder(),bind=frame([2,3,3],[le(0,4),Buffer.from(binding.digest,'hex'),text.encode(binding.tap)]);
 const viewWire=()=>bytes(magic,[2],bind,[8],frame([3],[text.encode(JSON.stringify(meta))]),[255,254]);
 let collects=0;
 const cache=new ViewCache(async url=>{
  if(url.startsWith('/collect?')){collects++;assert.equal(new URL(url,'https://test').searchParams.get('definition'),binding.digest);return new Response(wire,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Definition-Version':binding.digest,'Trunk-Format':'native-pac/0','Trunk-Completion':'body'}})}
  return new Response(viewWire(),{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Format':'pac-view/0','Trunk-Completion':'body'}});
 });
 const query={engine:'tantivy',tap:binding.tap,from:new Date(from).toISOString(),to:new Date(to).toISOString(),size:1,include:'page'};
 const view=await cache.query(query,'',new AbortController().signal,codec,definitions,archive);
 assert.equal(collects,1);assert.equal(view.native[0].row.length,6);
 const event=viewEvents(view)[0];assert.equal(valueAt(event,'revision_id'),3);assert.equal(event.millis,from+20*60000);
 const source=(await backend.list())[0];assert.deepEqual(committedRanges(source).index,[{Start:1,End:7}]);
 const original=records.find(r=>r.kind==='chunk'&&r.file==='index').bytes.subarray(2*6,3*6);
 assert.deepEqual(view.native[0].row,original,'client rewrote canonical index bytes');
 const clone=structuredClone(view);assert.equal(valueAt(viewEvents(clone)[0],'message'),'request completed');
 await cache.query(query,'',new AbortController().signal,codec,definitions,archive);assert.equal(collects,1,'already complete hour fetched again');
 const saved=new Map(),storage={get length(){return saved.size},key:i=>[...saved.keys()][i],getItem:k=>saved.get(k)??null,setItem:(k,v)=>saved.set(k,v),removeItem:k=>saved.delete(k)};
 new SearchSession(storage).append(query,query,'anonymous',view);
 const restored=new SearchSession(storage).restore(query,'anonymous');
 assert.deepEqual(restored.query,query);assert.equal(restored.events,undefined);assert.equal(restored.views,undefined);
 evicted=true;
 await cache.query(query,'',new AbortController().signal,codec,definitions,archive);
 assert.equal(collects,2,'evicted local hour was not rechecked');
 const conflict=new NativeCacheError('generation conflict',{code:'native-cache-conflict',source:binding.tap,reason:'generation-fork'});
 const failed=new ViewCache(async(url,init)=>{if(url.startsWith('/collect?')){collects++;throw conflict;}return new Response(viewWire(),{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Format':'pac-view/0','Trunk-Completion':'body'}})});
 await assert.rejects(failed.query(query,'',new AbortController().signal,codec,definitions,archive),error=>nativeCacheIssue(error)?.conflicts?.[0].source===conflict.issue.source);
 assert.equal(collects,3,'persistent conflict must not trigger a duplicate download');collects--;

 const originalPage=await store.page(source.key,from,to,undefined,100);
 const positions=[6,1,3];
 meta.page=positions.map((row,i)=>({...meta.page[0],row,key:String(i+1).repeat(64)}));
 const beforeReads=reads;
 const grouped=await cache.query({...query,size:3},'',new AbortController().signal,codec,definitions,archive);
 assert.equal(reads-beforeReads,1,'one storage command per hour, not per event');
 assert.equal(collects,2,'already collected hour was downloaded again');
 positions.forEach((row,i)=>assert.deepEqual(grouped.native[i].row,originalPage.rows.subarray((row-1)*6,row*6),'global query order changed'));
 let opens=0;const file=backend.file.bind(backend);
 backend.file=async(...args)=>{opens++;return file(...args)};
 const individual=[];
 for(const row of [1,3,6])individual.push(await store.page(source.key,from,to,row-1,1));
 assert.equal(opens,6);
 opens=0;
 const batch=await store.records(source.key,from,to,[1,3,6]);
 assert.equal(opens,2,'batch must open index and payload just once');
 individual.forEach((page,i)=>assert.deepEqual(batch.payload.subarray(batch.offsets[i*2],batch.offsets[i*2+1]),page.payload));
 await assert.rejects(store.records(source.key,from,to,[3,1]),/Invalid native record selection/);
 await assert.rejects(store.records(source.key,from,to,[1,1]),/Invalid native record selection/);
 await assert.rejects(store.records(source.key,from,to,[1,7]),/Missing canonical search record/);
 await assert.rejects(store.records(source.key,from,to,[-1]),/Invalid native record selection/);

});

test('sealed page overlaps at most four hour collections and batches rows without changing query order', {timeout:3000}, async()=>{
 const codec=new Codec(await WebAssembly.instantiate(wasm)),definitions=new Map(),bindings=[],records=[];
 const parser=new PILStream(codec,definitions,()=>{},b=>bindings.push(b),async()=>{},async r=>records.push(r));
 await parser.feed(wire);parser.finish();parser.close();
 const binding=bindings[0],manifest=records.find(r=>r.kind==='manifest').manifest;
 const generation=Buffer.from(manifest.Generation).toString('hex'),from=Date.UTC(2026,9,16,12);
 const refs=Array.from({length:12},(_,i)=>({stream:0,row:12-i,key:i.toString(16).padStart(64,'0'),cached:false,native:{from:from+(i%6)*3600000,to:from+(i%6+1)*3600000,generation}}));
 const meta={cursor:'cursor',snapshot:'a'.repeat(64),summaryVersion:'a'.repeat(64),page:refs,provisional:false,committedThrough:from+6*3600000};
 const bind=frame([2,3,3],[le(0,4),Buffer.from(binding.digest,'hex'),new TextEncoder().encode(binding.tap)]);
 const viewWire=bytes(magic,[2],bind,[8],frame([3],[new TextEncoder().encode(JSON.stringify(meta))]),[255,254]);
 let active=0,peak=0,collects=0,reads=0,release;
 const gate=new Promise(resolve=>{release=resolve});
 const cache=new ViewCache(async url=>{
  if(!url.startsWith('/collect?'))return new Response(viewWire,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Format':'pac-view/0','Trunk-Completion':'body'}});
  collects++;active++;peak=Math.max(peak,active);
  if(collects===4)release();
  await gate;active--;
  return new Response(wire,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Definition-Version':binding.digest,'Trunk-Format':'native-pac/0','Trunk-Completion':'body'}});
 });
 const archive=async(_,c)=>{
  if(c.kind==='native-coverage')return {nativeCoverage:[]};
  if(c.kind==='native-finish')return {nativeSources:['source']};
  if(c.kind==='native-dependencies')return {nativeDependencies:{source:{digest:binding.digest,generation},trie:[]}};
  if(c.kind==='native-rows'){
   reads++;assert.equal(c.rows.length,2);assert.ok(c.rows[0]<c.rows[1]);
   const rows=new Uint8Array(c.rows.length*6);c.rows.forEach((row,i)=>{rows[i*6]=row});
   return {nativePage:{rows,payload:Uint8Array.from(c.rows),offsets:Uint32Array.from(c.rows.flatMap((_,i)=>[i,i+1])),bases:new Float64Array(c.rows.length).fill(c.from),after:c.rows.at(-1),done:true}};
  }
  if(c.kind==='native-page')assert.fail('per-event read');
  return {};
 };
 const result=await cache.query({engine:'tantivy',tap:binding.tap,from:new Date(from).toISOString(),to:new Date(from+6*3600000).toISOString(),size:12,include:'page'},'',new AbortController().signal,codec,definitions,archive);
 assert.equal(peak,4);assert.equal(collects,6);assert.equal(reads,6);
 assert.deepEqual(result.native.map(record=>record.row[0]),refs.map(ref=>ref.row));
 assert.deepEqual([...new Uint8Array(result.batch.bytes)],refs.map(ref=>ref.row));
});

test('search discovers every conflicting resource beyond the four concurrent collectors',async()=>{
 const codec=new Codec(await WebAssembly.instantiate(wasm)),definitions=new Map(),bindings=[];
 const parser=new PILStream(codec,definitions,()=>{},b=>bindings.push(b),async()=>{},async()=>{});
 await parser.feed(wire);parser.finish();parser.close();
 const digest=bindings[0].digest,from=Date.UTC(2026,9,16,12),generation='a'.repeat(32);
 const taps=Array.from({length:6},(_,i)=>`demo/service${i}/resource.v1/write`);
 const refs=Array.from({length:12},(_,i)=>({stream:i%6,row:i+1,key:i.toString(16).padStart(64,'0'),cached:false,native:{from:from+Math.floor(i/6)*3600000,to:from+(Math.floor(i/6)+1)*3600000,generation}}));
 const meta={cursor:'cursor',snapshot:'a'.repeat(64),summaryVersion:'a'.repeat(64),page:refs,provisional:false,committedThrough:from+7200000};
 const packet=bytes(magic,...taps.map((tap,i)=>bytes([2],frame([2,3,3],[le(i,4),Buffer.from(digest,'hex'),new TextEncoder().encode(tap)]))),[8],frame([3],[new TextEncoder().encode(JSON.stringify(meta))]),[255,254]);
 const calls=[];
 const cache=new ViewCache(async url=>{
  if(!url.startsWith('/collect?'))return new Response(packet,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Format':'pac-view/0','Trunk-Completion':'body'}});
  const source=new URL(url,'https://test').searchParams.get('tap');calls.push(source);
  throw new NativeCacheError('conflict',{code:'native-cache-conflict',source,reason:'generation-fork'});
 });
 const archive=async()=>({});
 await assert.rejects(cache.query({engine:'tantivy',tap:'',from:new Date(from).toISOString(),to:new Date(from+7200000).toISOString(),size:12},'',new AbortController().signal,codec,definitions,archive),error=>{
  assert.ok(nativeCacheIssue(error),error.message);assert.deepEqual(nativeCacheIssue(error).conflicts.map(c=>c.source).sort(),taps);return true;
 });
 assert.deepEqual([...new Set(calls)].sort(),taps,'later resources must still be checked');
 assert.ok(calls.length<refs.length,'known failed resources must skip remaining hours; already in-flight hours can finish');
});
