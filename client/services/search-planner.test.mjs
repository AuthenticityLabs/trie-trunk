import {test} from 'node:test';
import assert from 'node:assert/strict';
import {planSearch,prepareSearch,searchWindows,searchTasks} from './search/planner.ts';
import {HOUR,TIME_INDEX,TIME_STATE,TIME_SUM,TIME_SODS} from './store/time-trie.ts';
const from=Date.UTC(2026,9,20),definition='a'.repeat(64),tap='prod/payment/transactions.v1/write';
const trunk={catalog:{files:['tri','idx','pods','sum'].map(kind=>({path:`prod/payment/transactions.v1/0/write.${kind}`,kind,definition,bootstrap:false}))},definitions:[{digest:definition}],bindings:[{digest:definition,tap}]};
const receipt=rows=>async(_token,command)=>{assert.equal(command.kind,'time-read-many');assert.equal(command.queries[0].tap,tap);return {times:command.queries.map(()=>({rows,missing:[],serial:0}))}};
test('planner selects complete hourly packs independently of index replication',async()=>{
 const rows=[{time:from,count:5,flags:TIME_SUM},{time:from+HOUR,count:7,flags:TIME_INDEX|TIME_STATE},{time:from+2*HOUR,count:3,flags:TIME_SODS}];
 const plan=await planSearch(trunk,'prod/payment/*/*',from,from+3*HOUR,'',receipt(rows));
 assert.equal(plan.matching,'client');assert.deepEqual(plan.packRequests.map(r=>[r.from,r.to]),[[from,from+2*HOUR]]);
 assert.equal(plan.branches[0].hours[1].events,false);assert.equal(plan.branches[0].hours[1].index,true);
 assert.equal(plan.branches[0].hours[2].index,false);assert.equal(plan.branches[0].hours[2].events,true);
});
test('adjacent missing event hours coalesce while empty and unknown hours are preserved as distinct states',async()=>{
 const rows=[{time:from,count:2,flags:0},{time:from+HOUR,count:3,flags:0},{time:from+2*HOUR,count:0,flags:0},{time:from+4*HOUR,count:1,flags:0}];
 const plan=await planSearch(trunk,'',from,from+5*HOUR,'',receipt(rows));
 assert.deepEqual(plan.packRequests.map(r=>[r.from,r.to]),[[from,from+2*HOUR],[from+4*HOUR,from+5*HOUR]]);
 assert.equal(plan.branches[0].hours.length,4);assert.equal(plan.branches[0].hours[2].count,0);
});
test('a replicated index alone does not satisfy whole-pack collection',async()=>{
 const plan=await planSearch(trunk,'',from,from+HOUR,'',receipt([{time:from,count:90000,flags:TIME_INDEX|TIME_STATE}]));
 assert.equal(plan.packRequests.length,1);assert.equal(plan.branches[0].hours[0].events,false);
});
test('planner rejects missing catalog paths instead of guessing a resource minor',async()=>{
 const missing=structuredClone(trunk);missing.catalog.files=missing.catalog.files.filter(f=>f.kind!=='pods');
 await assert.rejects(planSearch(missing,'',from,from+HOUR,'',receipt([])),/Missing catalog pods/);
});
test('canceled search never starts time or index requests',async()=>{
 const c=new AbortController();c.abort();let calls=0;
 await assert.rejects(prepareSearch(trunk,'',from,from+HOUR,{signal:c.signal,token:'',archive:receipt([]),fetcher:async()=>{calls++;throw Error('unexpected')},definitions:new Map(),codec:{}}),/abort/i);
 assert.equal(calls,0);
});

test('first-hour planning skips empty gaps and batches the remaining day in order',async()=>{
 const rows=[0,1,5,25].map(h=>({time:from+h*HOUR,count:10,flags:0}));
 const plan=await planSearch(trunk,'',from,from+26*HOUR,'',receipt(rows));
 assert.deepEqual(searchWindows(plan,from,from+26*HOUR,false),[{from,to:from+HOUR},{from:from+HOUR,to:from+6*HOUR},{from:from+25*HOUR,to:from+26*HOUR}]);
 assert.deepEqual(searchWindows(plan,from,from+26*HOUR,true),[{from:from+25*HOUR,to:from+26*HOUR},{from,to:from+6*HOUR}]);
});
test('failed parallel work cancels siblings and waits for cleanup before returning',async()=>{
 let canceled=false,finished=false;
 await assert.rejects(searchTasks([0,1,2,3,4],new AbortController().signal,async(i,_index,signal)=>{
  if(i===0){await new Promise(resolve=>setTimeout(resolve,0));throw Error('collection failed')}
  if(i===4)throw Error('must not schedule after failure');
  await new Promise(resolve=>signal.addEventListener('abort',()=>{canceled=true;resolve()},{once:true}));
  await Promise.resolve();finished=true;
 }),/collection failed/);
 assert.ok(canceled&&finished);
});

 test('known empty hours join missing packs without joining unknown or collected hours',async()=>{
 const rows=[{time:from,count:2,flags:0},{time:from+HOUR,count:0,flags:0},{time:from+2*HOUR,count:3,flags:0},{time:from+3*HOUR,count:4,flags:TIME_SODS},{time:from+4*HOUR,count:2,flags:0}];
 const plan=await planSearch(trunk,'',from,from+5*HOUR,'',receipt(rows));
 assert.deepEqual(plan.packRequests.map(r=>[r.from,r.to]),[[from,from+3*HOUR],[from+4*HOUR,from+5*HOUR]]);
 });

test('performance contract: 25 sparse branches use one metadata batch and one range each; warm replicas request no packs',async()=>{
 const files=[],bindings=[];
 for(const service of ['payment','billing','identity','gateway','worker'])for(const branch of ['read','write','error','debug','trace']){
  const base=`prod/${service}/events.v1`,tap=`${base}/${branch}`;
  bindings.push({digest:definition,tap});
  for(const kind of ['tri','idx','pods'])files.push({path:`${base}/0/${branch}.${kind}`,kind,definition,bootstrap:false});
 }
 const catalog={catalog:{files},definitions:[{digest:definition}],bindings};
 for(const warm of [false,true]){
  let calls=0;
  const archive=async(_token,c)=>{calls++;assert.equal(c.kind,'time-read-many');assert.equal(c.queries.length,25);return {times:c.queries.map(()=>({serial:1,missing:[],rows:Array.from({length:24},(_,h)=>({time:from+h*HOUR,count:h%2===0?5:0,flags:warm?TIME_SODS:0}))}))};};
  const plan=await planSearch(catalog,'',from,from+24*HOUR,'',archive);
  assert.equal(calls,1,'branch count must not multiply metadata commands');
  assert.equal(plan.packRequests.length,warm?0:25,'known-empty hours must not fragment collection or cause refetch');
  if(!warm)for(const r of plan.packRequests)assert.deepEqual([r.from,r.to],[from,from+23*HOUR]);
 }
});
test('performance contract: collection concurrency remains bounded across many branches',async()=>{
 let active=0,maximum=0,completed=0;
 await searchTasks(Array.from({length:100},(_,i)=>i),new AbortController().signal,async()=>{
  maximum=Math.max(maximum,++active);await new Promise(resolve=>setTimeout(resolve,0));active--;completed++;
 });
 assert.equal(completed,100);assert.ok(maximum>1&&maximum<=4,`unexpected concurrent collection count: ${maximum}`);
});


test('performance contract: long-range collection retains early results without one request per calendar day',async()=>{
 const rows=Array.from({length:720},(_,h)=>({time:from+h*HOUR,count:5,flags:0}));
 const plan=await planSearch(trunk,'',from,from+720*HOUR,'',receipt(rows));
 assert.equal(plan.packRequests.length,1);
 assert.deepEqual(searchWindows(plan,from,from+720*HOUR,false),[
  {from,to:from+HOUR},{from:from+HOUR,to:from+24*HOUR},{from:from+24*HOUR,to:from+720*HOUR}]);
 assert.deepEqual(searchWindows(plan,from,from+720*HOUR,true),[
  {from:from+719*HOUR,to:from+720*HOUR},{from:from+696*HOUR,to:from+719*HOUR},{from,to:from+696*HOUR}]);
});
