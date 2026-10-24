import {build} from 'vite';
import {createServer} from 'node:http';
import {readFile,writeFile,mkdtemp,rm} from 'node:fs/promises';
import {join,resolve} from 'node:path';
import {tmpdir} from 'node:os';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE??'playwright-core');
const input=process.argv[2];if(!input)throw Error('Provide measurement directory');
const out=await mkdtemp(join(tmpdir(),'pil-compression-browser-'));let server,browser;
try{
 await build({configFile:false,logLevel:'warn',build:{outDir:out,emptyOutDir:true,lib:{entry:resolve('scripts/benchmark-compression-worker.ts'),formats:['es'],fileName:()=> 'worker.js'},minify:false}});
 const measurements=JSON.parse(await readFile(join(input,'server.json'),'utf8')).filter(r=>r.Round===0).map(r=>({...r,file:`${r.Profile}-${r.Count}-${r.Mode}.pil`}));
 server=createServer(async(req,res)=>{try{
  if(req.url==='/'){res.setHeader('Content-Type','text/html');res.end('<!doctype html><title>PIL compression benchmark</title>');return;}
  const path=req.url==='/worker.js'?join(out,'worker.js'):req.url==='/pil.wasm'?resolve('assets/pil.wasm'):req.url.startsWith('/fixture/')&&measurements.some(m=>'/fixture/'+m.file===req.url)?join(input,req.url.slice(9)):null;
  if(!path){res.writeHead(404);res.end();return;}
  res.setHeader('Content-Type',req.url==='/worker.js'?'text/javascript':req.url==='/pil.wasm'?'application/wasm':'application/octet-stream');res.end(await readFile(path));
 }catch(e){res.writeHead(500);res.end(String(e))}});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 browser=await chromium.launch({executablePath:process.env.CHROME_PATH,headless:true});const page=await browser.newPage();await page.goto('http://127.0.0.1:'+server.address().port);
 const response=await page.evaluate(items=>new Promise((resolve,reject)=>{const worker=new Worker('/worker.js',{type:'module'});worker.onmessage=({data})=>{worker.terminate();resolve(data)};worker.onerror=e=>reject(Error(e.message));worker.postMessage(items)}),measurements);
 if(response.error)throw Error(response.error);
 const hashes=new Map();for(const r of response.results){const key=r.Profile+'/'+r.Count;if(hashes.has(key)&&hashes.get(key)!==r.hash)throw Error('Raw/compressed disk output differs');hashes.set(key,r.hash);}
 await writeFile(join(input,'browser.json'),JSON.stringify({browser:await browser.version(),results:response.results},null,2));
 console.log(JSON.stringify({browser:await browser.version(),cases:response.results.length,verified:'identical raw/compressed OPFS contents'}));
}finally{await browser?.close();await new Promise(r=>server?server.close(r):r());await rm(out,{recursive:true,force:true});}
