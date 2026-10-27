import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { Codec } from './pil/wasm.ts';
import { PILStream } from './pil/stream.ts';
import { ViewCache } from './view-archive-fixture.mjs';
import { pacHeader } from './pil/pac.ts';
import { workerService } from './pil/worker-runtime.ts';
import { fixture,bytes,magic,frame,le,extended,response } from './pil-fixtures.mjs';
const module=WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
const cold=readFileSync(new URL('./fixtures/pac-view/cold.pil',import.meta.url));
const warm=readFileSync(new URL('./fixtures/pac-view/warm.pil',import.meta.url));
const query={tap:'demo/payment/transactions.v1/write',from:'2026-09-01T12:00:00Z',to:'2026-09-01T14:00:00Z',size:2};
function reply(raw,stride=65536){const r=response(raw,'a'.repeat(64),stride);r.headers.set('Trunk-Format','pac-view/0');return r}
const fresh=async()=>new Codec(await WebAssembly.instantiate(await module));
test('Go PAC view round trip, one GET and exact cached dependencies/page omission',async()=>{
 const calls=[],codec=await fresh(),definitions=new Map();
 const cache=new ViewCache(async(url,opts)=>{calls.push({url,opts});return reply(calls.length===1?cold:warm,1)});
 const first=await cache.query(query,'secret',new AbortController().signal,codec,definitions);
 assert.equal(first.result.summary.count,7);assert.equal(first.batch.rows.length,6);assert.equal(first.definitions.length,1);
 const second=await cache.query(query,'secret',new AbortController().signal,codec,definitions);
 assert.deepEqual(second.batch,first.batch);assert.deepEqual(second.result.summary,first.result.summary);
 assert.ok(second.result.page.every(r=>r.cached));
 assert.equal(calls.length,2);assert.match(calls[0].url,/^\/events\?/);assert.equal(calls[0].opts.headers['Definition-Known'],'');
 assert.equal(calls[1].opts.headers['Definition-Known'],first.definitions[0].digest);
 assert.equal(calls[1].opts.headers['Summary-Known'],first.result.snapshot);
 assert.equal(calls[1].opts.headers['Event-Known'],first.result.page.map(r=>r.key).join(','));
});
test('truncated responses do not publish cached pages or summaries',async()=>{
 const calls=[],codec=await fresh(),defs=new Map();
 const cache=new ViewCache(async(url,opts)=>{calls.push(opts.headers);return reply(calls.length===1?cold.subarray(0,-1):cold)});
 await assert.rejects(cache.query(query,'',new AbortController().signal,codec,defs),/complet|Truncated/i);
 await cache.query(query,'',new AbortController().signal,codec,defs);
 assert.equal(calls[1]['Event-Known'],'');assert.equal(calls[1]['Summary-Known'],undefined);
});
test('PAC validates version, bounds, required extensions, prefix and member boundaries',async()=>{
 const packed=new Uint8Array(readFileSync(new URL('../../../pill-packer/rust/tests/fixtures/pac/packed.pac',import.meta.url)));
 const parsed=pacHeader(packed,0);assert.equal(parsed.count,16);assert.ok(parsed.common.length);assert.equal(parsed.end,packed.length);
 const bad=packed.slice();bad[3]=1;assert.throws(()=>pacHeader(bad,0),/bounds/);
 const badCount=packed.slice();new DataView(badCount.buffer).setBigUint64(24,9999999n,true);assert.throws(()=>pacHeader(badCount,0),/count/);
 const short=packed.subarray(0,-1);assert.equal(pacHeader(short,0),undefined);
 const codec=await fresh(),p=new PILStream(codec,new Map(),()=>{},()=>{},async()=>{},undefined,()=>{});
 try{await assert.rejects(p.feed(bytes(magic,[7],packed,[255,254])),/Unbound PAC/)}finally{p.close()}
 const unnegotiated=new PILStream(codec,new Map(),()=>{},()=>{},async()=>{});
 try{await assert.rejects(unnegotiated.feed(cold),/negotiated/)}finally{unnegotiated.close()}
});
test('worker view uses same-call negotiation and remembers definitions without range coverage',async t=>{
 const stored=[],urls=[];
 const connect=workerService(module,async(url)=>{urls.push(url);return reply(cold)},async(token,cmd)=>{stored.push(cmd);return {definitions:[]}});
 const channel=new MessageChannel();connect(channel.port2);t.after(()=>{channel.port1.postMessage({kind:'close'});channel.port1.close()});
 const messages=await new Promise((resolve,reject)=>{const list=[];channel.port1.onmessage=({data})=>{list.push(data);if(data.kind==='done')resolve(list);if(data.kind==='error')reject(Error(data.message))};channel.port1.postMessage({id:1,kind:'view',token:'secret',query})});
 assert.equal(urls.length,1);assert.match(urls[0],/^\/events/);
 assert.deepEqual(stored.map(c=>c.kind),['definitions']);
 const view=messages.find(m=>m.kind==='view').view;
 assert.equal(view.batch.rows.length,6);
 await new Promise((resolve,reject)=>{channel.port1.onmessage=({data})=>{if(data.kind==='done')resolve();if(data.kind==='error')reject(Error(data.message))};channel.port1.postMessage({id:2,kind:'persist-view',token:'secret',definitions:view.definitions,bindings:view.bindings})});
 assert.deepEqual(stored.map(c=>c.kind),['definitions','remember']);
});
test('continuation reuses summary but advertises only relevant page frames',async()=>{
 const next=readFileSync(new URL('./fixtures/pac-view/next.pil',import.meta.url));
 const calls=[],cache=new ViewCache(async(url,opts)=>{calls.push(opts.headers);return reply(calls.length===1?cold:next)});
 const codec=await fresh(),defs=new Map(),signal=new AbortController().signal;
 const a=await cache.query(query,'',signal,codec,defs);
 const b=await cache.query({...query,after:a.result.next},'',signal,codec,defs);
 assert.equal(b.result.summary.count,7);assert.equal(b.batch.rows.length,6);assert.equal(calls[1]['Event-Known'],'');
});

test('full-range statistics validate independently of page length and retain zero durations',async()=>{
 const {validateStatistics}=await import('./pil/view.ts');
 const statistics={services:[{name:'payment',count:1000}],levels:[{name:'ERROR',count:10},{name:'INFO',count:990}],duration:{count:800,average:25,min:0,max:500},operations:{errors:[{service:'payment',resource:'transactions.v1',operation:'authorize',value:10,samples:10}],latency:[{service:'payment',resource:'transactions.v1',operation:'authorize',value:100,samples:800}]}};
 assert.doesNotThrow(()=>validateStatistics({count:1000,statistics}));
 for(const change of [s=>s.services[0].count=100,s=>s.duration.average=Infinity,s=>s.duration.min=100,s=>s.operations.latency[0].samples=801,s=>s.operations.errors[0].value=9]){
  const copy=structuredClone(statistics);change(copy);assert.throws(()=>validateStatistics({count:1000,statistics:copy}),/Invalid full-range/);
 }
});

test('pages certify a full semantic bucket and reuse it across queries and worker restoration',async()=>{
 const raw=[0,1,2].map(i=>readFileSync(new URL(`./fixtures/pac-buckets/page-${i}.pil`,import.meta.url)));
 const warm=readFileSync(new URL('./fixtures/pac-buckets/warm.pil',import.meta.url));
 const calls=[],saved=[];let at=0;
 const cache=new ViewCache(async(url,opts)=>{calls.push(opts.headers);return reply(at<3?raw[at++]:warm)});
 const codec=await fresh(),definitions=new Map(),signal=new AbortController().signal;
 const q={...query,to:'2026-09-01T13:00:00Z'};
 let after;
 for(let i=0;i<3;i++){
  const v=await cache.query({...q,after},'',signal,codec,definitions);saved.push(v);after=v.result.next;
  assert.equal(v.result.coverage.complete,i===2?1:0);
  assert.equal(calls[i]['Bucket-Known'],undefined,'partial pages cannot certify a bucket');
 }
 const reused=await cache.query({...q,from:'2026-09-01T12:00:01Z'},'',signal,codec,definitions);
 assert.equal(calls[3]['Bucket-Known'],saved[0].result.buckets[0].key);
 assert.equal(calls[3]['Event-Known'],'','reuse must come from bucket receipt, not identical page key');
 assert.deepEqual(reused.batch,saved[0].batch);assert.ok(reused.result.page.every(r=>r.cached));
 const restored=new ViewCache(async(url,opts)=>{assert.equal(opts.headers['Bucket-Known'],saved[0].result.buckets[0].key);return reply(warm)});
 saved.forEach(v=>restored.remember(v));
 assert.deepEqual((await restored.query(q,'',signal,codec,definitions)).batch,saved[0].batch);
});

test('a truncated final page cannot certify complete bucket coverage',async()=>{
 const raw=[0,1,2].map(i=>readFileSync(new URL(`./fixtures/pac-buckets/page-${i}.pil`,import.meta.url)));
 let at=0;const calls=[];
 const cache=new ViewCache(async(url,opts)=>{calls.push(opts.headers);return reply(at<2?raw[at++]:at++===2?raw[2].subarray(0,-1):raw[2])});
 const codec=await fresh(),defs=new Map(),signal=new AbortController().signal;
 const a=await cache.query(query,'',signal,codec,defs);
 const b=await cache.query({...query,after:a.result.next},'',signal,codec,defs);
 await assert.rejects(cache.query({...query,after:b.result.next},'',signal,codec,defs),/complet|Truncated/i);
 const completed=await cache.query({...query,after:b.result.next},'',signal,codec,defs);
 assert.equal(calls[3]['Bucket-Known'],undefined);assert.equal(completed.result.coverage.complete,1);
});

test('Go live pages include pending events without certifying their disk rows',async()=>{
 const pages=['first','next'].map(name=>readFileSync(new URL('./fixtures/pac-live/'+name+'.pil',import.meta.url)));
 let n=0;const calls=[],cache=new ViewCache(async(url,opts)=>{calls.push(opts.headers);return reply(pages[n++],1)});
 const codec=await fresh(),defs=new Map(),signal=new AbortController().signal;
 const q={...query,from:'2026-10-18T10:00:00Z',to:'2026-10-18T14:00:00Z'};
 const a=await cache.query(q,'',signal,codec,defs);
 assert.equal(a.result.summary.status,'live-snapshot');
 assert.equal(a.result.summary.count,3);
 assert.deepEqual(a.result.page.map(r=>!!r.provisional),[false,true]);
 const b=await cache.query({...q,after:a.result.next},'',signal,codec,defs);
 assert.equal(b.result.summary.count,3);
 assert.equal(b.result.page.length,1);
 assert.equal(b.result.page[0].provisional,true);
 assert.equal(b.result.next,undefined);
 assert.equal(b.result.coverage.complete,1,'only the earlier sealed hour is complete');
});

test('prefix views survive cache and session restore without expanding members',async()=>{
 const {viewEvents}=await import('./pil/view-events.ts');
 const codec=await fresh(),defs=new Map();const cache=new ViewCache(async()=>reply(cold,1));
 const view=await cache.query(query,'',new AbortController().signal,codec,defs);
 assert.ok(view.batch.common.some(p=>p?.length));
 const rows=viewEvents(view);assert.equal(rows.length,2);
 for(const event of rows){assert.ok(event.prefix()?.length);assert.ok(event.orderedKeys().length>=7);for(const key of event.orderedKeys())event.read(key)}
 const cloned=structuredClone(view);assert.deepEqual(viewEvents(cloned).map(e=>e.orderedKeys().map(k=>e.read(k))),rows.map(e=>e.orderedKeys().map(k=>e.read(k))));
});

test('native placement preserves source rows across pages without padding or record hydration',async()=>{
 const {pacPlacement,pacLocalRows,nativePAC}=await import('./pil/pac-native.ts');
 const raw=readFileSync(new URL('../../../pill-packer/rust/tests/fixtures/pac/native.pac',import.meta.url));
 const source=readFileSync(new URL('../../../pill-packer/rust/tests/fixtures/pac/native.idx',import.meta.url));
 const expected=readFileSync(new URL('../../../pill-packer/rust/tests/fixtures/pac/local.idx',import.meta.url));
 assert.throws(()=>pacHeader(raw,0),/Unsupported/);
 const header=pacHeader(raw,0,64<<20,new Set([nativePAC]));
 const place=pacPlacement(header);
 assert.equal(place.memberStart,119n);assert.equal(place.pageSize,64);assert.equal(place.firstRow,77n);
 const codec=await fresh(),handle=codec.define(readFileSync(new URL('../../../pill-packer/rust/tests/fixtures/pac/definition.cbor',import.meta.url)));
 codec.upload(raw);
 assert.throws(()=>codec.pac(handle,0,raw.length,header.count),/unsupported/);
 const bounds=codec.pac(handle,0,raw.length,header.count,true);
 const lengths=Uint32Array.from({length:header.count},(_,i)=>bounds[i*2+1]-bounds[i*2]);
 codec.release(handle);
 assert.deepEqual(Buffer.from(pacLocalRows(place,source,lengths)),expected);
 const local=new DataView(expected.buffer,expected.byteOffset,expected.byteLength);
 assert.equal(local.getUint32(12,true),0,'member starts are segment-relative on both ends');
 assert.throws(()=>pacLocalRows({...place,memberBytes:place.memberBytes+1n},source,lengths),/extent/);
 const bad=lengths.slice();bad[0]++;assert.throws(()=>pacLocalRows(place,source,bad),/member start/);
});

test('sealed-hour promotions serialize across tabs and retry failed persistence',async()=>{
 const raw=[0,1,2].map(i=>readFileSync(new URL(`./fixtures/pac-buckets/page-${i}.pil`,import.meta.url)));
 let at=0;const cache=new ViewCache(async()=>reply(raw[at++]));
 const codec=await fresh(),defs=new Map(),signal=new AbortController().signal;
 let after;
 for(let n=0;n<3;n++){const view=await cache.query({...query,after},'',signal,codec,defs);after=view.result.next;}
 assert.equal(cache.completeHours().length,1);
 await assert.rejects(cache.persistHours(async()=>{throw Error('quota');}),/quota/);
 assert.equal(cache.completeHours().length,1,'failed save cannot certify persistence');
 let release;const barrier=new Promise(resolve=>release=resolve);let saves=0;
 const first=cache.persistHours(async()=>{saves++;await barrier;});
 const second=cache.persistHours(async()=>{saves++;});
 await new Promise(resolve=>setImmediate(resolve));assert.equal(saves,1);
 release();await Promise.all([first,second]);
 assert.equal(saves,1);assert.equal(cache.completeHours().length,0);
});

test('query predicates isolate summary/page caches and are passed intact to the server',async()=>{
 const calls=[],codec=await fresh(),defs=new Map(),signal=new AbortController().signal;
 const cache=new ViewCache(async(url,opts)=>{calls.push({url,headers:opts.headers});return reply(cold)});
 const plan={filters:[{field:'duration',op:'>=',value:{kind:'number',value:'18446744073709551615'}}],order:'desc'};
 await cache.query(query,'',signal,codec,defs);
 await cache.query({...query,query:plan},'',signal,codec,defs);
 const url=new URL(calls[1].url,'http://localhost');assert.deepEqual(JSON.parse(url.searchParams.get('query')),plan);
 assert.equal(calls[1].headers['Summary-Known'],undefined);assert.equal(calls[1].headers['Event-Known'],'');assert.equal(calls[1].headers['Bucket-Known'],undefined);
});

test('progressive worker transfers event buffers once and delivers summaries as metadata',async t=>{
 const connect=workerService(module,async()=>reply(cold),async()=>({definitions:[]}));
 const channel=new MessageChannel();connect(channel.port2);
 t.after(()=>{channel.port1.postMessage({kind:'close'});channel.port1.close()});
 const messages=await new Promise((resolve,reject)=>{
  const list=[];channel.port1.onmessage=({data})=>{list.push(data);if(data.kind==='done')resolve(list);if(data.kind==='error')reject(Error(data.message))};
  channel.port1.postMessage({id:1,kind:'view',token:'',query,progressive:true});
 });
 assert.deepEqual(messages.map(m=>m.kind),['view-page','view-summary','done']);
 assert.equal(messages[0].view.batch.rows.length,6);
 assert.ok(messages[0].view.batch.bytes.byteLength>0);
 assert.equal(messages[0].view.result.summary,undefined);
 assert.equal(messages[1].result.snapshot,messages[0].view.result.snapshot);
 assert.equal(messages[1].result.summary.count,7);
 assert.equal(messages[1].view,undefined);
});
