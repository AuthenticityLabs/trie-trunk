import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Codec} from './pil/wasm.ts';
import {PILStream} from './pil/stream.ts';
import {ViewCache} from './pil/view.ts';
import {viewEvents} from './pil/view-events.ts';
import {NativeStore} from './store/native.ts';
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
 let evicted=false;
 const archive=async(token,c)=>{
  if(c.kind==='native-page'&&evicted){evicted=false;throw Error('Native source evicted')}
  if(c.kind==='native-begin')sessions.set(c.session,store.begin(await store.serial()));
  if(c.kind==='native-record')await store.record(sessions.get(c.session),c.record,c.definition);
  if(c.kind==='native-finish'){const session=sessions.get(c.session);await store.finish(session);sessions.delete(c.session);return {nativeSources:[...new Set(session.packages.map(p=>p.key))]}}
  if(c.kind==='native-abort')sessions.delete(c.session);
  if(c.kind==='native-coverage')return {nativeCoverage:await store.coverage(c.tap)};
  if(c.kind==='native-dependencies')return {nativeDependencies:await store.dependencies(c.source)};
  if(c.kind==='native-page')return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit)};
  return {};
 };
 const meta={cursor:'cursor',snapshot:'a'.repeat(64),summaryVersion:'a'.repeat(64),page:[{stream:0,row:3,key:'b'.repeat(64),cached:false,native:{from,to,generation}}],provisional:false,committedThrough:to};
 const text=new TextEncoder(),bind=frame([2,3,3],[le(0,4),Buffer.from(binding.digest,'hex'),text.encode(binding.tap)]);
 const viewWire=bytes(magic,[2],bind,[8],frame([3],[text.encode(JSON.stringify(meta))]),[255,254]);
 let collects=0;
 const cache=new ViewCache(async url=>{
  if(url.startsWith('/collect?')){collects++;assert.equal(new URL(url,'https://test').searchParams.get('definition'),binding.digest);return new Response(wire,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Definition-Version':binding.digest,'Trunk-Format':'native-pac/0','Trunk-Completion':'body'}})}
  return new Response(viewWire,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Format':'pac-view/0','Trunk-Completion':'body'}});
 });
 const query={tap:binding.tap,from:new Date(from).toISOString(),to:new Date(to).toISOString(),size:1,include:'page'};
 const view=await cache.query(query,'',new AbortController().signal,codec,definitions,archive);
 assert.equal(collects,1);assert.equal(view.native[0].row.length,16);
 const event=viewEvents(view)[0];assert.equal(valueAt(event,'revision_id'),3);assert.equal(event.millis,from+20*60000);
 const source=(await backend.list())[0];assert.deepEqual(source.index,[{Start:1,End:7}]);
 const original=records.find(r=>r.kind==='chunk'&&r.file==='index').bytes.subarray(2*16,3*16);
 assert.deepEqual(view.native[0].row,original,'client rewrote canonical index bytes');
 const clone=structuredClone(view);assert.equal(valueAt(viewEvents(clone)[0],'message'),'request completed');
 await cache.query(query,'',new AbortController().signal,codec,definitions,archive);assert.equal(collects,1,'already complete hour fetched again');
 const saved=new Map(),storage={get length(){return saved.size},key:i=>[...saved.keys()][i],getItem:k=>saved.get(k)??null,setItem:(k,v)=>saved.set(k,v),removeItem:k=>saved.delete(k)};
 new SearchSession(storage).append(query,query,'anonymous',view);
 const restored=new SearchSession(storage).restore(query,'anonymous');
 assert.equal(valueAt(restored.events[0],'revision_id'),3);assert.equal(restored.events[0].uuid,event.uuid);
 assert.deepEqual(restored.views[0].native[0].row,original);
 evicted=true;
 await cache.query(query,'',new AbortController().signal,codec,definitions,archive);
 assert.equal(collects,2,'evicted local hour was not rechecked');

});
