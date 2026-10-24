import {readFile,writeFile,mkdir,mkdtemp,rm,readdir,stat} from 'node:fs/promises';
import {join,resolve} from 'node:path';
import {tmpdir} from 'node:os';
const pw=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const engine=process.env.SLOT_BROWSER??'chromium';
const output=resolve(process.argv[2]??'benchmarks/storage/2026-10-22-fixed-slots');
await mkdir(output,{recursive:true});
const cases=JSON.parse(await readFile(join(output,'cases.json'),'utf8'));
const workerSource=await readFile(new URL('./benchmark-fixed-slots-worker.mjs',import.meta.url),'utf8');
const origin='http://localhost:9481';let profile,context;
async function files(dir){const out=[];for(const e of await readdir(dir,{withFileTypes:true})){const p=join(dir,e.name);if(e.isDirectory())out.push(...await files(p));else if(e.isFile()){const s=await stat(p);out.push({path:p.slice(profile.length+1),logical:s.size,allocated:s.blocks*512})}}return out}
async function launch(){
 context=await pw[engine].launchPersistentContext(profile,{headless:true,...(engine==='chromium'?{executablePath:process.env.CHROME_PATH}:{} )});
 await context.route(origin+'/**',route=>route.fulfill({contentType:route.request().url().endsWith('.mjs')?'text/javascript':'text/html',body:route.request().url().endsWith('.mjs')?workerSource:'<!doctype html><title>Fixed Slot Allocation Probe</title>'}));
 const page=await context.newPage();if(process.env.SLOT_QUOTA){const cdp=await context.newCDPSession(page);await cdp.send('Storage.overrideQuotaForOrigin',{origin,quotaSize:Number(process.env.SLOT_QUOTA)})}await page.goto(origin);await page.evaluate(()=>globalThis.probe=new Worker('/probe.mjs',{type:'module'}));return page;
}
async function call(page,c,action){return page.evaluate(({c,action})=>new Promise((resolve,reject)=>{probe.onmessage=({data})=>data.error?reject(Error(data.error)):resolve(data.result);probe.onerror=e=>reject(Error(e.message));probe.postMessage({c,action})}),{c,action})}
const results=[];let version;
try{
 for(const c of cases){
  if(process.env.SLOT_CASE&&!c.name.includes(process.env.SLOT_CASE))continue;
  profile=await mkdtemp(join(tmpdir(),'trunk-fixed-opfs-'));let page=await launch();version=context.browser().version();
  let result;
  try{result=await call(page,c,'prepare')}catch(error){
   if(!process.env.SLOT_QUOTA)throw error;
   results.push({case:c,quota:Number(process.env.SLOT_QUOTA),error:String(error)});
   console.log(JSON.stringify(results.at(-1)));await context.close();context=undefined;await rm(profile,{recursive:true,force:true});profile=undefined;continue;
  }await context.close();context=undefined;
  result.disk=(await files(profile)).filter(f=>f.logical===result.logical);
  page=await launch();result.restart=await call(page,c,'verify');await call(page,c,'delete');await context.close();context=undefined;
  results.push(result);console.log(JSON.stringify(result));
  await rm(profile,{recursive:true,force:true});profile=undefined;
 }
}finally{
 await context?.close();if(profile)await rm(profile,{recursive:true,force:true});
 await writeFile(join(output,(process.env.SLOT_QUOTA?'quota-':'')+(process.env.SLOT_CASE??'')+engine+'.json'),JSON.stringify({engine,version,platform:process.platform,arch:process.arch,results},null,2)+'\n');
}
