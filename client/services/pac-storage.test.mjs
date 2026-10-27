import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Codec} from './pil/wasm.ts';
import {PILStream} from './pil/stream.ts';
import {NativeStore,committedRanges} from './store/native.ts';
import {NativeEvent,NativeTrie} from './pil/native-event.ts';
import {compile} from './pil/metadata.ts';
import {Backend} from './native-backend-fixture.mjs';

const module=WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
const wire=readFileSync(new URL('./fixtures/pac-storage/hour.pil',import.meta.url));
async function receive(store,backend,bytes=wire,stride=137){
 const codec=new Codec(await WebAssembly.instantiate(await module)),defs=new Map(),session=store.begin(await store.serial());
 const stream=new PILStream(codec,defs,()=>{},()=>{},async()=>{throw Error('expanded event')},
   async record=>store.record(session,record,defs.get(record.binding.digest)));
 try{
  for(let at=0;at<bytes.length;at+=stride)await stream.feed(bytes.subarray(at,at+stride));
  stream.finish();await store.finish(session);
 }finally{stream.close();}
 return (await backend.list())[0];
}
test('sealed Go PACs persist native rows, shared prefixes and sparse member pages across restart',async()=>{
 const backend=new Backend(),store=new NativeStore(backend);
 const source=await receive(store,backend);
 assert.equal(committedRanges(source).index.reduce((n,r)=>n+r.End-r.Start,0),8);
 assert.equal(source.contexts.length,1);
 assert.equal(source.files.population.end,(1<<20)+8);
 assert.equal(source.files.index.end,(1<<20)+8);
 assert.ok(source.contexts.some(c=>c.common.length));
 const reopened=new NativeStore(backend);
 const page=await reopened.page(source.key,Date.parse('2026-10-19T12:00:00Z'),Date.parse('2026-10-19T13:00:00Z'));
 assert.equal(page.rows.length,8*16);
 const dependencies=await reopened.dependencies(source.key);
 const trie=new NativeTrie(compile(dependencies.definition,source.tap,[]));
 for(const chunk of dependencies.trie)trie.append(chunk.start,chunk.bytes);
 for(let i=0;i<8;i++){
  const event=new NativeEvent(page.rows.subarray(i*16,(i+1)*16),page.population.subarray(page.offsets[i*2],page.offsets[i*2+1]),trie,page.common[i],page.bases[i]);
  assert.equal(event.millis,Date.parse('2026-10-19T12:00:00Z')+i*60000);
 }
 const before=structuredClone(source.files);
 await receive(reopened,backend);
 assert.deepEqual((await backend.list())[0].files,before,'repeat collection allocates no additional pages');
});
test('a truncated hour never certifies persistent completeness; retry reuses staged pages',async()=>{
 const backend=new Backend(),store=new NativeStore(backend);
 await assert.rejects(receive(store,backend,wire.subarray(0,-1)),/complet|Truncated/i);
 assert.deepEqual(await store.coverage(),[]);
 const source=await receive(new NativeStore(backend),backend);
 assert.equal(committedRanges(source).index.reduce((n,r)=>n+r.End-r.Start,0),8);
 assert.equal(source.files.population.end,(1<<20)+8);
});

test('out-of-order whole hours share pages, fill gaps, and retire without rebinding rows',async()=>{

 const fixture=name=>readFileSync(new URL(`./fixtures/pac-storage/${name}.pil`,import.meta.url));
 const backend=new Backend(),store=new NativeStore(backend),at=Date.parse('2026-10-19T12:00:00Z'),hour=3600000;
 // Deliver the latest hour before the earliest; leave the middle hour absent.
 await receive(store,backend,fixture('gap-2'),65536);
 let source=await receive(store,backend,fixture('gap-0'),65536);
 assert.deepEqual(committedRanges(source).index,[{Start:1,End:3},{Start:7,End:9}]);
 assert.deepEqual(Object.keys(source.files.population.pages).map(Number).sort(),[0,1,3,4]);
 assert.equal(source.files.population.end,4*(1<<20)+8,'missing page 2 occupies no disk');
 const localPages=structuredClone(source.files.population.pages);
 const before=await store.page(source.key,at+2*hour,at+3*hour);
 assert.equal(before.rows.length,32);assert.ok(before.population.length>1_600_000);
 source=await receive(store,backend,fixture('gap-1'),65536);
 assert.deepEqual(committedRanges(source).index,[{Start:1,End:3},{Start:4,End:6},{Start:7,End:9}]);
 assert.equal(source.files.population.end,5*(1<<20)+8);
 for(const [page,physical]of Object.entries(localPages))assert.equal(source.files.population.pages[page],physical);
 assert.deepEqual(await store.page(source.key,at+2*hour,at+3*hour),before,'gap fill changed neighboring hour');
 const all=await store.page(source.key,at,at+3*hour);
 assert.equal(all.rows.length,96);
 for(let i=0;i<6;i++)assert.equal(all.population[all.offsets[i*2+1]-1],97+i,'cross-page member corrupted');
 const originalRows=before.rows.slice();
 source=await receive(store,backend,fixture('retired'),65536);
 assert.deepEqual(committedRanges(source).index,[{Start:7,End:9}]);assert.equal(source.retiredRows,6);
 assert.equal(source.files.index.retired,96);
 assert.deepEqual((await new NativeStore(backend).page(source.key,at+2*hour,at+3*hour)).rows,originalRows);
 assert.deepEqual(Object.keys(source.files.population.pages).map(Number).sort(),[3,4]);
 await assert.rejects(receive(store,backend,fixture('gap-0'),65536),/Stale PAC retirement/);
 assert.deepEqual(committedRanges(await backend.get(source.key)).index,[{Start:7,End:9}]);
 source=await receive(store,backend,fixture('after-retirement'),65536);
 assert.equal(source.retiredRows,9);assert.deepEqual(committedRanges(source).index,[{Start:10,End:11}]);
 const final=await store.page(source.key,at+72*hour,at+73*hour);
 assert.equal(final.rows.length,16);assert.equal(final.after,10);
 assert.ok(source.files.population.end<=5*(1<<20)+8,'retention failed to reuse storage');
});

test('quota failure during member-page allocation cannot certify an hour',async()=>{
 const backend=new Backend(),store=new NativeStore(backend),open=backend.file.bind(backend);let blocked=true;
 backend.file=async(...args)=>{
  const file=await open(...args);
  if(args[1]!=="population")return file;
  return new Proxy(file,{get(target,key){
   if(key==="truncate")return size=>{if(blocked&&size>8)throw new DOMException("Test quota exhausted","QuotaExceededError");target.truncate(size)};
   const value=target[key];return typeof value==="function"?value.bind(target):value;
  }});
 };
 await assert.rejects(receive(store,backend),e=>e.name==="QuotaExceededError");
 assert.deepEqual(await store.coverage(),[]);
 blocked=false;const source=await receive(new NativeStore(backend),backend);
 assert.deepEqual(committedRanges(source).index,[{Start:1,End:9}]);assert.equal(source.files.population.end,(1<<20)+8);
});

test('fixed 16-byte index segments cross sparse physical pages without ordinal translation',async()=>{
 const {pacHeader}=await import('./pil/pac.ts'),{nativePAC}=await import('./pil/pac-native.ts');
 const definitions=new Map(),records=[];
 const codec=new Codec(await WebAssembly.instantiate(await module));
 const parser=new PILStream(codec,new Map(),d=>definitions.set(d.digest,d),()=>{},async()=>{},async r=>records.push(r));
 await parser.feed(wire);parser.finish();parser.close();
 const base=Math.floor(3*(1<<20)/16)-1;
 for(const r of records){
  if(r.kind==='manifest'){
   r.manifest.Tail+=base;r.manifest.Index=r.manifest.Index.map(v=>({Start:v.Start+base,End:v.End+base}));
  }else if(r.kind==='chunk'&&r.file==='index')r.start+=base;
  else if(r.kind==='pac'){
   const header=pacHeader(r.bytes,0,64<<20,new Set([nativePAC]));
   const ext=header.extensions.find(e=>e.type===nativePAC).bytes;
   const view=new DataView(ext.buffer,ext.byteOffset,ext.byteLength);view.setBigUint64(72,view.getBigUint64(72,true)+BigInt(base),true);
  }
 }
 const backend=new Backend(),store=new NativeStore(backend),session=store.begin();
 for(const record of records)await store.record(session,record,definitions.get(record.binding.digest));
 await store.finish(session);
 const source=(await backend.list())[0];
 assert.deepEqual(Object.keys(source.files.index.pages).map(Number).sort(),[2,3]);
 assert.equal(source.files.index.end,2*(1<<20)+8,'unreceived index pages were allocated');
 const page=await new NativeStore(backend).page(source.key,Date.parse('2026-10-19T12:00:00Z'),Date.parse('2026-10-19T13:00:00Z'));
 assert.equal(page.rows.length,8*16);assert.equal(page.after,base+8);
 for(let i=0;i<8;i++)assert.equal(new DataView(page.rows.buffer).getUint32(i*16+8,false),i+1);
});
