import { summaryResponse } from './summary-pack-fixture.mjs';
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {decodeSummaryDay,mergeDailyAnalytics,SummaryDayCache} from './pil/summaries.ts';
import {validateStatistics,ViewCache} from './pil/view.ts';
import {Codec} from './pil/wasm.ts';
const root=new URL('./fixtures/daily-summary/',import.meta.url);
const fixture=kind=>JSON.parse(readFileSync(new URL(`${kind}/manifest.json`,root)));
const bytes=(kind,ref)=>new Uint8Array(readFileSync(new URL(`${kind}/${ref.key}.sumday`,root)));
for(const kind of ['aligned','border']){
 test(`daily columns render ${kind} counts, errors and exact p95`,async()=>{
  const {result,baseline}=fixture(kind),days=await Promise.all(result.analytics.days.map(r=>decodeSummaryDay(bytes(kind,r))));
  const merged=mergeDailyAnalytics(result.summary,result.analytics,days);
  validateStatistics(merged);assert.equal(merged.count,baseline.count);assert.equal(merged.count,kind==='aligned'?864:861);assert.equal(merged.counts.reduce((a,b)=>a+b),merged.count);
  assert.equal(merged.errors.reduce((a,b)=>a+b),kind==='aligned'?288:287);
  assert.equal(merged.statistics.complete,true);assert.equal(merged.statistics.duration.count,576);assert.equal(merged.statistics.duration.min,12);assert.equal(merged.statistics.duration.max,298);
  const values=Array.from({length:288},(_,i)=>i).filter(i=>i%3!==0).map(i=>i+11);
  assert.ok(Math.abs(merged.statistics.duration.average-values.reduce((a,b)=>a+b)/values.length)<1e-10);
  assert.equal(merged.statistics.operations.latency[0].value,values[Math.ceil(values.length*.95)-1]);
  assert.equal(merged.statistics.operations.errors[0].value,kind==='aligned'?288:287);
  assert.equal(merged.statistics.services[0].count,merged.count);
  assert.throws(()=>mergeDailyAnalytics(result.summary,{...result.analytics,days:[...result.analytics.days,result.analytics.days[0]]},[...days,days[0]]),/Duplicate/);
 });
}
test('daily decoder rejects corrupt, short and over-expanding packets',async()=>{
 const ref=fixture('aligned').result.analytics.days[0],raw=bytes('aligned',ref);
 const decoded=await decodeSummaryDay(raw),compressed=new DataView(raw.buffer,raw.byteOffset,raw.byteLength).getUint32(4,true)===2;
 for(const at of [0,8,12,16,20,24,32,raw.length-1]){
  const bad=raw.slice();bad[at]^=255;
  // Gzip MTIME and XFL are header metadata, outside the decoded payload CRC.
  if(compressed&&(at===20||at===24))assert.deepEqual(await decodeSummaryDay(bad),decoded);
  else await assert.rejects(decodeSummaryDay(bad));
 }
 await assert.rejects(decodeSummaryDay(raw.subarray(0,-1)));
 const bad=raw.slice();new DataView(bad.buffer).setUint32(8,128*1024*1024+1,true);await assert.rejects(decodeSummaryDay(bad));
});
test('daily cache reuses content, partitions authorization and checks manifest',async()=>{
 const ref=fixture('aligned').result.analytics.days[0],raw=bytes('aligned',ref),cache=new SummaryDayCache();let calls=0;
 const fetcher=async()=>{calls++;return summaryResponse([raw])};const signal=new AbortController().signal;
 const a=await cache.load(ref,'a',fetcher,signal);assert.equal(await cache.load(ref,'a',fetcher,signal),a);assert.equal(calls,1);
 await cache.load(ref,'b',fetcher,signal);assert.equal(calls,2);
 await assert.rejects(cache.load({...ref,day:ref.day+86400000},'a',fetcher,signal),/manifest/);
 const broken=raw.slice();broken[40]^=1;await assert.rejects(new SummaryDayCache().load(ref,'',async()=>summaryResponse([broken]),signal),/identity/);
});
test('Snoot summary-only request fetches daily summaries and no event PACs',async()=>{
 const {result}=fixture('aligned'),calls=[],raw=readFileSync(new URL('aligned/view.pil',root));
 const fetcher=async(url,options)=>{calls.push(url);if(url.startsWith('/demo/indexes?')){assert.match(options.headers['PIL-Features'],/daily-summaries/);return new Response(raw,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Trunk-Format':'pac-view/0'}})}
  assert.equal(url,'/demo/summaries');assert.equal(options.method,'POST');return summaryResponse(JSON.parse(options.body).packs.map(ref=>bytes('aligned',ref)));};
 const codec=new Codec((await WebAssembly.instantiate(readFileSync(new URL('../assets/pil.wasm',import.meta.url)))).instance);
 const cache=new ViewCache(fetcher),definitions=new Map(),query={engine:'tantivy',tap:'demo/payment/transactions.v1/*',from:new Date(result.summary.from).toISOString(),to:new Date(result.summary.to).toISOString(),include:'summary',size:50};
 const first=await cache.query(query,'secret',new AbortController().signal,codec,definitions);
 assert.equal(first.result.summary.count,864);assert.equal(first.batch.rows.length,0);assert.ok(calls.every(u=>!u.startsWith('/collect')));
 const count=calls.length;await cache.query(query,'secret',new AbortController().signal,codec,definitions);assert.equal(calls.length,count+1);
});

test('daily count columns cover a whole hour in one bucket and widen past U24',async()=>{
 for(const count of [255,256,65535,65536,16777215,16777216]){
  const raw=new Uint8Array(readFileSync(new URL(`counts/${count}.sumday`,root)));
  const day=await decodeSummaryDay(raw);
  assert.equal(day.rollup[0],BigInt(count));assert.equal(day.rollup[1],BigInt(count));
  assert.equal(day.rollup[2],BigInt(count)*50000n);
  assert.equal(day.hours[0].encoding[0].width,count<=255?1:count<=65535?2:count<=16777215?3:4);
  assert.equal(day.hours[0].encoding[7].base,50000n);
  assert.equal(day.hours[0].encoding[7].width,1);
  assert.deepEqual((await decodeSummaryDay(raw)).rollup,day.rollup,'decoding must not mutate the supplied packet');
 }
});
test('background summary priming reports unavailable persistent storage',async()=>{
 const ref=fixture('aligned').result.analytics.days.find(r=>r.sealed);
 assert.ok(ref);
 await assert.rejects(new SummaryDayCache().load(ref,'',async()=>summaryResponse([bytes('aligned',ref)]),new AbortController().signal,true),/storage is unavailable/);
});

test('progressive search publishes its page before any summary fetch, then the same snapshot totals',async()=>{
 const {result}=fixture('aligned'),raw=readFileSync(new URL('aligned/view.pil',root));
 let published,dayFetches=0;
 const fetcher=async (url,options)=>{
  if(url.startsWith('/demo/indexes?'))return new Response(raw,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Trunk-Format':'pac-view/0'}});
  assert.ok(published,'summary network must not delay page delivery');dayFetches++;
  return summaryResponse(JSON.parse(options.body).packs.map(ref=>bytes('aligned',ref)));
 };
 const codec=new Codec((await WebAssembly.instantiate(readFileSync(new URL('../assets/pil.wasm',import.meta.url)))).instance);
 const cache=new ViewCache(fetcher),query={engine:'tantivy',tap:'demo/payment/transactions.v1/*',from:new Date(result.summary.from).toISOString(),to:new Date(result.summary.to).toISOString(),include:'summary',size:50};
 const complete=await cache.query(query,'',new AbortController().signal,codec,new Map(),undefined,false,undefined,page=>{
  assert.equal(dayFetches,0);assert.equal(page.result.summary,undefined);
  published=structuredClone(page,{transfer:[page.batch.bytes,page.batch.rows.buffer]});
 });
 assert.equal(dayFetches,1);assert.equal(complete.result.snapshot,published.result.snapshot);
 assert.equal(complete.result.summary.count,864);assert.equal(published.timings.summaries,0);
});

test('failed chart retrieval leaves the delivered page intact and remains retryable',async()=>{
 const {result}=fixture('aligned'),raw=readFileSync(new URL('aligned/view.pil',root));let published,broken=true;
 const fetcher=async (url,options)=>{
  if(url.startsWith('/demo/indexes?'))return new Response(raw,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Trunk-Format':'pac-view/0'}});
  if(broken)throw Error('offline');
  return summaryResponse(JSON.parse(options.body).packs.map(ref=>bytes('aligned',ref)));
 };
 const codec=new Codec((await WebAssembly.instantiate(readFileSync(new URL('../assets/pil.wasm',import.meta.url)))).instance);
 const cache=new ViewCache(fetcher),query={engine:'tantivy',tap:'demo/payment/transactions.v1/*',from:new Date(result.summary.from).toISOString(),to:new Date(result.summary.to).toISOString(),include:'summary',size:50};
 await assert.rejects(cache.query(query,'',new AbortController().signal,codec,new Map(),undefined,false,undefined,page=>{published=page}),/offline/);
 assert.ok(published);assert.equal(published.result.summary,undefined);
 broken=false;assert.equal((await cache.query(query,'',new AbortController().signal,codec,new Map())).result.summary.count,864);
});
