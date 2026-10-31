import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {readSummaryPacks} from './pil/summary-packs.ts';
import {SummaryDayCache} from './pil/summaries.ts';
import {summaryArray,summaryResponse} from './summary-pack-fixture.mjs';
const root=new URL('./fixtures/daily-summary/aligned/',import.meta.url);
const refs=JSON.parse(readFileSync(new URL('manifest.json',root))).result.analytics.days;
const pack=ref=>new Uint8Array(readFileSync(new URL(`${ref.key}.sumday`,root)));
const signal=()=>new AbortController().signal;
const response=raw=>new Response(raw,{headers:{'Content-Type':'application/pil'}});

test('array parser handles arbitrary network boundaries and applies backpressure per pack',async()=>{
  const packs=[new Uint8Array(32).fill(1),new Uint8Array(300).fill(2)],raw=summaryArray(packs);
  let at=0,release,entered;
  const gate=new Promise(r=>release=r),started=new Promise(r=>entered=r),seen=[];
  const stream=new ReadableStream({pull(controller){if(at===raw.length)controller.close();else controller.enqueue(raw.subarray(at,++at))}},{highWaterMark:0});
  const done=readSummaryPacks(response(stream),2,1024,signal(),async(i,body)=>{seen.push(body);if(i===0){entered();await gate}});
  await started;
  assert.equal(seen.length,1);assert.ok(at<raw.length,'must not consume later packs while the client is busy');
  release();await done;assert.deepEqual(seen,packs);
});

test('array parser rejects wrong count, framing, oversize, truncation, trailing bytes and wrong content type',async()=>{
  const raw=summaryArray([new Uint8Array(32)]);
  for(const bad of [Uint8Array.from([0x82,...raw.subarray(1)]),Uint8Array.from([0x81,0x7f]),raw.subarray(0,-1),Uint8Array.from([...raw,0])])
    await assert.rejects(readSummaryPacks(response(bad),1,1024,signal(),async()=>{}));
  await assert.rejects(readSummaryPacks(response(raw),1,31,signal(),async()=>{}),/limit/);
  await assert.rejects(readSummaryPacks(new Response(raw),1,1024,signal(),async()=>{}),/content type/);
});

test('array cancellation interrupts a blocked network reader',async()=>{
  let cancelled=false,ready;const started=new Promise(r=>ready=r),controller=new AbortController();
  const stream=new ReadableStream({pull(){ready()},cancel(){cancelled=true}});
  const done=readSummaryPacks(response(stream),1,1024,controller.signal,async()=>{});
  await started;controller.abort();await assert.rejects(done,{name:'AbortError'});assert.equal(cancelled,true);
});

test('batch search requests only cache holes, retains verified packs after interruption, and reuses them on retry',async()=>{
  const cache=new SummaryDayCache(),requests=[];
  const unique=[...new Map(refs.map(ref=>[ref.key,ref])).values()];assert.ok(unique.length>=2);
  let broken=true;
  const fetcher=async(url,options)=>{
    assert.equal(url,'/demo/summaries');assert.equal(options.method,'POST');
    const selected=JSON.parse(options.body).packs;requests.push(selected.map(ref=>ref.key));
    const raw=summaryArray(selected.map(pack));
    return response(broken?raw.subarray(0,-1):raw);
  };
  await assert.rejects(cache.loadMany(unique,'a',fetcher,signal()),/Truncated/);
  broken=false;
  await cache.loadMany(unique,'a',fetcher,signal());
  assert.deepEqual(requests[1],[unique.at(-1).key]);
  await cache.loadMany(unique,'a',fetcher,signal());assert.equal(requests.length,2);
  await cache.loadMany(unique,'b',fetcher,signal());assert.deepEqual(requests[2],unique.map(ref=>ref.key));
});

test('a long range collapses to one batch and a corrupt late pack never becomes a valid chart',async()=>{
  const cache=new SummaryDayCache(),selected=[...new Map(refs.map(ref=>[ref.key,ref])).values()];let calls=0;
  const fetcher=async(url,options)=>{calls++;const requested=JSON.parse(options.body).packs;assert.deepEqual(requested.map(ref=>ref.key),selected.map(ref=>ref.key));return summaryResponse(requested.map(pack))};
  const days=await cache.loadMany(Array.from({length:460},(_,i)=>selected[i%selected.length]),'',fetcher,signal());
  assert.equal(calls,1);assert.equal(days.length,460);
  await assert.rejects(new SummaryDayCache().loadMany(selected,'',async()=>{
    const packs=selected.map(pack);packs.at(-1)[40]^=1;return summaryResponse(packs);
  },signal()),/identity/);
});
