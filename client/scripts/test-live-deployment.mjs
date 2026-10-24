// Read-only live demo smoke test. Uses an isolated browser profile, never user storage.
import assert from 'node:assert/strict';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH,headless:true});
const origin=process.env.SNOOT_ORIGIN??'http://127.0.0.1:3020';
let page;
try{
 const context=await browser.newContext({viewport:{width:1440,height:1000}});
 page=await context.newPage();page.setDefaultTimeout(90000);
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 assert.equal((await page.goto(origin)).status(),200);
 const form=page.getByRole('form',{name:'Search Events'});
 const input=page.getByRole('combobox',{name:'Search Query'});
 await form.getByLabel('Time Window',{exact:true}).selectOption('86400');
 const total=async()=>Number((await form.locator('[data-matched-count]').textContent()).replaceAll(',','').split(' ')[0]);
 const rows=()=>page.locator('.paged-results .event-summary');
 async function query(text){
  await input.fill(text);await form.getByRole('button',{name:'Search',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('.paged-results')?.getAttribute('aria-busy')==='false'&&document.querySelectorAll('.paged-results .event-summary').length>0,null,{timeout:90000});
  return total();
 }
 console.log('Checking all-event search');
 const count=await query('events.query().order(timestamp, desc).page(5)');
 assert.equal(count,Number(process.env.SNOOT_EXPECT_EVENTS??50000));
 console.log('All-event count:',count);
 const timing=form.locator('[data-search-time-kind=Search]');
 assert.ok(Number(await timing.getAttribute('data-search-time-ms'))>=0);
 await timing.getByRole('button').hover();
 await timing.getByRole('tooltip').waitFor({state:'visible'});
 assert.equal(await timing.locator('.performance-row').count(),5);
 assert.equal(await timing.locator('.performance-bar > span').count(),5);
 await page.screenshot({path:'/tmp/snoot-search-performance.png',fullPage:true});
 await form.getByRole('button',{name:'Clear',exact:true}).waitFor();
 const searchButton=await form.getByRole('button',{name:'Search',exact:true}).boundingBox();
 const clearButton=await form.getByRole('button',{name:'Clear',exact:true}).boundingBox();
 assert.ok(Math.abs(searchButton.y-clearButton.y)<3,'Clear and Search should share their row');
 const summaryPackets=await page.evaluate(async()=>{const cache=await caches.open('snoot-summary-days');return (await cache.keys()).length});
 assert.ok(summaryPackets>0,'completed daily summaries should be cached');
 console.log('Cached Daily Summaries:',summaryPackets);
 await page.screenshot({path:'/tmp/snoot-daily-summary-deployment.png',fullPage:true});
 const first=await rows().first().getAttribute('id'),initial=await rows().count();
 await rows().last().scrollIntoViewIfNeeded();
 await page.evaluate(()=>window.scrollTo(0,document.body.scrollHeight));
 await page.waitForFunction(n=>document.querySelectorAll('.paged-results .event-summary').length>n,initial,{timeout:90000});
 assert.equal(await total(),count,'paging must not narrow the summary');
 console.log('Checking reload');
 await page.reload();
 await page.waitForFunction(()=>document.querySelectorAll('.paged-results .event-summary').length>0);
 assert.equal(await rows().first().getAttribute('id'),first);
 assert.equal(await total(),count);
 await form.locator('[data-search-time-kind=Restore]').waitFor();
 console.log('Checking full-text search');
 const textMatches=await query('events.query().search("timeout", terms).page(5)');
 assert.ok(textMatches>0&&textMatches<count);
 console.log('Checking write search');
 const writes=await query('events.query().where(access, ==, write).where(level, ==, INFO).order(timestamp, desc).page(5)');
 assert.ok(writes>0);
 const link=page.locator('.paged-results .object-history-link').first();
 const object=await link.textContent();console.log('Checking Object History:',object);await link.click();
 const dialog=page.getByRole('dialog',{name:'Object History',exact:true});
 await dialog.waitFor();
 await page.waitForFunction(()=>document.querySelector('.history-modal [role="status"]')?.textContent==='Complete',null,{timeout:90000});
 const revisions=await dialog.locator('.history-entry').count();assert.ok(revisions>1);
 await dialog.getByRole('button',{name:'Close Object History',exact:true}).click();
 assert.deepEqual(errors,[]);
 await page.screenshot({path:'/tmp/snoot-live-deployment.png',fullPage:true});
 console.log(JSON.stringify({origin,events:count,paging:true,reload:true,textMatches,writes,object,revisions,errors}));
}catch(e){console.error(await page?.locator('body').innerText());throw e;}
finally{await browser.close();}
