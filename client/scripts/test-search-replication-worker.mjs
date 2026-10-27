// Serve alongside /search/search.js and /fixture/fixture.json. Runs in a
// dedicated browser worker; no screenshots, UI automation, or Node runtime.
import init,{SearchIndex} from '/search/search.js';
const hex=bytes=>Array.from(new Uint8Array(bytes),b=>b.toString(16).padStart(2,'0')).join('');
const digest=async bytes=>hex(await crypto.subtle.digest('SHA-256',bytes));
const assert=(v,m)=>{if(!v)throw Error(m);};
const percentile=(values,p)=>[...values].sort((a,b)=>a-b)[Math.min(values.length-1,Math.floor(values.length*p))];
self.onmessage=async()=>{
 let root,folder,step="initialization";
 try{
  const started=performance.now(),wasm=await init();
  const initMs=performance.now()-started;
  const fixture=await (await fetch('/fixture/fixture.json')).json();
  root=await navigator.storage.getDirectory();folder=`search-evaluation-${crypto.randomUUID()}`;
  const dir=await root.getDirectoryHandle(folder,{create:true});
  const metrics=[];let indexBytes=0,peakWasmBytes=0,checks=0,negativeChecks=0;
  for(const hour of fixture.hours){
   const handles=new Map();let index;
   try{
    const specs=[];
    for(const file of hour.files){
     const response=await fetch(`/fixture/${hour.hour}/${file.name}`);
     assert(response.ok,'Missing index file');const bytes=await response.arrayBuffer();
     assert(bytes.byteLength===file.len&&await digest(bytes)===file.sha256,'Invalid index checksum');
     indexBytes+=bytes.byteLength;
     const handle=await (await dir.getFileHandle(`${hour.hour}-${file.id}`,{create:true})).createSyncAccessHandle();
     handles.set(file.id,handle);
     assert(handle.write(bytes)===bytes.byteLength,'Short index write');handle.flush();
     specs.push({id:file.id,name:file.name,len:file.len});
    }
    let reads=0,readBytes=0;
    const opened=performance.now();
    index=new SearchIndex(JSON.stringify(specs),(id,at,bytes)=>{
     reads++;readBytes+=bytes.byteLength;return handles.get(id).read(bytes,{at});
    });
    const openMs=performance.now()-opened,openReads=reads,openBytes=readBytes;
    assert(index.documents()===BigInt(hour.rows),'Wrong replicated document count');
    for(const check of hour.checks){
     step=`hour ${hour.hour}: ${check.name}`;
     const json=JSON.stringify(check.query);const times=[];let bytes=0,calls=0;
     for(let i=0;i<3;i++){
      const beforeBytes=readBytes,beforeCalls=reads,t=performance.now();
      const rows=index.search(json);times.push(performance.now()-t);
      bytes+=readBytes-beforeBytes;calls+=reads-beforeCalls;
      assert(rows.length===check.count,`${check.name}: count differs`);
      assert(await digest(rows.buffer)===check.sha256,`${check.name}: rows differ`);checks++;
     }
     metrics.push({hour:hour.hour,name:check.name,nativeMs:check.nativeMs,firstMs:times[0],warmMs:percentile(times.slice(1),.5),readCalls:calls/3,readBytes:bytes/3,openMs,openReads,openBytes});
    }
    if(hour.hour===0){
     const rejects=fn=>{let rejected=false;try{fn();}catch{rejected=true;}assert(rejected,'Expected reader rejection');negativeChecks++;};
     rejects(()=>index.search('{"ranges":[[2,1]]}'));
     rejects(()=>index.search('{"ranges":[[0,10]],"invented":true}'));
     assert(index.search('{"ranges":[]}').length===0,'Missing range must not search every event');negativeChecks++;
     rejects(()=>new SearchIndex(JSON.stringify(specs),()=>0));
     rejects(()=>new SearchIndex(JSON.stringify([{id:0,name:'../meta.json',len:2}]),()=>0));
    }
    peakWasmBytes=Math.max(peakWasmBytes,wasm.memory.buffer.byteLength);
   }finally{try{index?.free();}catch(e){console.error("Reader release failed",String(e));}for(const handle of handles.values())handle.close();}
  }
  const windowQueries=Object.entries(Object.groupBy(metrics,m=>m.name)).map(([name,rows])=>({name,firstMs:rows.reduce((s,r)=>s+r.firstMs,0),warmMs:rows.reduce((s,r)=>s+r.warmMs,0)}));
  self.postMessage({ok:true,windowQueries,hours:fixture.hours.length,events:fixture.hours.reduce((n,h)=>n+h.rows,0),checks,negativeChecks,initMs,indexBytes,peakWasmBytes,queryFirstMedianMs:percentile(metrics.map(m=>m.firstMs),.5),queryWarmMedianMs:percentile(metrics.map(m=>m.warmMs),.5),queryWarmP95Ms:percentile(metrics.map(m=>m.warmMs),.95),metrics});
 }catch(e){self.postMessage({ok:false,error:String(e),step,stack:e?.stack});}
 finally{if(root&&folder)await root.removeEntry(folder,{recursive:true});}
};
