// Run by the Go HTTP integration test against an isolated server and disposable
// storage. Uses the production worker, decoder, planner, and native writer.
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {workerService} from '../services/pil/worker-runtime.ts';
import {NativeStore} from '../services/store/native.ts';
import {nativeTransaction} from '../services/store/transaction.ts';
import {Backend} from '../services/native-backend-fixture.mjs';
import {TIME_SUM,TIME_INDEX,TIME_STATE,TIME_SODS,timeLeaf} from '../services/store/time-trie.ts';
const base=process.argv[2];if(!base)throw Error('Server URL required');
const wasm=WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
const backend=new Backend(),sessions=new Map(),definitions=new Map(),calls=[];
let serial=Promise.resolve(),failSummaryCommit=false;
const archive=(_token,c)=>{
 const task=serial.then(async()=>{
  const store=new NativeStore(backend);
  switch(c.kind){
   case 'definitions':return {definitions:[...definitions.values()]};
   case 'trunk-save':for(const d of c.snapshot.definitions)definitions.set(d.digest,d);return {};
   case 'time-read-many':return {times:await store.timeWindows(c.queries)};
   case 'time-read':return {time:await store.timeWindow(c.query)};
   case 'time-save':await nativeTransaction(backend,b=>new NativeStore(b).importTime(c.input));return {};
   case 'summary-read':return {summaryBytes:await store.summaryBytes(c.query)};
   case 'summary-save':if(failSummaryCommit){failSummaryCommit=false;backend.fail=true;}await nativeTransaction(backend,b=>new NativeStore(b).importSummary(c.input));return {};
   case 'native-coverage':return {nativeCoverage:await store.coverage(c.tap)};
   case 'native-resolve':return {nativeDependencies:await store.resolveHour(c)};
   case 'native-dependencies':return {nativeDependencies:await store.dependencies(c.source)};
   case 'native-page':return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit,c.descending)};
   case 'native-rows':return {nativePage:await store.records(c.source,c.from,c.to,c.rows)};
   case 'native-begin':sessions.set(c.session,store.begin(await store.serial()));return {};
   case 'native-abort':sessions.delete(c.session);return {};
   case 'native-records':case 'native-finish':{
    const session=sessions.get(c.session);assert.ok(session);
    await nativeTransaction(backend,async b=>{const staged=new NativeStore(b);for(const r of c.records??[])await staged.record(session,r.record,r.definition);if(c.kind==='native-finish')await staged.finish(session)});
    if(c.kind==='native-finish'){sessions.delete(c.session);return {nativeSources:[...new Set(session.packages.map(p=>p.key))]};}return {};
   }
   default:throw Error('Unexpected archive command '+c.kind);
  }
 });serial=task.catch(()=>{});return task;
};
let truncatePack=false;
const fetcher=async(path,options)=>{calls.push(path);const response=await fetch(new URL(path,base),options);if(truncatePack&&new URL(path,base).pathname.endsWith('/seeds')){const raw=new Uint8Array(await response.arrayBuffer());return new Response(raw.subarray(0,raw.length-2),{headers:response.headers});}return response;};
function client(){
 const channel=new MessageChannel(),connect=workerService(wasm,fetcher,archive);connect(channel.port2);
 const jobs=new Map();let id=0;
 channel.port1.onmessage=({data:m})=>{const job=jobs.get(m.id);if(!job)return;job.messages.push(m);if(m.kind==='done'){jobs.delete(m.id);job.resolve(job.messages)}if(m.kind==='error'){jobs.delete(m.id);job.reject(Error(m.message))}};
 return {run(command){return new Promise((resolve,reject)=>{const next=++id;jobs.set(next,{resolve,reject,messages:[]});channel.port1.postMessage({id:next,token:'',...command})})},close(){channel.port1.postMessage({kind:'close'});channel.port1.close()}};
}
let tab=client();
try{
 await tab.run({kind:'trunk'});
 const query={tap:'demo/payment/transactions.v1/write',from:'2026-10-20T00:00:00Z',to:'2026-10-21T00:00:00Z',size:2,query:{filters:[],order:'asc'},include:'summary,page,statistics'};
 const search=async(q=query)=>{const messages=await tab.run({kind:'view',query:q});return messages.find(m=>m.kind==='view').view};
 const cold=await search();assert.equal(cold.result.page.length,2);assert.equal(cold.result.summary.count,4);
 const path=p=>new URL(p,base).pathname;
 const paths=calls.map(path);assert.ok(paths.some(p=>p.endsWith('/timelines')));assert.ok(paths.some(p=>p.endsWith('/seeds')));
 assert.ok(!paths.includes('/events'));assert.ok(!paths.some(p=>p.endsWith('/indexes')));assert.ok(!paths.includes('/collect'));
 let source=(await backend.list()).find(s=>s.tap===query.tap),hour=Date.parse(query.from);
 assert.equal(timeLeaf(source.timeTrie,hour).flags,TIME_INDEX|TIME_STATE|TIME_SODS);
 assert.equal(timeLeaf(source.timeTrie,hour+3600000).flags,TIME_INDEX|TIME_STATE|TIME_SODS,'whole hours precede local matching');
 const count=calls.length;const warm=await search();assert.equal(warm.result.summary.count,4);
 assert.deepEqual(calls.slice(count).map(path),[]);
 // A fresh worker has no response/decoded-memory caches. All native receipts
 // and packs must still be reusable, including independently replicated sums.
 tab.close();tab=client();await tab.run({kind:'trunk'});const restarted=calls.length;
 const again=await search();assert.equal(again.result.page.length,2);assert.deepEqual(calls.slice(restarted).map(path),[]);
 const second=await search({...query,after:again.result.next});assert.equal(second.result.page.length,2);
 source=(await backend.list()).find(s=>s.tap===query.tap);
 assert.ok(timeLeaf(source.timeTrie,hour+3600000).flags&TIME_SODS);
 const filtered=await search({...query,query:{filters:[],search:{text:'completed',mode:'terms'},order:'asc'}});
 assert.equal(filtered.result.summary.count,4);
 // A truncated hourly pack must fail collection without publishing completion
 // for physically written but incomplete data. No server search is involved.
 await new NativeStore(backend).rebase(query.tap);
 tab.close();tab=client();await tab.run({kind:'trunk'});truncatePack=true;const failedStart=calls.length;
 await assert.rejects(search(),/Incomplete|incomplete|Truncated|truncated|before successful completion/);
 assert.ok(!calls.slice(failedStart).map(path).includes('/events'));
 for(const s of (await backend.list()).filter(s=>!s.retiredBy))for(const r of s.timeTrie?.ranges??[])for(let t=r.from;t<r.to;t+=3600000)assert.equal(timeLeaf(s.timeTrie,t).flags&(TIME_INDEX|TIME_SODS),0);
 truncatePack=false;const retry=await search();assert.equal(retry.result.page.length,2);
 await new NativeStore(backend).rebase(query.tap);
 tab.close();tab=client();await tab.run({kind:'trunk'});failSummaryCommit=true;
 await assert.rejects(search({...query,include:'summary',query:undefined}),/crash before metadata commit/);
 for(const s of (await backend.list()).filter(s=>!s.retiredBy))for(const r of s.timeTrie?.ranges??[])for(let t=r.from;t<r.to;t+=3600000)assert.equal(timeLeaf(s.timeTrie,t).flags&TIME_SUM,0);
 const recovered=await search({...query,include:'summary',query:undefined});assert.equal(recovered.result.summary.count,4);
 console.log(JSON.stringify({coldRequests:paths,warmRequests:0,restartRequests:0,matched:4,firstPage:2,secondPage:2,summaryBytes:source.files.sum?.end??0,indexBytes:source.files.index.end,eventBytes:source.files.sods.end}));
}finally{tab.close()}
