import assert from 'node:assert/strict';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH,headless:true});let page;
try {
 const context=await browser.newContext({viewport:{width:390,height:844},isMobile:true,hasTouch:true,deviceScaleFactor:2});
 await context.addInitScript(()=>{
  // Headless browsers have no OS keyboard. Exercise the same viewport resize/
  // pan events used by on-screen keyboards without pretending this tests iOS.
  const v=window.visualViewport;let height,top=0;
  const realHeight=Object.getOwnPropertyDescriptor(VisualViewport.prototype,'height').get;
  Object.defineProperties(v,{height:{get:()=>height??realHeight.call(v)},offsetTop:{get:()=>top}});
  window.setQueryViewport=(h,t=0)=>{height=h;top=t;v.dispatchEvent(new Event('resize'));v.dispatchEvent(new Event('scroll'))};
 });
 page=await context.newPage();page.setDefaultTimeout(15000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(process.env.SNOOT_ORIGIN??'http://127.0.0.1:3020');
 const input=page.getByRole('combobox',{name:'Search Query'}),dock=page.getByRole('region',{name:'Search Suggestions'});
 await page.getByRole('button',{name:'+ Filter',exact:true}).waitFor({state:'visible'});
 await page.waitForFunction(()=>!document.querySelector('.query-actions button').disabled);
 await page.getByLabel('Time Window',{exact:true}).selectOption('43200');
 await input.tap();await dock.waitFor();
 assert.equal(await input.evaluate(e=>getComputedStyle(e).fontSize),'16px');
 assert.equal(await page.locator('.query-key-guide').isVisible(),false);
 await dock.getByRole('option',{name:'+ Filter where',exact:true}).tap();
 assert.equal(await input.inputValue(),'events.query()\n  .where(');
 assert.equal(await dock.getByRole('button',{name:'Search',exact:true}).isDisabled(),true);
 const choose=async name=>{const option=dock.getByRole('option',{name,exact:true});await option.scrollIntoViewIfNeeded();await option.tap();assert.equal(await input.evaluate(e=>document.activeElement===e),true,'tapping keeps keyboard focus')};
 await choose('level STRING');await choose('== Comparison');await choose('ERROR Mapped value');
 await choose('+ Filter where');await choose('access STRING');await choose('== Comparison');await choose('write Mapped value');
 assert.equal(await input.inputValue(),'events.query()\n  .where(level, ==, "ERROR")\n  .where(access, ==, write)');
 // A keyboard opening, viewport panning and landscape rotation keep the panel visible.
 for(const [height,top] of [[410,90],[300,25],[844,0]]){
  await page.evaluate(([h,t])=>window.setQueryViewport(h,t),[height,top]);
  await page.waitForFunction(({h,t})=>Math.abs(document.querySelector('.query-mobile-dock').getBoundingClientRect().bottom-(h+t))<2,{h:height,t:top});
  await page.waitForFunction(()=>document.querySelector('#snoot-query').getBoundingClientRect().bottom<=document.querySelector('.query-mobile-dock').getBoundingClientRect().top-4);
  const b=await dock.boundingBox();assert.ok(b.height<=height*.48+1);assert.ok(b.y>=top);assert.ok(b.width<=390);
 }
 await page.evaluate(()=>window.setQueryViewport(410));
 await page.waitForFunction(()=>Math.abs(document.querySelector('.query-mobile-dock').getBoundingClientRect().bottom-410)<2);
 await page.screenshot({path:'/tmp/snoot-mobile-suggestions.png'});
 await dock.getByRole('button',{name:'Hide Suggestions',exact:true}).tap();await dock.waitFor({state:'hidden'});
 assert.equal(await input.evaluate(e=>document.activeElement===e),true);
 await input.tap();await dock.waitFor();
 // IME composition must not be interrupted by a completion.
 await input.dispatchEvent('compositionstart');await dock.waitFor({state:'hidden'});
 await input.dispatchEvent('compositionend');await dock.waitFor();
 await page.evaluate(()=>window.setQueryViewport(undefined));
 // The mobile Search button uses the same form and closes suggestions explicitly.
 await dock.getByRole('button',{name:'Search',exact:true}).tap();await dock.waitFor({state:'hidden'});
 assert.equal(await input.evaluate(e=>document.activeElement===e),false);
 await page.waitForFunction(()=>document.querySelectorAll('.paged-results .event-summary').length>0);
 assert.deepEqual(errors,[]);
 console.log('Mobile query passed: touch-only field/operator/value flow, focus retention, dismissal/reopen, viewport resize/pan, IME and explicit submit. OS keyboard behavior still requires a real-device check.');
} catch(e){console.error(await page?.locator('body').innerText());throw e}finally{await browser.close()}
