import { summaryResponse } from './summary-pack-fixture.mjs';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { FastProjection, panelGroups, fastDashboard, chartCSV, chartValue, sparklinePath, trendAngle, validateFastRequest, dayMs } from './fast-dashboard.ts';
import { decodeSummaryDay } from './pil/summaries.ts';
import { ViewCache } from './pil/view.ts';
import { Codec } from './pil/wasm.ts';
const root=new URL('./fixtures/daily-summary/aligned/',import.meta.url);
const fixture=JSON.parse(readFileSync(new URL('manifest.json',root)));
const raw=r=>new Uint8Array(readFileSync(new URL(`${r.key}.sumday`,root)));
const request={scopes:['demo/payment'],from:fixture.result.summary.from,to:fixture.result.summary.to};

test('trend arrows are symmetric, bounded and explicit about missing data',()=>{
 assert.equal(trendAngle(100,200),45);
 assert.equal(trendAngle(200,100),-45);
 assert.equal(trendAngle(100,100.5),0);
 assert.equal(trendAngle(0,0),0);
 assert.equal(trendAngle(0,1),90);
 assert.equal(trendAngle(1,0),-90);
 for(const pair of [[null,1],[1,null],[NaN,1],[1,Infinity],[-1,1]])assert.equal(trendAngle(...pair),null);
});

test('trend halves aggregate histograms rather than averaging bucket percentiles',()=>{
 const projection=new FastProjection({scopes:['demo/payment'],from:0,to:2*dayMs});
 // Exercise the same accumulators used by summary-cell ingestion.
 projection.halves[0]={events:100,errors:10,histogram:new Map([[10,99],[1000,1]])};
 projection.halves[1]={events:200,errors:10,histogram:new Map([[20,190],[1000,10]])};
 const {earlier,later}=projection.finish().comparison;
 assert.equal(earlier.errorRate,10);
 assert.equal(later.errorRate,5);
 assert.equal(earlier.average,19.9);
 assert.equal(later.average,69);
 assert.equal(earlier.p95,10);
 assert.equal(later.p95,20);
});

test('statistic trends preserve missing buckets, zero values, and isolated observations',()=>{
  assert.equal(sparklinePath([]),'');
  assert.equal(sparklinePath([null,NaN]),'');
  assert.equal(sparklinePath([0,0]),'M1,26L1,26L99,26');
  assert.equal(sparklinePath([10,null,20]),'M1,14L1,14M99,2L99,2');
  assert.equal(sparklinePath([5]),'M1,2L1,2');
  assert.equal(sparklinePath([10,20]),sparklinePath([10000,20000]));
});

test('Fast charts aggregate real columnar packets with weighted means and exact merged p95',async()=>{
  const p=new FastProjection(request),manifest=fixture.result.analytics;
  p.consume(manifest,await Promise.all(manifest.days.map(r=>decodeSummaryDay(raw(r)))));
  const result=p.finish();
  const values=Array.from({length:288},(_,i)=>i).filter(i=>i%3!==0).map(i=>i+11);
  assert.equal(result.totals.events,864);assert.equal(result.totals.errors,288);
  assert.equal(result.totals.samples,576);assert.equal(result.totals.p95,values[Math.ceil(values.length*.95)-1]);
  assert.ok(Math.abs(result.totals.average-values.reduce((a,b)=>a+b)/values.length)<1e-9);
  assert.equal(result.time.reduce((n,t)=>n+t.events,0),864);
  assert.equal(result.comparison.earlier.events+result.comparison.later.events,result.totals.events);
  assert.ok(result.comparison.earlier.samples>0&&result.comparison.later.samples>0);
  const halves=[result.comparison.earlier,result.comparison.later];
  assert.ok(Math.abs(halves.reduce((sum,h)=>sum+h.average*h.samples,0)/result.totals.samples-result.totals.average)<1e-9);
  assert.equal(result.groups.resource.length,1);
  assert.equal(result.groups.service[0].key,'demo/payment');
  assert.equal(result.groups.level.reduce((n,g)=>n+g.events,0),864);
  assert.throws(()=>p.consume(manifest,[]),/Missing/);
  assert.throws(()=>p.consume(manifest,[{}, {}, {}]),/Duplicate/);
});
test('Top and Bottom ranks are applied to the complete group set, with stable ties',()=>{
  const groups=Array.from({length:25},(_,i)=>({key:String(i).padStart(2,'0'),label:String(i),events:i+1}));
  const top=fastDashboard.panels.find(p=>p.id==='top'),bottom=fastDashboard.panels.find(p=>p.id==='bottom');
  assert.deepEqual(panelGroups(top,{groups:{resource:groups}}).map(g=>g.events),[25,24,23,22,21,20,19,18,17,16]);
  assert.deepEqual(panelGroups(bottom,{groups:{resource:groups}}).map(g=>g.events),[1,2,3,4,5,6,7,8,9,10]);
  assert.equal(groups[0].events,1,'ranking does not reorder the source');
});
test('Fast scopes reject overlap, arbitrary paths and unbounded time ranges',()=>{
  validateFastRequest(request);
  for(const scopes of [['demo/payment','demo/payment/transactions.v1'],['demo/payment','demo/payment'],['*/*'],['../payment']])assert.throws(()=>validateFastRequest({...request,scopes}));
  assert.throws(()=>validateFastRequest({...request,to:request.from+367*dayMs}));
  assert.throws(()=>validateFastRequest({...request,from:request.from+1}));
});
test('JSON panel contract keeps data shapes and compatible views separate',()=>{
  const compatible={number:['value'],boolean:['status'],set:['tags'],table:['bars','list-trends','table'],series:['timechart']};
  assert.equal(new Set(fastDashboard.panels.map(p=>p.id)).size,fastDashboard.panels.length);
  for(const p of fastDashboard.panels){assert.ok(compatible[p.data.shape].includes(p.view.kind));assert.ok([3,4,6,12].includes(p.span));}
  assert.equal(fastDashboard.source,'summaries');
});
test('CSV preserves exact rows and escapes spreadsheet formulas and quotes',()=>{
  assert.equal(chartCSV([{key:'=test,"x"',events:5}],['events']),'"Resource","events"\r\n"\'=test,""x""","5"');
});
test('Fast projection reuses cached summary packets and never requests event bodies',async()=>{
  const calls=[],payload=readFileSync(new URL('view.pil',root));
  const fetcher=async(url,options)=>{
    calls.push(url);
    if(url.startsWith('/demo/indexes?')){
      assert.equal(new URL(url,'http://localhost').searchParams.get('include'),'summary');
      assert.equal(options.headers['Summary-Known'],undefined,'projection always gets the source manifest');
      return new Response(payload,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Trunk-Format':'pac-view/0'}});
    }
    assert.equal(url,'/demo/summaries');
    return summaryResponse(JSON.parse(options.body).packs.map(raw));
  };
  const codec=new Codec((await WebAssembly.instantiate(readFileSync(new URL('../assets/pil.wasm',import.meta.url)))).instance),cache=new ViewCache(fetcher),definitions=new Map();
  for(let run=0;run<2;run++){
    const p=new FastProjection(request);
    const view=await cache.query({engine:'tantivy',tap:'demo/payment/*/*',from:new Date(request.from).toISOString(),to:new Date(request.to).toISOString(),include:'summary',size:1},'',new AbortController().signal,codec,definitions,undefined,false,(manifest,days)=>p.consume(manifest,days));
    assert.equal(p.count,view.result.summary.count);assert.equal(view.batch.rows.length,0);
  }
  assert.equal(calls.filter(u=>u.startsWith('/demo/indexes?')).length,2);
  assert.equal(calls.filter(u=>u==='/demo/summaries').length,1);
});

test('duration display converts the microsecond role to milliseconds without rounding data',()=>{
  assert.equal(chartValue(370624,'ms'),370.624);
  assert.equal(chartValue(null,'ms'),null);
  assert.equal(chartValue(3.5,'percent'),3.5);
  assert.match(chartCSV([{key:'demo/payment',average:110266.7,p95:370624}],['average','p95']),/average_us","p95_us/);
});
test('summary coverage remains explicit when some branches lack precomputed columns',()=>{
  const projection=new FastProjection(request);
  projection.cover(100,0);
  const result=projection.finish();
  assert.equal(result.matched,100);assert.equal(result.totals.events,0);
  assert.equal(result.totals.average,null);assert.equal(result.totals.p95,null);
  assert.throws(()=>projection.cover(10,11),/coverage/);
});
