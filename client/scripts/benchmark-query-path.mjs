// Isolated real-codec, HTTP and OPFS benchmark. Go fixture endpoints are supplied via server.json.
import assert from 'node:assert/strict';
const {webkit,chromium}=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const directory=process.env.QUERY_BENCH_ROOT??'/tmp/trie-framework-comparison';
import {mkdtemp,readFile,writeFile,rm} from 'node:fs/promises';
const variant=process.argv[2],engine=process.argv[3]??'webkit',mode=process.argv[4]??'local',label=variant+(mode==='tantivy'?'-tantivy':'')+(process.env.SUITE==='matched'?'-matched':'')+(process.env.RUN_ID?'-'+process.env.RUN_ID:'');
const {origin}=JSON.parse(await readFile(`${directory}/${variant}/server.json`));
const profile=await mkdtemp('/tmp/framework-browser-');
const context=await ({webkit,chromium}[engine]).launchPersistentContext(profile,{executablePath:engine==='webkit'?process.env.WEBKIT_PATH:process.env.CHROME_PATH,headless:true});
const results=[];
try{
 const page=await context.newPage();page.on('console',m=>console.log(m.text()));await page.goto(origin);
 const workloads=[
 {name:'empty-today',from:'2026-10-30T00:00:00Z',to:'2026-10-30T13:10:00Z',filters:[]},
 {name:'one-day',from:'2026-10-28T00:00:00Z',to:'2026-10-29T00:00:00Z',filters:[]},
 {name:'one-day-errors',from:'2026-10-28T00:00:00Z',to:'2026-10-29T00:00:00Z',filters:[{field:'http_status',op:'==',value:{kind:'number',value:'503'}}]},
 {name:'30-days',from:'2026-09-30T00:00:00Z',to:'2026-10-30T00:00:00Z',filters:[]},
 ];
 if(process.env.SUITE==='matched')for(const q of workloads)if(q.name==='one-day'||q.name==='30-days'){q.name+='-info';q.filters=[{field:'level',op:'==',value:{kind:'string',value:'INFO'}}];}
 for(const q of workloads){
  const result=await page.evaluate(async({variant,q,mode})=>await new Promise((resolve,reject)=>{
   const worker=new Worker('/bench/worker.js',{type:'module'});worker.onerror=e=>reject(Error(e.message));worker.onmessage=({data})=>{
    if(data.stage==='run')console.log(JSON.stringify({name:data.run.name,pass:data.run.pass,ms:data.run.ms,matched:data.run.matched,requests:data.run.requests.length}));
    if(data.error){worker.terminate();reject(Error(data.error+' '+data.stack));}if(data.result){worker.terminate();resolve(data.result);}
   };
   worker.postMessage({variant:variant+'-'+q.name+'-'+crypto.randomUUID(),repeats:3,queries:[{name:q.name,engine:mode,tap:'',from:q.from,to:q.to,query:{filters:q.filters,order:'asc'}}]});
  }),{variant,q,mode});results.push(result);
  assert.equal(new Set(result.runs.map(r=>r.matched)).size,1,'Cold/warm result counts must agree');
  if(variant==='current'&&mode==='local'){
   assert.ok(result.runs[0].requests.length<=85,'Whole-window collection request budget regressed');
   assert.ok(result.runs.slice(1).every(r=>r.requests.length===0),'Warm search unexpectedly used the network');
  }
  await writeFile(`${directory}/${label}-${engine}.json`,JSON.stringify({variant,engine,results},null,2));
 }
}finally{await context.close();await rm(profile,{recursive:true,force:true});}
