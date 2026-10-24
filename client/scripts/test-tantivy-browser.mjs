import assert from 'node:assert/strict';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH,headless:true});
try{
 const page=await browser.newPage({viewport:{width:1440,height:1000}});page.setDefaultTimeout(60000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(process.env.SNOOT_ORIGIN??'http://127.0.0.1:3020');
 const form=page.getByRole('form',{name:'Search Events'}),input=page.getByRole('combobox',{name:'Search Query'});
 await form.getByRole('button',{name:'Cache: All Caches',exact:true}).click();
 for(const [label,value] of [['Cache','demo'],['Service','payment'],['Resource','transactions.v1']]){const scope=page.getByRole('combobox',{name:`Find ${label}`,exact:true});await scope.fill(value);await scope.press('Tab')}
 await form.getByLabel('Time Window',{exact:true}).selectOption('43200');
 const count=async()=>Number((await form.locator('[data-matched-count]').textContent()).replaceAll(',','').split(' ')[0]);
 async function search(text){await input.fill(text);await form.getByRole('button',{name:'Search',exact:true}).click();await page.waitForFunction(()=>document.querySelector('.paged-results')?.getAttribute('aria-busy')==='false');return count()}
 const totals={};
 totals.terms=await search('events.query().search("authorized", terms).page(5)');assert.ok(totals.terms>5,'mapped dictionary text is indexed');
 const beforeRows=await page.locator('.paged-results .event-summary').count();assert.ok(beforeRows>0&&beforeRows<totals.terms);
 // Scroll triggers the next page; summaries must retain the full match count.
 await page.locator('.paged-results .event-summary').last().scrollIntoViewIfNeeded();await page.evaluate(()=>window.scrollTo(0,document.body.scrollHeight));
 await page.waitForFunction(n=>document.querySelectorAll('.paged-results .event-summary').length>n,beforeRows);assert.equal(await count(),totals.terms);
 totals.phrase=await search('events query | search "authorized" mode phrase | page 5');assert.equal(totals.phrase,totals.terms);
 totals.prefix=await search('events query | search "authoriz" mode prefix | page 5');assert.ok(totals.prefix>=totals.terms);
 totals.fuzzy=await search('events query | search "authorzed" mode fuzzy | page 5');assert.ok(totals.fuzzy>=totals.terms);
 const compound=await search('events query | where duration > 50000 | search "authorized" mode terms | order timestamp desc | page 5');assert.ok(compound>0&&compound<totals.terms);
 await form.getByRole('button',{name:'Text Mode',exact:true}).click();await page.getByRole('option',{name:'phrase Search Mode',exact:true}).click();assert.match(await input.inputValue(),/mode phrase/);
 await form.getByRole('button',{name:'Builder',exact:true}).click();assert.match(await input.inputValue(),/search\("authorized", phrase\)/);
 await page.screenshot({path:'/tmp/snoot-tantivy-search.png',fullPage:true});
 assert.deepEqual(errors,[]);console.log('Tantivy browser search passed:',JSON.stringify({...totals,compound}));
 // The same mode choices are usable without a keyboard on a narrow touch viewport.
 const mobile=await browser.newPage({viewport:{width:390,height:844},isMobile:true,hasTouch:true});await mobile.goto(process.env.SNOOT_ORIGIN??'http://127.0.0.1:3020');
 const mi=mobile.getByRole('combobox',{name:'Search Query'});await mi.fill('events.query().search("timeout", ');await mi.focus();await mobile.getByRole('option',{name:'terms Search Mode',exact:true}).click();assert.equal(await mi.inputValue(),'events.query().search("timeout", terms)');
 console.log('Touch mode autocomplete passed.');
} finally {await browser.close()}
