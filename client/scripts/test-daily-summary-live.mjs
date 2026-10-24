// Read-only live deployment check using the same PIL and summary decoder as Snoot.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { Codec } from '../services/pil/wasm.ts';
import { ViewCache } from '../services/pil/view.ts';
const origin=process.env.SNOOT_ORIGIN??'http://127.0.0.1:3020';
const fixture=JSON.parse(readFileSync(process.env.SNOOT_MANIFEST??new URL('../../caches/demo-data.json',import.meta.url)));
let summaryRequests=0,summaryBytes=0;
const calls=[];
const cache=new ViewCache(async(path,options)=>{
 calls.push(path);
 const response=await fetch(new URL(path,origin),options);
 if(!response.ok)throw Error(`${response.status}: ${await response.text()}`);
 if(path.startsWith('/summary-day?')) {
  summaryRequests++;
  const bytes=await response.arrayBuffer();summaryBytes+=bytes.byteLength;
  return new Response(bytes,{headers:response.headers});
 }
 return response;
});
const codec=new Codec((await WebAssembly.instantiate(readFileSync(new URL('../assets/pil.wasm',import.meta.url)))).instance);
const definitions=new Map();
const query={tap:'*/*/*/*',from:fixture.From,to:fixture.To,size:5,include:'summary'};
const signal=AbortSignal.timeout(120000);
const first=await cache.query(query,'',signal,codec,definitions);
assert.equal(first.result.summary.count,fixture.Events);
assert.equal(first.batch.rows.length,0);
assert.ok(summaryRequests>0,'must use daily packets');
assert.ok(first.result.summary.errors.reduce((a,b)=>a+b)>0);
assert.ok(calls.every(x=>!x.startsWith('/collect')),'summary must not download event PACs');
const fetched=summaryRequests;
const second=await cache.query(query,'',signal,codec,definitions);
assert.equal(second.result.summary.count,fixture.Events);
assert.equal(summaryRequests,fetched,'repeat query must reuse decoded summaries');
console.log(JSON.stringify({origin,events:first.result.summary.count,summaryRequests,summaryBytes,repeatSummaryRequests:summaryRequests-fetched,eventPACRequests:0}));
