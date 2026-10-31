import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Codec} from './pil/wasm.ts';
import {PILStream} from './pil/stream.ts';
import {NativeStore} from './store/native.ts';
import {nativeTransaction} from './store/transaction.ts';
import {timeLeaf,TIME_SODS,TIME_INDEX} from './store/time-trie.ts';
import {loadTimeWindow,compactHour} from './pil/time-window.ts';
import {Backend} from './native-backend-fixture.mjs';
const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
async function setup(){
 const records=[],definitions=new Map(),codec=new Codec(await WebAssembly.instantiate(wasm));
 const stream=new PILStream(codec,new Map(),d=>definitions.set(d.digest,d),()=>{},async()=>{},async r=>records.push(r));
 try{await stream.feed(new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/ranges.pil',import.meta.url))));stream.finish()}finally{stream.close()}
 const manifest=records.find(r=>r.kind==='manifest'&&r.manifest.TimeTrie),m=manifest.manifest,b=manifest.binding,definition=definitions.get(b.digest);
 const view=new DataView(m.TimeTrie.buffer,m.TimeTrie.byteOffset,m.TimeTrie.byteLength),from=Number(view.getBigUint64(64,true)),to=Number(view.getBigUint64(72,true));
 const generation=Buffer.from(m.Generation).toString('hex'),sequence=m.GenerationSequence??1;
 const path=b.tap.slice(0,b.tap.lastIndexOf('/'))+'/0/'+b.tap.split('/').at(-1)+'.tri';
 const trunk={version:'',catalog:{files:[{path,kind:'tri',definition:b.digest,bootstrap:false,size:0}]},bindings:[b],definitions:[definition]};
 const backend=new Backend(),store=new NativeStore(backend);
 const archive=async(_token,c)=>c.kind==='time-read-many'?{times:await store.timeWindows(c.queries)}:await nativeTransaction(backend,x=>new NativeStore(x).importTime(c.input)).then(()=>({}));
 const input={tap:b.tap,definition,generation,sequence,serial:0,bytes:m.TimeTrie};
 const response=()=>new Response(m.TimeTrie,{headers:{'Trunk-Format':'time-trie/0','Trunk-Path':path,'Trunk-Source':b.tap,'Definition-Version':b.digest,'Trunk-Generation':generation,'Trunk-Generation-Sequence':String(sequence)}});
 return {records,definitions,backend,store,archive,input,response,query:{trunk,scope:'',from,to},from,to};
}
test('time replication reuses durable leaves without certifying events or indexes',async()=>{
 const f=await setup();let calls=0;
 const fetcher=async url=>{calls++;assert.equal(new URL(url,'http://test').pathname,'/demo/timelines');return f.response()};
 const signal=new AbortController().signal;
 const first=await loadTimeWindow(f.query,'',signal,f.archive,fetcher);
 assert.ok(first.count>0);assert.equal(calls,1);
 const [source]=await f.backend.list();assert.equal(timeLeaf(source.timeTrie,f.from).flags,0);
 assert.equal((await f.store.coverage()).length,0);
 const second=await loadTimeWindow(f.query,'',signal,f.archive,fetcher);
 const {receipts,...counts}=second;assert.deepEqual(counts,first);assert.equal(receipts.length,1);assert.equal(calls,1);
});
test('time receipt preserves already committed SODS and index bits',async()=>{
 const f=await setup(),session=f.store.begin();
 for(const r of f.records)await f.store.record(session,r,f.definitions.get(r.binding.digest));
 await f.store.finish(session);
 // Use the sequence advertised by the native fixture for this generation.
 const [prior]=await f.backend.list();f.input.sequence=prior.generationSequence??1;
 if(prior.generationSequence===undefined){prior.generationSequence=1;await f.backend.put([prior])}
 await nativeTransaction(f.backend,b=>new NativeStore(b).importTime(f.input));
 const [after]=await f.backend.list();assert.equal(timeLeaf(after.timeTrie,f.from).flags&(TIME_INDEX|TIME_SODS),TIME_INDEX|TIME_SODS);
});
test('failed publication and cache reset cannot publish time receipts',async()=>{
 const f=await setup();f.backend.fail=true;
 await assert.rejects(nativeTransaction(f.backend,b=>new NativeStore(b).importTime(f.input)),/crash/);
 assert.equal((await f.backend.list()).length,0);
 await f.store.rebaseMany([f.input.tap]);
 await assert.rejects(nativeTransaction(f.backend,b=>new NativeStore(b).importTime(f.input)),/rebased/);
});
test('mismatched identity, truncated body, and aborted responses leave no receipt',async()=>{
 for(const kind of ['identity','truncated','abort']){
  const f=await setup(),controller=new AbortController();
  const fetcher=async()=>{
   const res=f.response();if(kind==='identity')res.headers.set('Trunk-Source','wrong');
   if(kind==='truncated')return new Response(f.input.bytes.subarray(0,81),{headers:res.headers});
   if(kind==='abort')controller.abort();return res;
  };
  await assert.rejects(loadTimeWindow(f.query,'',controller.signal,f.archive,fetcher));
  assert.equal((await f.backend.list()).length,0);
 }
});
test('diagnostic branches never contribute to ordinary event counts',async()=>{
 const f=await setup(),file=f.query.trunk.catalog.files[0],b=f.query.trunk.bindings[0];
 f.query.trunk.catalog.files.push({...file,path:file.path.replace(/write\.tri$/,'debug.tri')});
 f.query.trunk.bindings.push({...b,tap:b.tap.replace(/write$/,'debug')});
 let calls=0;await loadTimeWindow(f.query,'',new AbortController().signal,f.archive,async()=>{calls++;return f.response()});assert.equal(calls,1);
});
test('compact UTC addressing validates leap years and hour boundaries',()=>{
 assert.equal(compactHour(Date.UTC(2024,1,29,23)),'2406023');
 assert.equal(compactHour(Date.UTC(2025,0,1)),'2500100');
 assert.throws(()=>compactHour(Date.UTC(2025,0,1)+1));assert.throws(()=>compactHour(Date.UTC(2100,0,1)));
});

test('a time-only source accepts later full PAC collection without losing its leaves',async()=>{
 const f=await setup();await nativeTransaction(f.backend,b=>new NativeStore(b).importTime(f.input));
 const session=f.store.begin();
 for(const r of f.records){if(r.kind==='manifest')r.manifest.GenerationSequence=f.input.sequence;await f.store.record(session,r,f.definitions.get(r.binding.digest))}
 await f.store.finish(session);
 const [s]=await f.backend.list();assert.equal(timeLeaf(s.timeTrie,f.from).flags&(TIME_INDEX|TIME_SODS),TIME_INDEX|TIME_SODS);
 assert.equal((await f.store.coverage()).length,1);
});
test('older and forked physical generations cannot overwrite received counts',async()=>{
 const f=await setup();f.input.sequence=3;await nativeTransaction(f.backend,b=>new NativeStore(b).importTime(f.input));
 const before=await f.backend.list();
 for(const sequence of [2,3]){
  const generation='ff'+f.input.generation.slice(2),bytes=f.input.bytes.slice();bytes[48]=255;
  await assert.rejects(nativeTransaction(f.backend,b=>new NativeStore(b).importTime({...f.input,generation,sequence,bytes})),/generation/);
  assert.deepEqual(await f.backend.list(),before);
 }
});
