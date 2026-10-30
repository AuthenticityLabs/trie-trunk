// Production UI + real Go endpoint; isolated WebKit profile, no user storage.
import assert from 'node:assert/strict';
import {readFile,mkdtemp,rm,writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
const {webkit}=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const target=process.env.TRUNK_COLLECTION_BROWSER_URL??'/tmp/snoot-collection-browser-url',origin=(await readFile(target,'utf8')).trim();
const profile=await mkdtemp(join(tmpdir(),'snoot-collection-profile-'));let browser;
try{
 browser=await webkit.launchPersistentContext(profile,{headless:true,executablePath:process.env.WEBKIT_PATH});
 const page=await browser.newPage();page.setDefaultTimeout(90000);const issues=[];
 page.on('pageerror',e=>issues.push(e.message));
 await page.goto(origin);const form=page.getByRole('form',{name:'Search Events'});
 await form.getByRole('combobox',{name:'Search Query'}).fill('events');await form.getByLabel('Time Window',{exact:true}).selectOption('86400');
 await form.getByLabel('Tantivy Comparison').setChecked(false);
 async function completed(uses=0){
  const deadline=Date.now()+90000;while(Date.now()<deadline){
   const row=await page.evaluate(async uses=>{
    if(!(await indexedDB.databases()).some(d=>d.name==='snoot-searches'))return;
    const rows=await new Promise((resolve,reject)=>{const open=indexedDB.open('snoot-searches');open.onerror=()=>reject(open.error);open.onsuccess=()=>{const db=open.result;const tx=db.transaction('SearchesLRU'),r=tx.objectStore('SearchesLRU').getAll();r.onsuccess=()=>resolve(r.result);tx.oncomplete=()=>db.close();tx.onabort=()=>{db.close();reject(tx.error)}}});
    return rows.find(r=>r.label==='events'&&r.status==='complete'&&r.uses>uses);
   },uses);
   if(row)return row;const alert=await page.locator('.paged-results [role=alert]').allTextContents();if(alert.length)throw Error(alert.join('\n'));await new Promise(r=>setTimeout(r,100));
  }throw Error('Search did not complete');
 }
 await form.getByRole('button',{name:'Search',exact:true}).click();const cold=await completed();assert.equal(cold.measurements.matched,10000,JSON.stringify({cold,wire:await(await fetch(origin+'/__test/collections')).json()}));
 await form.getByRole('button',{name:'Search',exact:true}).click();const warm=await completed(cold.uses);assert.equal(warm.measurements.matched,10000);
 const wire=await(await fetch(origin+'/__test/collections')).json();
 assert.equal(wire.length,2);assert.equal(wire[1].Checkpoint,cold.checkpointEpochMs-1);
 assert.equal(wire[0].Frames,10000);assert.equal(wire[1].Frames,2);assert.deepEqual(issues,[]);
 assert.equal(await page.getByText('Committed events only · the recent arrival window is still open').count(),0);
 assert.equal((await fetch(origin+'/__test/append',{method:'POST'})).status,204);
 await form.getByRole('button',{name:'Search',exact:true}).click();const changed=await completed(warm.uses);assert.equal(changed.measurements.matched,10001);
 const updated=await(await fetch(origin+'/__test/collections')).json();assert.equal(updated.length,3);assert.equal(updated[2].Checkpoint,warm.checkpointEpochMs-1);assert.equal(updated[2].Frames,3);
 console.log(JSON.stringify({arrival:{ms:changed.measurements.elapsedMs,bytes:updated[2].Bytes,liveFrames:updated[2].Frames,matched:changed.measurements.matched},browser:'WebKit',events:10000,first:{ms:cold.measurements.elapsedMs,bytes:wire[0].Bytes,liveFrames:wire[0].Frames,serverMs:wire[0].ElapsedMs},repeat:{ms:warm.measurements.elapsedMs,bytes:wire[1].Bytes,liveFrames:wire[1].Frames,serverMs:wire[1].ElapsedMs,requests:1},checkpoint:wire[1].Checkpoint}));
}finally{await browser?.close();await rm(profile,{recursive:true,force:true});await writeFile(target+'.done','done')}
