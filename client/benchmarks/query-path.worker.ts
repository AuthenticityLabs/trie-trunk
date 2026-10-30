// End-to-end search benchmark: real HTTP, codec, storage worker and durable OPFS.
// Uses a private namespace; never clears application data.
import wasmURL from '../assets/pil.wasm?url';
import {Codec} from '../services/pil/wasm.ts';
import {readTrunk} from '../services/pil/trunk.ts';
import {ViewCache} from '../services/pil/view.ts';
import {storagePort} from '../services/store/client.ts';
self.onmessage=async({data})=>{
 const setupStarted=performance.now();
 const storage=new Worker(new URL('../services/store/worker.ts',import.meta.url),{type:'module'}),channel=new MessageChannel();
 const remote=storagePort(channel.port1),token='query-path-benchmark-'+data.variant;
 storage.postMessage({kind:'connect'},[channel.port2]);
 let commands:Record<string,{count:number;ms:number}>={},requests:{path:string;ms:number;bytes:number}[]=[];
 const archive:typeof remote=async(_,command)=>{const t=performance.now();try{return await remote(token,command)}finally{const c=commands[command.kind]??={count:0,ms:0};c.count++;c.ms+=performance.now()-t;}};
 const fetcher:typeof fetch=async(input,init)=>{const t=performance.now(),res=await fetch(input,init),r={path:String(input),ms:performance.now()-t,bytes:0};requests.push(r);
  if(!res.body)return res;return new Response(res.body.pipeThrough(new TransformStream({transform(chunk,controller){r.bytes+=chunk.byteLength;r.ms=performance.now()-t;controller.enqueue(chunk);}})),{status:res.status,statusText:res.statusText,headers:res.headers});};
 try{
  const codec=new Codec(await WebAssembly.instantiate(await WebAssembly.compileStreaming(fetch(wasmURL)))),definitions=new Map(),signal=new AbortController().signal;
  const trunk=await readTrunk({token:'',signal,codec,definitions,fetcher});
  const cache=new ViewCache(fetcher);cache.setTrunk(trunk);
  const setupMs=performance.now()-setupStarted,setupRequests=requests;
  const runs=[];
  for(const q of data.queries){for(let pass=0;pass<(data.repeats??3);pass++){
   commands={};requests=[];if(data.resetQueryCache)cache.resetEvents();let first:number|undefined;const t=performance.now();
   const result=await cache.query({...q,size:100},'',signal,codec,definitions,archive,false,undefined,()=>first??=performance.now()-t);
   const run={name:q.name,pass,ms:performance.now()-t,firstPageMs:first,timings:result.timings,matched:result.result.summary?.count,loaded:result.result.page.length,commands,requests};runs.push(run);self.postMessage({stage:'run',run});
  }}
  self.postMessage({result:{variant:data.variant,setupMs,setupRequests,branches:trunk.catalog.files.filter(f=>f.kind==='tri'&&!f.bootstrap).length,runs}});
 }catch(error){self.postMessage({error:String(error),stack:(error as Error).stack});}
 finally{remote.close?.();storage.terminate();}
};
