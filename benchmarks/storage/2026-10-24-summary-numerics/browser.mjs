import assert from 'node:assert/strict';
import {readFileSync,writeFileSync,mkdtempSync,rmSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {build} from '../../../client/node_modules/vite/dist/node/index.js';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const dir=fileURLToPath(new URL('.',import.meta.url));
const built=mkdtempSync(join(tmpdir(),'trunk-summary-browser-'));
await build({configFile:false,build:{outDir:built,emptyOutDir:true,lib:{entry:fileURLToPath(new URL('../../../client/services/pil/summaries.ts',import.meta.url)),formats:['es'],fileName:()=> 'summary.mjs'}}});
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH,headless:true});
try{
 const page=await browser.newPage();
 await page.route('http://summary.test/**',async route=>{
  const path=new URL(route.request().url()).pathname;
  if(path==='/')return route.fulfill({contentType:'text/html',body:'<!doctype html><title>Summary Codec Validation</title>'});
  const file=path==='/summary.mjs'?built+'/summary.mjs':dir+path;
  return route.fulfill({contentType:path.endsWith('.mjs')?'text/javascript':'application/octet-stream',body:readFileSync(file)});
 });
 await page.goto('http://summary.test');
 const results=await page.evaluate(async()=>{
  const {decodeSummaryDay}=await import('/summary.mjs'),results=[];
  for(const workload of ['sparse-day','demo-20','dense-240k','large-base-240k','varied-240k','full-u32-240k']){
   let expected;
   for(const mode of ['narrow','based','based-shuffle']){
    const bytes=new Uint8Array(await (await fetch(`/${workload}-${mode}.sumday`)).arrayBuffer());
    const times=[];let decoded;
    for(let n=0;n<12;n++){const start=performance.now();decoded=await decodeSummaryDay(bytes);if(n>1)times.push(performance.now()-start)}
    const totals=decoded.rollup.map(String).join(',');if(expected&&totals!==expected)throw Error('Rollup differs');expected=totals;
    times.sort((a,b)=>a-b);results.push({workload,mode,bytes:bytes.length,decodedBytes:decoded.bytes,medianMs:(times[4]+times[5])/2,p90Ms:times[8],totals});
   }
  }
  return results;
 });
 assert.equal(results.length,18);writeFileSync(dir+'/browser.json',JSON.stringify({browser:await browser.version(),results},null,2));console.log(JSON.stringify(results));
}finally{await browser.close();rmSync(built,{recursive:true,force:true})}
