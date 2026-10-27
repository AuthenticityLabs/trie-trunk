// Isolated browser fixture: no live service or user profile changes.
import assert from 'node:assert/strict';
import {gzipSync} from 'node:zlib';
import { readFile,mkdtemp,rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'vite';
import { bytes,frame,magic,le } from '../services/pil-fixtures.mjs';
import { Codec } from '../services/pil/wasm.ts';
import { PILStream } from '../services/pil/stream.ts';
const playwright=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const engine=process.env.PAC_BROWSER??'chromium';
const driver=playwright[engine];if(!driver)throw Error('Unknown test browser');
const root=fileURLToPath(new URL('../../',import.meta.url)),output=await mkdtemp(join(tmpdir(),'snoot-pac-test-'));
const cold=new Uint8Array(await readFile(root+'client/services/fixtures/pac-view/cold.pil'));
const warm=await readFile(root+'client/services/fixtures/pac-view/warm.pil'),next=await readFile(root+'client/services/fixtures/pac-view/next.pil');
const compressed = await Promise.all(['cold','warm','next'].map(n=>readFile(root+'client/services/fixtures/pac-view/'+n+'.pil.gz')));
const spliced=await readFile(root+'../pill-packer/rust/tests/fixtures/pac/spliced.gz');
const packedPAC=await readFile(root+'../pill-packer/rust/tests/fixtures/pac/packed.pac'),splicedPAC=await readFile(root+'../pill-packer/rust/tests/fixtures/pac/spliced-pacs.gz');
let metadata,binding,view;
const wasm=await WebAssembly.compile(await readFile(root+'client/assets/pil.wasm'));
const parser=new PILStream(new Codec(await WebAssembly.instantiate(wasm)),new Map(),d=>metadata=d,b=>binding=b,async()=>{},undefined,v=>view=v);
await parser.feed(cold);parser.finish();parser.close();
const hash=Uint8Array.from(Buffer.from(metadata.digest,'hex'));
const schemas=bytes(magic,[1],frame([3,3,3],[hash,metadata.store,metadata.pil]),[2],frame([2,3,3],[le(binding.stream,4),hash,new TextEncoder().encode(binding.tap)]),[255,254]);
const calls=[],issues=[];let server,browser;let failNext=false;
try{
 await build({root:root+'client',configFile:root+'client/vite.config.ts',logLevel:'warn',build:{outDir:output,emptyOutDir:true}});
 server=createServer(async(req,res)=>{
  const url=new URL(req.url,'http://localhost');
  const headers={'Content-Type':'application/pil','PIL-Version':'2','Definition-Version':metadata.digest,'Trunk-Completion':'body','Trunk-Features':'native-ranges,pac-arrays'};
  if(url.pathname==='/gzip-splice-pacs'){res.writeHead(200,{'Content-Encoding':'gzip'});res.end(splicedPAC);return}
  if(url.pathname==='/gzip-splice'){res.writeHead(200,{'Content-Encoding':'gzip'});res.end(spliced);return}
  if(url.pathname==='/gzip-probe'){res.writeHead(200,{'Content-Encoding':'gzip'});res.end(Buffer.concat([gzipSync(Buffer.from('first')),gzipSync(Buffer.from('second'))]));return}
  if(url.pathname==='/caches'){res.setHeader('Content-Type','application/json');res.end(JSON.stringify({caches:[binding.tap]}));return}
  if(url.pathname==='/schemas'){res.writeHead(200,headers);res.end(schemas);return}
  if(url.pathname==='/events'){
   calls.push({url,headers:req.headers});
   if(failNext&&url.searchParams.has('after')){failNext=false;res.writeHead(503);res.end('Temporary fixture failure');return;}
   res.writeHead(200,{...headers,'Trunk-Format':'pac-view/0','Content-Encoding':'gzip'});
   const body=url.searchParams.has('after')&&url.searchParams.get('after')!==view.cursor?compressed[2]:req.headers['event-known']?compressed[1]:compressed[0];
   // Actual Go response: independently compressed PACs spliced without inflation.
   res.end(body);return;
  }
  if(url.pathname==='/collect'){issues.push('paged view unexpectedly collected entire range');res.writeHead(500);res.end();return}
  try{const path=url.pathname==='/'?'/index.html':url.pathname;if(path.includes('..'))throw Error('path');let body=await readFile(output+path);if(path==='/index.html')body=Buffer.from(body.toString().replace('</head>','<style>.event-summary { height:600px; } [data-paged-dashboard] { min-height:1200px; }</style></head>'));res.setHeader('Content-Type',path.endsWith('.js')?'text/javascript':path.endsWith('.css')?'text/css':path.endsWith('.html')?'text/html':path.endsWith('.wasm')?'application/wasm':'image/png');res.end(body)}catch{res.writeHead(404);res.end()}
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 browser=await driver.launch({executablePath:engine==='chromium'?process.env.CHROME_PATH:process.env.WEBKIT_PATH,headless:true});const page=await browser.newPage({viewport:{width:1440,height:1000}});page.setDefaultTimeout(15000);page.on('pageerror',e=>issues.push(e.message));
 await page.clock.setFixedTime(new Date('2026-09-01T14:00:00Z'));
 await page.goto(`http://127.0.0.1:${server.address().port}`);
 assert.equal(await page.evaluate(async()=>await (await fetch('/gzip-splice')).text()),'firstsecond');
 const decoded=await page.evaluate(async()=>Array.from(new Uint8Array(await (await fetch('/gzip-splice-pacs')).arrayBuffer())));
 assert.deepEqual(Buffer.from(decoded),Buffer.concat([packedPAC,packedPAC]));
 console.log('Spliced gzip: firstsecond and two complete PACs; Browser:',engine,browser.version(),'concatenated gzip probe:',await page.evaluate(async()=>await (await fetch('/gzip-probe')).text()));
 await page.addStyleTag({content:'.event-summary { height: 600px; }'});
 if(process.env.PAC_TRANSPORT_ONLY!=='1'){
 const form=page.getByRole('form',{name:'Search Events'});
 assert.equal(await page.getByText('Collect entire range',{exact:true}).count(),0);
 await form.getByLabel('Time Window',{exact:true}).selectOption('custom');
 await form.getByRole('button',{name:'Search Time Window',exact:true}).click();
 await page.getByRole('textbox',{name:'Calendar from date',exact:true}).fill('2026-07-03');
 await page.getByRole('textbox',{name:'Calendar from date',exact:true}).press('Tab');
 await page.waitForFunction(()=>document.querySelector('[data-matched-count]')?.textContent?.includes('7 matched')).catch(async e=>{console.error({calls:calls.length,issues,body:await page.locator("body").innerText()});throw e});
 assert.equal(calls[0].url.searchParams.get('from'),'2026-07-03T00:00:00.000Z');
 assert.equal(calls.length,1);assert.equal(await page.locator('.paged-results .event-summary').count(),2);
 assert.match(await form.locator('[data-collected-count]').textContent(),/2 loaded/);
 assert.equal(await page.locator('.pagination').count(),0);
 assert.equal(await page.getByRole('combobox',{name:'Events per page'}).count(),0);
 const first=await page.locator('.event-summary').first().getAttribute('id');
 await page.locator('.event-summary').first().getByRole('button',{name:/Inspect event/}).click();
 failNext=true;
 await page.locator('.paged-results .table-scroll').evaluate(el=>el.scrollTop=el.scrollHeight);
 await page.getByRole('alert').waitFor();
 assert.equal(await page.locator('.event-summary').count(),2,'failed continuation retains rows');
 assert.equal(calls.length,2,'failed continuation does not retry endlessly');
 await page.getByRole('button',{name:'Retry',exact:true}).click();
 await page.waitForFunction(()=>document.querySelectorAll('.paged-results .event-summary').length===4);
 assert.equal(calls.length,3);assert.equal(calls[1].url.searchParams.get('after'),calls[2].url.searchParams.get('after'));
 assert.equal(calls[2].headers['summary-known'],view.snapshot);
 assert.equal(await page.locator('.event-summary').first().getAttribute('id'),first);
 assert.equal(await page.locator('.event-summary').first().getByRole('button',{name:/Inspect event/}).getAttribute('aria-expanded'),'true');
 assert.match(await form.locator('[data-collected-count]').textContent(),/4 loaded/);
 const nav=page.getByRole('navigation');
 assert.match(await nav.getByRole('button',{name:/Search/}).textContent(),/7/);
 await page.evaluate(()=>{window.scrollTo(0,160);window.dispatchEvent(new Event('pagehide'))});
 const searchScroll=await page.evaluate(()=>window.scrollY);assert.ok(searchScroll>0);
 await nav.getByRole('button',{name:'Dashboards',exact:true}).click();
 assert.equal(await page.locator('[data-dashboard-matched]').textContent(),'7');
 assert.match(await page.locator('[data-paged-dashboard]').textContent(),/Loaded Events4/);
 await page.waitForFunction(()=>window.scrollY===0);
 await page.evaluate(()=>{window.scrollTo(0,80);window.dispatchEvent(new Event('pagehide'))});
 const dashboardScroll=await page.evaluate(()=>window.scrollY);assert.ok(dashboardScroll>0);
 await page.reload();
 await page.locator('[data-dashboard-matched]').waitFor();
 await page.waitForFunction(top=>window.scrollY===top,dashboardScroll).catch(async error=>{console.error('dashboard scroll',dashboardScroll,await page.evaluate(()=>({y:scrollY,tab:sessionStorage.getItem('snoot:workspace')})));throw error});
 await nav.getByRole('button',{name:/Search/}).click();
 await page.waitForFunction(top=>window.scrollY===top,searchScroll).catch(async error=>{console.error('search scroll',searchScroll,await page.evaluate(()=>({y:scrollY,tab:sessionStorage.getItem('snoot:workspace')})));throw error});
 assert.equal(await page.locator('.event-summary').count(),4);
 assert.equal(calls.length,3,'navigation must retain loaded rows without another request');
 // Reload restores query settings and refetches the first page, never base64 event pages.
 await page.evaluate(()=>window.dispatchEvent(new Event('pagehide')));
 const checkpoint=await page.evaluate(()=>JSON.parse(sessionStorage.getItem('snoot:search:workspace')));
 assert.equal(checkpoint.pages,undefined);assert.equal(checkpoint.result,undefined);
 const beforeReload=calls.length;
 await page.reload();
 await page.waitForFunction(()=>document.querySelectorAll('.paged-results .event-summary').length===2);
 assert.equal(calls.length,beforeReload+1);
 assert.deepEqual(await page.evaluate(async()=>await indexedDB.databases()),[{name:'snoot-profile',version:1}]);
 assert.equal(await page.getByRole('alert').count(),0);
 await page.screenshot({path:'/private/tmp/snoot-pac-preview.png',fullPage:true});
 assert.deepEqual(issues,[]);console.log('PAC browser passed: scroll appends rows, retains expansion and summaries, retries continuation, refresh reuses cache; no full collect');
 }else console.log('PAC transport passed: browser decoded two precompressed PACs byte for byte; UI/OPFS not tested');
}finally{await browser?.close();if(server)await new Promise(r=>server.close(r));await rm(output,{recursive:true,force:true})}
