// Read-only API checks against a running Snoot development preview.
// Uses an isolated persistent profile because WebKit requires one for OPFS.
import assert from 'node:assert/strict';
import {mkdtemp, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve,sep} from 'node:path';
const drivers=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const engine=process.env.SEARCH_BROWSER??'webkit';
const profile=await mkdtemp(join(tmpdir(),'snoot-search-controls-'));
const context=await drivers[engine].launchPersistentContext(profile,{
 executablePath:engine==='webkit'?process.env.WEBKIT_PATH:process.env.CHROME_PATH,
 headless:true,viewport:{width:1280,height:1000},
});
try{
 if(process.env.SNOOT_WEB_DIR){
  const root=resolve(process.env.SNOOT_WEB_DIR),origin=process.env.SNOOT_ORIGIN??'http://127.0.0.1:5175';
  await context.route(origin+'/**',async route=>{
   const path=new URL(route.request().url()).pathname;
   if(path==='/'||path.startsWith('/assets/')){
    const file=resolve(root,path==='/'?'index.html':'.'+path);
    if(file.startsWith(root+sep))return route.fulfill({path:file});
   }
   return route.continue();
  });
 }
 const page=await context.newPage(),errors=[];
 page.setDefaultTimeout(60000);
 page.on('pageerror',e=>errors.push(e.message));
 await page.goto(process.env.SNOOT_ORIGIN??'http://127.0.0.1:5175');
 const form=page.getByRole('form',{name:'Search Events'});
 const search=form.getByRole('button',{name:'Search',exact:true});
 const toggle=form.getByLabel('Tantivy Comparison');
 await form.getByLabel('Time Window',{exact:true}).selectOption('1440');
 await search.click();
 const results=page.locator('.paged-results');
 await results.locator('.events-table, .empty-state, .event-summary').first().waitFor();
 await page.waitForFunction(()=>document.querySelector('.paged-results')?.getAttribute('aria-busy')==='false');
 assert.equal(await results.locator('[role=alert]').count(),0);
 const activeQuery=await page.evaluate(()=>JSON.parse(sessionStorage.getItem('snoot:workspace')).query);
 assert.equal(activeQuery.engine,'local');
 const timing=form.locator('.search-performance');
 const originalTime=await timing.getAttribute('data-search-time-ms');
 await results.evaluate(node=>{window.__searchResults=node;});
 for(const checked of [true,false,true]){
  await toggle.setChecked(checked);
  assert.ok(await results.evaluate(node=>window.__searchResults===node),'Toggle must preserve the result component');
  assert.equal(await timing.getAttribute('data-search-time-ms'),originalTime);
  assert.deepEqual(await page.evaluate(()=>JSON.parse(sessionStorage.getItem('snoot:workspace')).query),activeQuery,'Toggle must not replace the active search');
 }
 await timing.getByRole('button').click();
 assert.equal(await timing.getAttribute('data-open'),'true');
 assert.ok(await timing.getByRole('tooltip').isVisible());
 assert.equal(await timing.locator('.performance-row').count(),4);
 assert.match(await timing.getByRole('tooltip').innerText(),/Time Trie Sync/);
 assert.doesNotMatch(await timing.getByRole('tooltip').innerText(),/Request & Wait/);
 await search.click();
 await page.waitForFunction(()=>document.querySelector('.paged-results')!==window.__searchResults&&document.querySelector('.paged-results')?.getAttribute('aria-busy')==='false'&&!!document.querySelector('.performance-trigger'));
 assert.equal(await page.evaluate(()=>JSON.parse(sessionStorage.getItem('snoot:workspace')).query.engine),'tantivy');
 assert.equal(await results.locator('[role=alert]').count(),0);
 // The comparison planner rejects logical predicates before returning a page.
 // This exercises a real failed-search path without changing the server/store.
 await form.getByRole('combobox',{name:'Search Query'}).fill('events | where duration > 1 OR duration == 0');
 await search.click();
 await form.getByRole('button',{name:/Search Failed Performance/}).waitFor();
 await form.getByRole('button',{name:/Search Failed Performance/}).click();
 assert.equal(await timing.locator('.performance-row').count(),0,'Missing measurements must not become zero-valued phases');
 assert.match(await timing.getByRole('tooltip').innerText(),/Phase timings were not returned/);
 assert.ok(Number(await timing.getAttribute('data-search-time-ms'))>0);
 assert.match(await results.locator('[role=alert]').innerText(),/Logical expressions require Local Search/);
 await page.setViewportSize({width:390,height:844});
 await form.getByRole('button',{name:/Search Failed Performance/}).click();
 const box=await timing.getByRole('tooltip').boundingBox();
 assert.ok(box&&box.x>=0&&box.x+box.width<=390,'Breakdown must stay inside the mobile viewport');
 assert.deepEqual(errors,[]);
 console.log(`${engine}: engine selection preserves results; explicit Search changes planner; success/failure timings and mobile breakdown pass.`);
}finally{
 await context.close();
 await rm(profile,{recursive:true,force:true});
}
