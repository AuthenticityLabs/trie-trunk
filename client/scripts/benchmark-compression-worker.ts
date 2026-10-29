import {Codec} from '../services/pil/wasm.ts';
import {PILStream, type Batch} from '../services/pil/stream.ts';

// Isolated dedicated-worker benchmark of the actual decoder and OPFS APIs.
// Production uses a SharedWorker plus its storage worker; this deliberately
// excludes tab messaging, UI rendering and wide-area network latency.
self.onmessage=async({data})=>{
 try{
  const module=await WebAssembly.compile(await (await fetch('/pil.wasm')).arrayBuffer());
  const root=await navigator.storage.getDirectory();
  const results=[];
  for(const item of data){
   const bytes=new Uint8Array(await (await fetch('/fixture/'+item.file)).arrayBuffer());
   for(let round=0;round<5;round++){
    const file=await root.getFileHandle('compression-benchmark.sods',{create:true});
    const disk=await (file as any).createSyncAccessHandle();disk.truncate(0);
    const definitions=new Map();let count=0,stored=0,diskMS=0;
    const c=new Codec(await WebAssembly.instantiate(module));
    const stream=new PILStream(c,definitions,()=>{},()=>{},async(batch:Batch)=>{
     const start=performance.now();
     let size=0;for(let i=0;i<batch.rows.length;i+=3)size+=batch.rows[i+2]-batch.rows[i+1];
     const packed=new Uint8Array(size);let at=0;
     for(let i=0;i<batch.rows.length;i+=3){const raw=new Uint8Array(batch.bytes,batch.rows[i+1],batch.rows[i+2]-batch.rows[i+1]);packed.set(raw,at);at+=raw.length;count++;}
     disk.write(packed,{at:stored});stored+=packed.length;
     disk.flush();diskMS+=performance.now()-start;
    });
    const start=performance.now();
    try{for(let i=0;i<bytes.length;i+=65536)await stream.feed(bytes.subarray(i,i+65536));stream.finish();}finally{stream.close();}
    const elapsed=performance.now()-start;
    const saved=new Uint8Array(stored);disk.read(saved,{at:0});disk.close();
    if(count!==item.Count||stored!==item.Raw)throw Error('Wrong persisted record count/bytes');
    const hash=[...new Uint8Array(await crypto.subtle.digest('SHA-256',saved))].map(x=>x.toString(16).padStart(2,'0')).join('');
    results.push({...item,round,elapsed,diskMS,stored,hash});
   }
  }
  await root.removeEntry('compression-benchmark.sods');
  self.postMessage({results});
 }catch(e){self.postMessage({error:String(e)})}
};
