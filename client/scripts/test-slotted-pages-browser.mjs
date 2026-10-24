import {readFile} from 'node:fs/promises';
import {mkdtemp,rm,writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
const pw=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const fixture=resolve(process.argv[2]),out=resolve(process.argv[3]),origin='http://localhost:9488';
for(const engine of ['chromium','webkit']){
  const profile=await mkdtemp(join(tmpdir(),'trunk-slotted-'));let context;
  const launch=async()=>{context=await pw[engine].launchPersistentContext(profile,{headless:true,...(engine==='chromium'?{executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'}:{})});
    await context.route(origin+'/**',async route=>{const path=new URL(route.request().url()).pathname;const body=path==='/pages.bin'||path==='/queries.json'?await readFile(join(fixture,path.slice(1))):path==='/worker.mjs'?await readFile(new URL('./test-slotted-pages-worker.mjs',import.meta.url)):'<!doctype html><title>Slotted Pages Test</title>';
      await route.fulfill({body,contentType:path.endsWith('.mjs')?'text/javascript':path.endsWith('.json')?'application/json':path.endsWith('.bin')?'application/octet-stream':'text/html'});});
    const page=await context.newPage();await page.goto(origin);return page;};
  const call=(page,action)=>page.evaluate(action=>new Promise((resolve,reject)=>{const w=new Worker('/worker.mjs',{type:'module'});w.onmessage=({data})=>{w.terminate();data.error?reject(Error(data.error)):resolve(data);};w.onerror=e=>reject(Error(e.message));w.postMessage({action});}),action);
  try{let page=await launch();const written=await call(page,'write');await context.close();context=undefined;page=await launch();const result=await call(page,'verify');const report={engine,version:context.browser().version(),written,result,reopened_context:true};await writeFile(join(out,engine+'.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report));}
  finally{await context?.close();await rm(profile,{recursive:true,force:true});}
}
