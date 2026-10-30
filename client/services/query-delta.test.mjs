import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {Codec} from './pil/wasm.ts';
import {ViewCache} from './pil/view.ts';
import {NativeStore} from './store/native.ts';
import {Backend} from './native-backend-fixture.mjs';
import {encodeObject,decodeObject} from './pil/control.ts';
import {compile} from './pil/metadata.ts';
import {NativeEvent,NativeTrie} from './seeds/index.ts';
import {valueAt} from './dictionary.ts';
const load=name=>new Uint8Array(readFileSync(new URL('./fixtures/query-delta/'+name,import.meta.url)));
const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
async function fixture(){
 const storeBytes=load('definition.rx'),digest=createHash('sha256').update(storeBytes).digest('hex'),metadata={digest,store:storeBytes,pil:storeBytes};
 const tap='demo/payment/transactions.v1/write',path='demo/payment/transactions.v1/0/write',from=Date.UTC(2026,9,30,12);
 const trunk={version:'delta-test',catalog:{files:['tri','pods'].map(kind=>({path:path+'.'+kind,kind,definition:digest,bootstrap:false,size:0}))},bindings:[{stream:0,tap,digest}],definitions:[metadata]};
 const codec=new Codec(await WebAssembly.instantiate(wasm)),definitions=new Map(),store=new NativeStore(new Backend()),sessions=new Map(),requests=[],commands=[];
 const archive=async(_token,c)=>{
  commands.push(c);
  switch(c.kind){
   case 'time-read-many':return {times:await store.timeWindows(c.queries)};
   case 'time-save':await store.importTime(c.input);return {};
   case 'native-begin':sessions.set(c.session,store.begin(await store.serial()));return {};
   case 'native-coverage':return {nativeCoverage:await store.coverage(c.tap)};
   case 'native-records':for(const r of c.records)await store.record(sessions.get(c.session),r.record,r.definition);return {};
   case 'native-finish':{const s=sessions.get(c.session);for(const r of c.records??[])await store.record(s,r.record,r.definition);await store.finish(s);sessions.delete(c.session);return {nativeSources:[]}};
   case 'native-abort':sessions.delete(c.session);return {};
   case 'native-resolve':return {nativeDependencies:await store.resolveHour(c)};
   case 'native-page':return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit,c.descending)};
   default:throw Error('Unexpected command '+c.kind);
  }
 };
 let step=0,cache=new ViewCache(),previous;
 const snapshots=Array.from({length:6},(_,i)=>decodeObject(load(i+'.pil')));
 const fetcher=async(url,init)=>{
  if(url.startsWith('/query-delta?')){const request=decodeObject(new Uint8Array(init.body));requests.push(request);return new Response(encodeObject(snapshots[step]),{headers:{'Content-Type':'application/pil','Trunk-Format':'query-delta/0'}})}
  if(url.startsWith('/'+path+'.pods?'))return new Response(load('sealed.pil'),{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Definition-Version':digest,'Trunk-Path':path+'.pods','Trunk-Format':'native-pac/0'}});
  throw Error('Unexpected request '+url);
 };
 const reset=()=>{cache=new ViewCache(fetcher);cache.setTrunk(trunk);
  // Deterministic metadata adapter; real IndexedDB lifecycle is covered by the
  // browser SearchesLRU suite. Event bytes never enter this adapter.
  cache.searches={available:true,start:async()=>({signature:'query',id:'run',previous}),finish:async(_run,r)=>{previous={status:r.status,checkpointEpochMs:r.progress?.checkpointEpochMs}}};
 };
 reset();
 const query={now:true,tap,from:new Date(from).toISOString(),to:new Date(snapshots[0].To).toISOString(),size:20};
 return {store,requests,commands,snapshots,query,reset,setStep:n=>step=n,search:async (patch,signal=new AbortController().signal)=>cache.query({...query,...patch},'',signal,codec,definitions,archive),events:view=>view.result.page.map((r,i)=>{const b=view.bindings.find(b=>b.stream===r.stream),m=compile(view.definitions.find(d=>d.digest===b.digest),b.tap),n=view.native[i],trie=new NativeTrie(m);for(const part of n.trie)trie.append(part.start,part.bytes);return new NativeEvent(n.row,new Uint8Array(view.batch.bytes).subarray(view.batch.rows[i*3+1],view.batch.rows[i*3+2]),trie,view.batch.common?.[i],n.base)})};
}
test('now refresh uses one query checkpoint, merges overlap/late data, and replaces live rows at sealing',async()=>{
 const f=await fixture();let view=await f.search();assert.equal(view.result.summary.count,1);assert.equal(view.result.summary.status,'live-snapshot');
 const first=view.result.searchProgress.checkpointEpochMs;
 assert.equal(f.commands.filter(c=>c.kind==='native-begin').length,0,'live bytes must not mark durable hours');
 f.setStep(1);view=await f.search();assert.equal(view.result.summary.count,1);assert.equal(f.requests.at(-1).Checkpoint,first);assert.equal(f.requests.at(-1).Known.length,1);
 f.setStep(2);view=await f.search();assert.equal(view.result.summary.count,2);assert.equal(new Set(view.result.page.map(p=>p.key)).size,2);
 f.setStep(3);view=await f.search();assert.equal(view.result.summary.count,3);assert.deepEqual(f.events(view).map(e=>e.millis),[30,60,660].map(n=>Date.parse(f.query.from)+n*1000));
 f.setStep(5);view=await f.search();assert.equal(view.result.summary.count,3);assert.equal(f.events(view).length,3);assert.equal(new Set(view.result.page.map(p=>p.key)).size,3);
 assert.ok(f.commands.some(c=>c.kind==='native-begin'),'sealed bytes use the existing native path');
 assert.equal(f.requests.length,5);
});
test('live payloads support state and Boolean predicates without a sealed state leaf',async()=>{
 const f=await fixture();f.setStep(3);const all=await f.search();const events=f.events(all),state=valueAt(events[0],'state');assert.ok(state!==undefined);
 const filtered=await f.search({query:{filters:[{field:'state',op:'==',value:{kind:'string',value:String(state)}}]}});
 assert.equal(filtered.result.summary.count,events.filter(e=>valueAt(e,'state')===state).length);
 const zero=await f.search({query:{filters:[{field:'duration',op:'<',value:{kind:'number',value:'0'}}]}});assert.equal(zero.result.summary.count,0);assert.ok(zero.result.searchProgress.checkpointEpochMs);
});
test('a saved checkpoint without retained live bytes cannot skip the live snapshot after reload',async()=>{
 const f=await fixture();await f.search();f.reset();f.setStep(0);const again=await f.search();assert.equal(again.result.summary.count,1);assert.equal(f.requests.at(-1).Checkpoint,undefined);assert.deepEqual(f.requests.at(-1).Known,[]);
});
test('live pagination freezes the same snapshot and performs no refresh request',async()=>{
 const f=await fixture();f.setStep(3);let view=await f.search({size:1}),keys=[view.result.page[0].key];
 while(view.result.next){view=await f.search({size:1,after:view.result.next});keys.push(...view.result.page.map(p=>p.key))}
 assert.equal(keys.length,3);assert.equal(new Set(keys).size,3);assert.equal(f.requests.length,1);
});

test('aborted retrieval cannot advance the retained live receipt',async()=>{
 const f=await fixture();await f.search();f.setStep(2);
 await assert.rejects(f.search({},AbortSignal.abort()));
 f.setStep(1);const view=await f.search();assert.equal(view.result.summary.count,1);
 assert.equal(f.requests.at(-1).Known[0].Revision,f.snapshots[0].Branches[0].Receipt.Revision);
});
test('sliding the lower bound removes old live rows while retaining the common checkpoint',async()=>{
 const f=await fixture();f.setStep(3);await f.search();
 const from=Date.parse(f.query.from)+120000;
 f.snapshots[3].Branches[0].ReplaceFrom=from;f.snapshots[3].Branches[0].Frames=f.snapshots[3].Branches[0].Frames.slice(-1);
 const view=await f.search({from:new Date(from).toISOString()});
 assert.equal(view.result.summary.count,1);assert.ok(f.requests.at(-1).Checkpoint);assert.equal(f.requests.at(-1).Known.length,1);
});
