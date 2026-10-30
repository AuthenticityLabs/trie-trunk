// Real IndexedDB transactions in an isolated browser profile; no personal data.
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {mkdtemp,readFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve,sep} from 'node:path';
import {fileURLToPath} from 'node:url';
import {build} from 'vite';
const drivers=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const output=await mkdtemp(join(tmpdir(),'snoot-searches-test-'));
const origin=`https://searches-${randomUUID()}.test`;
let browser;
try{
 await build({configFile:false,logLevel:'warn',build:{outDir:output,emptyOutDir:true,lib:{entry:fileURLToPath(new URL('../services/search/searches-lru.ts',import.meta.url)),formats:['es'],fileName:()=>'searches.js'}}});
 const engine=process.env.SEARCH_BROWSER??'webkit';
 browser=await drivers[engine].launchPersistentContext(join(output,'profile'),{headless:true,executablePath:engine==='webkit'?process.env.WEBKIT_PATH:process.env.CHROME_PATH});
 await browser.route(origin+'/**',async route=>route.fulfill(new URL(route.request().url()).pathname==='/searches.js'?{contentType:'text/javascript',body:await readFile(join(output,'searches.js'))}:{contentType:'text/html',body:'<!doctype html><title>Search metadata tests</title>'}));
 const page=await browser.newPage();await page.goto(origin);
 const checks=await page.evaluate(async()=>{
  const m=await import('/searches.js'),ok=(condition,message)=>{if(!condition)throw Error(message)};
  let clock=1_800_000_000_000;
  const cache=new m.SearchesLRU({now:()=>clock});
  const context={source:'credential never stored',scope:'prod/payment',definitions:['b','a']},window={from:100,to:20000};
  const query=n=>({filters:[{field:'duration',op:'>',value:{kind:'number',value:String(n)}}],order:'asc'});
  const complete=(run,time=11000)=>cache.finish(run,{status:'complete',progress:{checkpointEpochMs:time},measurements:{elapsedMs:3,matched:0,loaded:0}});
  let run=await cache.start(query(1),context,window);await complete(run);
  let entry=await cache.get(run.signature);
  ok(entry.checkpointEpochMs===11000&&entry.measurements.matched===0,'zero-match checkpoint');
  ok(!JSON.stringify(entry).includes(context.source),'credentials persisted');
  ok(entry.label==='events | where duration > 1','readable label');
  run=await cache.start({...query(1),order:'desc'},{...context,definitions:['a','b']},{from:100,to:21000});
  ok(run.previous.checkpointEpochMs===11000,'end-window extension/sort reuse');await complete(run,12000);
  const sameSignature=run.signature;
  run=await cache.start(query(1),{...context,scope:'prod/identity'},window);
  ok(run.signature===sameSignature&&!run.previous,'scope in key or checkpoint crossed scope');
  ok((await cache.get(run.signature)).checkpointEpochMs===undefined,'scope inherited checkpoint');await complete(run);
  run=await cache.start(query(1),{...context,source:'different server'},window);ok(!run.previous,'source crossed');await complete(run);
  run=await cache.start(query(1),{...context,definitions:['other']},window);ok(!run.previous,'Definition crossed');await complete(run);
  run=await cache.start(query(1),context,window);await complete(run);
  run=await cache.start(query(1),context,{from:101,to:20000});ok(run.previous?.checkpointEpochMs===11000,'sliding start lost checkpoint');await complete(run);
  run=await cache.start(query(1),context,window);ok(!run.previous,'earlier start crossed');await complete(run);
  run=await cache.start(query(1),context,{from:101,to:11000});ok(!run.previous,'shrinking end crossed');await complete(run,10000);
  // LRU touches do not indefinitely extend the two-hour expiry.
  let expiry=(await cache.get(run.signature)).expiresAt;
  clock+=m.SEARCH_TTL_MS-1;ok((await cache.get(run.signature)).expiresAt===expiry,'read renewed TTL');
  clock++;ok((await cache.list()).length===0,'TTL boundary not pruned');
  // A new successful evaluation does renew its own metadata lifetime.
  run=await cache.start(query(1),context,window);clock+=10;await complete(run);
  ok((await cache.get(run.signature)).expiresAt===clock+m.SEARCH_TTL_MS,'successful refresh TTL');
  run=await cache.start(query(1),context,window);expiry=(await cache.get(run.signature)).expiresAt;
  await cache.finish(run,{status:'cancelled'});entry=await cache.get(run.signature);
  ok(entry.checkpointEpochMs===11000&&entry.expiresAt===expiry,'cancellation advanced progress or TTL');
  ok((await cache.start(query(1),context,window)).previous.checkpointEpochMs===11000,'cancellation discarded prior valid progress');
  const older=await cache.start(query(2),context,window),newer=await cache.start(query(2),context,window);
  await complete(newer,12000);await complete(older,13000);
  ok((await cache.get(newer.signature)).checkpointEpochMs===12000,'out-of-order completion won');
  const pending=await cache.start(query(3),context,window);await cache.clear(pending.signature);await complete(pending);
  ok(!await cache.get(pending.signature),'cleared search resurrected');
  await cache.clear();
  const signatures=[];
  for(let n=0;n<20;n++)signatures.push((await cache.start(query(n),context,window)).signature);
  await cache.get(signatures[0]);await cache.start(query(20),context,window);
  const rows=await cache.list();
  ok(rows.length===20&&rows.some(r=>r.signature===signatures[0])&&!rows.some(r=>r.signature===signatures[1]),'20-entry LRU eviction, including equal clocks');
  await cache.clear();
  // Persistence is checked after navigation by the outer test.
  const persistent=await cache.start(query(99),context,window);await complete(persistent);
  return {signature:persistent.signature,checks:['Predicate Key','Context Isolation','Zero-Match Progress','TTL','Cancellation','Concurrent Completion','Clear','20-Entry LRU']};
 });
 await page.reload();
 const persisted=await page.evaluate(async signature=>{const m=await import('/searches.js');return (await new m.SearchesLRU({now:()=>1_800_000_000_000}).get(signature))?.checkpointEpochMs},checks.signature);
 assert.equal(persisted,11000);
 // Two tabs contend on the same database; pruning and eviction stay atomic.
 const second=await browser.newPage();await second.goto(origin);
 const add=async(page,offset)=>page.evaluate(async offset=>{
  const {SearchesLRU}=await import('/searches.js'),cache=new SearchesLRU({now:()=>1_800_000_000_000});
  await Promise.all(Array.from({length:15},(_,i)=>cache.start({filters:[],search:{text:String(offset+i),mode:'contains'}},{source:'same server',scope:'',definitions:[]},{from:0,to:1})));
 },offset);
 await Promise.all([add(page,0),add(second,15)]);
 assert.equal(await page.evaluate(async()=>{const {SearchesLRU}=await import('/searches.js');return (await new SearchesLRU({now:()=>1_800_000_000_000}).list()).length}),20);
 console.log(JSON.stringify({browser:engine,passed:[...checks.checks,'Reload Persistence','Cross-Tab Transactions']}));
 if(process.env.SNOOT_ORIGIN){
  if(process.env.SNOOT_WEB_DIR){
   const root=resolve(process.env.SNOOT_WEB_DIR);
   await browser.route(process.env.SNOOT_ORIGIN+'/**',async route=>{
    const path=new URL(route.request().url()).pathname;
    if(path==='/'||path.startsWith('/assets/')){
     const file=resolve(root,path==='/'?'index.html':'.'+path);
     if(file.startsWith(root+sep))return route.fulfill({path:file});
    }
    return route.continue();
   });
  }
  const app=await browser.newPage();app.setDefaultTimeout(90000);
  await app.goto(process.env.SNOOT_ORIGIN);
  const form=app.getByRole('form',{name:'Search Events'});
  await form.getByRole('combobox',{name:'Search Query'}).fill('events | where duration >= 10000');
  await form.getByLabel('Time Window',{exact:true}).selectOption('86400');
  await form.getByLabel('Tantivy Comparison').setChecked(false);
  await app.evaluate(()=>{
   window.readSearches=async()=>{
    // Opening a nonexistent DB here would create an empty schema before the
    // worker initializes it. This observer must never create application stores.
    if(!(await indexedDB.databases()).some(db=>db.name==='snoot-searches'))return [];
    return new Promise((resolve,reject)=>{
    const open=indexedDB.open('snoot-searches',1);
    open.onerror=()=>reject(open.error);
    open.onsuccess=()=>{
     const db=open.result;
     if(!db.objectStoreNames.contains('SearchesLRU')){db.close();resolve([]);return}
     const tx=db.transaction('SearchesLRU'),get=tx.objectStore('SearchesLRU').getAll();
     get.onsuccess=()=>resolve(get.result);tx.oncomplete=()=>db.close();tx.onabort=()=>{db.close();reject(tx.error)};
    };
    });
   };
  });
  async function completed(uses=0){
   const deadline=Date.now()+90000;let rows;
   do{
    rows=await app.evaluate(()=>window.readSearches());
    const entry=rows.find(row=>row.status==='complete'&&row.label.includes('duration >= 10000')&&row.uses>uses);
    if(entry)return entry;
    await new Promise(resolve=>setTimeout(resolve,100));
   }while(Date.now()<deadline);
   throw Error('Query metadata did not complete: '+JSON.stringify(rows));
  }
  await form.getByRole('button',{name:'Search',exact:true}).click();
  const first=await completed();
  assert.equal(await app.locator('.paged-results [role=alert]').count(),0);
  await form.getByRole('button',{name:'Search',exact:true}).click();
  const repeated=await completed(first.uses);
  assert.equal(repeated.measurements.matched,first.measurements.matched);
  if(first.checkpointEpochMs!==undefined)assert.ok(repeated.checkpointEpochMs>=first.checkpointEpochMs);
  console.log(JSON.stringify({integration:'Local Search',uses:repeated.uses,matched:repeated.measurements.matched,checkpointEpochMs:repeated.checkpointEpochMs}));
  const expand=app.getByRole('button',{name:'Expand navigation',exact:true});
  if(await expand.isVisible())await expand.click();
  await app.getByRole('button',{name:/^Profile for /}).click();
  await app.getByRole('tab',{name:'Storage',exact:true}).click();
  await app.getByRole('button',{name:'Open Recent Searches',exact:true}).waitFor();
  const replicasBefore=await app.locator('.storage-item:not([data-storage-id="searches"])').allTextContents();
  await app.getByRole('button',{name:'Open Recent Searches',exact:true}).click();
  await app.getByRole('button',{name:'Clear This Folder',exact:true}).click();
  let reloads=0;app.on('framenavigated',frame=>{if(frame===app.mainFrame())reloads++});
  await app.getByRole('button',{name:'Clear Local Copy',exact:true}).click();
  await app.getByRole('region',{name:'Confirm Local Deletion',exact:true}).waitFor({state:'detached'});
  assert.deepEqual(await app.evaluate(()=>window.readSearches()),[]);
  assert.deepEqual(await app.locator('.storage-item:not([data-storage-id="searches"])').allTextContents(),replicasBefore);
  assert.equal(reloads,0,'Metadata deletion must not reload or reset the native event store');
  assert.equal(await app.locator('#profile-storage-panel [role=alert]').count(),0);
  console.log('Profile Storage: clear query metadata in place; profile and native replicas preserved.');
 }
}finally{await browser?.close();await rm(output,{recursive:true,force:true})}
