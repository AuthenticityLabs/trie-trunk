import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {Codec} from './pil/wasm.ts';
import {ViewCache} from './pil/view.ts';
import {NativeStore} from './store/native.ts';
import {Backend} from './native-backend-fixture.mjs';
import {compile} from './pil/metadata.ts';
import {NativeEvent,NativeTrie} from './seeds/index.ts';
import {valueAt} from './dictionary.ts';
const load=name=>new Uint8Array(readFileSync(new URL('./fixtures/collect-live/'+name,import.meta.url)));
const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
async function fixture(promotion=false){
 const storeBytes=load('definition.rx'),digest=createHash('sha256').update(storeBytes).digest('hex'),metadata={digest,store:storeBytes,pil:storeBytes};
 const tap='demo/payment/transactions.v1/write',path='demo/payment/transactions.v1/0/write',from=Date.UTC(2026,9,30,12);
 const trunk={version:'collection-test',catalog:{files:['tri','pods'].map(kind=>({path:path+'.'+kind,kind,definition:digest,bootstrap:false,size:0}))},bindings:[{stream:0,tap,digest}],definitions:[metadata]};
 const codec=new Codec(await WebAssembly.instantiate(wasm)),definitions=new Map([[digest,{...metadata,definition:compile(metadata,tap).definition,handle:codec.define(storeBytes)}]]),store=new NativeStore(new Backend()),sessions=new Map(),requests=[],commands=[];
 let failFinish=false;
 const archive=async(_token,c)=>{
  commands.push(c);
  switch(c.kind){
   case 'time-read-many':return {times:await store.timeWindows(c.queries)};
   case 'time-save':await store.importTime(c.input);return {};
   case 'native-begin':sessions.set(c.session,store.begin(await store.serial()));return {};
   case 'native-coverage':return {nativeCoverage:await store.coverage(c.tap)};
   case 'native-records':for(const r of c.records)await store.record(sessions.get(c.session),r.record,r.definition);return {};
   case 'native-finish':{if(failFinish){failFinish=false;throw Error('fixture disk write failed')}const s=sessions.get(c.session);for(const r of c.records??[])await store.record(s,r.record,r.definition);await store.finish(s);sessions.delete(c.session);return {nativeSources:[]}};
   case 'native-abort':sessions.delete(c.session);return {};
   case 'native-resolve':return {nativeDependencies:await store.resolveHour(c)};
   case 'native-page':return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit,c.descending)};
   default:throw Error('Unexpected command '+c.kind);
  }
 };
 let step=0,cache=new ViewCache(),previous;
 const snapshots=Array.from({length:promotion?3:6},(_,i)=>load((promotion?'promotion-':'')+i+'.pil'));
 const end=promotion?[65*60000,80*60000,80*60000]:[600000,601000,720000,720000,720000,7200000];
 const fetcher=async(url,init)=>{
  if(url.startsWith('/collect?')){const request=new URL(url,'http://test');requests.push({from:Date.parse(request.searchParams.get('from')),to:request.searchParams.get('to'),method:init.method});return new Response(snapshots[promotion&&request.searchParams.get('to')!=='now'?2:step],{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Definition-Version':digest,'Trunk-Window-To':String(from+end[step]),'Trunk-Format':'native-pac/0'}})}
  throw Error('Unexpected request '+url);
 };
 const reset=()=>{cache=new ViewCache(fetcher);cache.setTrunk(trunk);
  // Deterministic metadata adapter; real IndexedDB lifecycle is covered by the
  // browser SearchesLRU suite. Event bytes never enter this adapter.
  cache.searches={available:true,start:async()=>({signature:'query',id:'run',previous}),finish:async(_run,r)=>{previous={status:r.status,checkpointEpochMs:r.progress?.checkpointEpochMs}}};
 };
 reset();
 const query={now:true,tap,from:new Date(from).toISOString(),to:new Date(from+end[0]).toISOString(),size:20};
 return {store,requests,commands,snapshots,query,reset,failNextFinish:()=>{failFinish=true},setStep:n=>step=n,search:async (patch,signal=new AbortController().signal)=>cache.query({...query,...patch},'',signal,codec,definitions,archive),events:view=>view.result.page.map((r,i)=>{const b=view.bindings.find(b=>b.stream===r.stream),m=compile(view.definitions.find(d=>d.digest===b.digest),b.tap),n=view.native[i],trie=new NativeTrie(m);for(const part of n.trie)trie.append(part.start,part.bytes);return new NativeEvent(n.row,new Uint8Array(view.batch.bytes).subarray(view.batch.rows[i*3+1],view.batch.rows[i*3+2]),trie,view.batch.common?.[i],n.base)})};
}
test('now refresh appends to the original window and replaces live rows only after sealed storage commits',async()=>{
 const f=await fixture();let view=await f.search();assert.equal(view.result.summary.count,2);assert.equal(view.result.summary.status,'live-snapshot');
 const first=view.result.searchProgress.checkpointEpochMs;
 assert.equal(f.commands.filter(c=>c.kind==='native-records').length,0,'live bytes never publish durable hours');
 f.setStep(1);view=await f.search();assert.equal(view.result.summary.count,2);assert.equal(f.requests.at(-1).from,first-1);assert.equal(f.requests.at(-1).to,'now');assert.equal(f.requests.at(-1).method,'GET');
 f.setStep(2);view=await f.search();assert.equal(view.result.summary.count,3);assert.equal(new Set(view.result.page.map(p=>p.key)).size,3);
 assert.deepEqual(f.events(view).map(e=>e.millis),[30,60,660].map(n=>Date.parse(f.query.from)+n*1000));
 f.setStep(5);view=await f.search();assert.equal(view.result.summary.count,4);assert.equal(f.events(view).length,4);assert.equal(new Set(view.result.page.map(p=>p.key)).size,4);
 assert.ok(f.commands.some(c=>c.kind==='native-finish'&&c.records?.length),'sealed bytes commit through the native path');
 assert.equal(f.requests.length,4);
});
test('live payloads support state predicates without a sealed state leaf',async()=>{
 const f=await fixture();f.setStep(4);const all=await f.search();const events=f.events(all),state=valueAt(events[0],'state');assert.ok(state!==undefined);
 const filtered=await f.search({query:{filters:[{field:'state',op:'==',value:{kind:'string',value:String(state)}}]}});
 assert.equal(filtered.result.summary.count,events.filter(e=>valueAt(e,'state')===state).length);
 const zero=await f.search({query:{filters:[{field:'duration',op:'<',value:{kind:'number',value:'0'}}]}});assert.equal(zero.result.summary.count,0);assert.ok(zero.result.searchProgress.checkpointEpochMs);
});
test('a saved checkpoint without ephemeral bytes restarts retrieval at the original window',async()=>{
 const f=await fixture();await f.search();f.reset();const again=await f.search();assert.equal(again.result.summary.count,2);assert.equal(f.requests.at(-1).from,Date.parse(f.query.from));
});
test('live pagination freezes the window and performs no refresh request',async()=>{
 const f=await fixture();f.setStep(4);let view=await f.search({size:1}),keys=[view.result.page[0].key];
 while(view.result.next){view=await f.search({size:1,after:view.result.next});keys.push(...view.result.page.map(p=>p.key))}
 assert.equal(keys.length,4);assert.equal(new Set(keys).size,4);assert.equal(f.requests.length,1);
});
test('aborted retrieval cannot advance the retained live window',async()=>{
 const f=await fixture();const initial=await f.search();f.setStep(2);
 await assert.rejects(f.search({},AbortSignal.abort()));
 f.setStep(1);const view=await f.search();assert.equal(view.result.summary.count,2);assert.ok(f.requests.at(-1).from<=initial.result.searchProgress.checkpointEpochMs-1);
});

test('a failed durable write preserves the earlier ephemeral snapshot',async()=>{
 const f=await fixture(),initial=await f.search({size:1});assert.ok(initial.result.next);
 f.setStep(5);f.failNextFinish();await assert.rejects(f.search({size:1}),/fixture disk write failed/);
 const requests=f.requests.length,remaining=await f.search({size:1,after:initial.result.next});
 assert.equal(remaining.result.summary.count,2);assert.equal(remaining.result.page.length,1);assert.equal(f.requests.length,requests);
});

test('sealing behind the checkpoint fetches the missing whole hour before dropping its live copy',async()=>{
 const f=await fixture(true),first=await f.search();assert.equal(first.result.summary.count,2);
 f.setStep(1);const next=await f.search();assert.equal(next.result.summary.count,2);assert.equal(next.result.page.length,2);
 assert.equal(f.requests.length,3);assert.equal(f.requests[1].from,first.result.searchProgress.checkpointEpochMs-1);
 assert.equal(f.requests[2].from,Date.parse(f.query.from));assert.equal(Date.parse(f.requests[2].to),Date.parse(f.query.from)+3600000);
 assert.deepEqual(f.events(next).map(e=>e.millis),[3590000,3660000].map(n=>Date.parse(f.query.from)+n));
});

test('changing the requested lower bound trims only records outside that window',async()=>{
 const f=await fixture();await f.search();f.setStep(1);
 const from=Date.parse(f.query.from)+45000,view=await f.search({from:new Date(from).toISOString()});
 assert.equal(view.result.summary.count,1);assert.equal(f.events(view)[0].millis,Date.parse(f.query.from)+60000);
 assert.equal(f.requests.at(-1).from,Date.parse(f.query.from)+59999);
});
