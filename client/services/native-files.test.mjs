import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {nativeBackend} from './store/native-files.ts';
import {NativeStore,committedRanges} from './store/native.ts';
import {Codec} from './pil/wasm.ts';
import {PILStream} from './pil/stream.ts';
import {cbor} from './pil/cbor.ts';
class Directory{
 kind='directory';children=new Map();failCommit=false;
 constructor(name='root'){this.name=name;}
 async getDirectoryHandle(name,{create}={}){if(!this.children.has(name)){if(!create)throw new DOMException('missing','NotFoundError');this.children.set(name,new Directory(name));}return this.children.get(name);}
 async getFileHandle(name,{create}={}){
  if(!this.children.has(name)){
   if(!create)throw new DOMException('missing','NotFoundError');
   const file={name,kind:'file',data:new Uint8Array()};
   file.getFile=async()=>({arrayBuffer:async()=>file.data.slice().buffer});
   file.createWritable=async()=>{let pending;return {write:async b=>{pending=b.slice();},close:async()=>{if(name==='commit.cbor'&&this.failCommit){this.failCommit=false;throw Error('commit interrupted');}file.data=pending;},abort:async()=>{}};};
   file.createSyncAccessHandle=async()=>({getSize:()=>file.data.length,read:(out,{at})=>{const b=file.data.subarray(at,at+out.length);out.set(b);return b.length;},write:(b,{at})=>{if(at+b.length>file.data.length){const next=new Uint8Array(at+b.length);next.set(file.data);file.data=next;}file.data.set(b,at);return b.length;},truncate:n=>{const next=new Uint8Array(n);next.set(file.data.subarray(0,n));file.data=next;},flush(){},close(){}});
   this.children.set(name,file);
  }return this.children.get(name);
 }
 async removeEntry(name){if(!this.children.delete(name))throw new DOMException('missing','NotFoundError');}
 async *values(){yield* this.children.values();}
}
test('file-backed store reloads native events with no IndexedDB and publishes trie leaves atomically',async()=>{
 Object.defineProperty(globalThis,'indexedDB',{configurable:true,get(){throw Error('IndexedDB forbidden');}});
 const root=new Directory(),backend=nativeBackend(root),store=new NativeStore(backend),session=store.begin(),definitions=new Map();
 const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
 const stream=new PILStream(new Codec(await WebAssembly.instantiate(wasm)),definitions,()=>{},()=>{},async()=>{throw Error('expanded');},r=>store.record(session,r,definitions.get(r.binding.digest)));
 try{await stream.feed(new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/ranges.pil',import.meta.url))));stream.finish();}finally{stream.close();}
 const [pending]=await backend.list(),from=Date.UTC(2026,9,16,12),to=from+3600000;
 assert.deepEqual(committedRanges(pending),{index:[],sods:[]});
 root.failCommit=true;await assert.rejects(store.finish(session),/commit interrupted/);
 assert.deepEqual(committedRanges((await nativeBackend(root).list())[0]),{index:[],sods:[]});
 await store.finish(session);
 const reloaded=new NativeStore(nativeBackend(root)),[source]=await nativeBackend(root).list();
 const page=await reloaded.page(source.key,from,to);assert.equal(page.rows.length,96);assert.ok(page.population.length);
 assert.equal(source.contexts[0].placement.block,0);assert.equal(typeof source.contexts[0].placement.firstRow,'bigint');
 const branch=root.children.get('branches').children.get(source.key);
 const layout=[...branch.children.values()].find(f=>f.name.startsWith('layout.'));
 const metadata=cbor(layout.data);
 for(const key of ['timeTrie','packages','index','population'])assert.equal(metadata[key],undefined,key);
 assert.ok([...branch.children.keys()].some(n=>/^branch\.\d+\.tri$/.test(n)));
 assert.equal((await reloaded.coverage())[0].Index[0].Start,1,'wire coverage identifies event rows');
 delete globalThis.indexedDB;
});

test('a bounded collection publishes once; failed commit cannot expose leaves and retry recovers orphan bytes',async()=>{
 const {nativeTransaction}=await import('./store/transaction.ts');
 const records=[],definitions=new Map();
 const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
 const parser=new PILStream(new Codec(await WebAssembly.instantiate(wasm)),definitions,()=>{},()=>{},async()=>{},r=>{records.push(r);return Promise.resolve();});
 await parser.feed(new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/ranges.pil',import.meta.url))));parser.finish();parser.close();
 const root=new Directory();let commits=0;const open=root.getFileHandle.bind(root);
 root.getFileHandle=async(...args)=>{const file=await open(...args);if(args[0]==='commit.cbor'&&!file.counted){file.counted=true;const create=file.createWritable;file.createWritable=async()=>{const writer=await create(),close=writer.close;writer.close=async()=>{await close();commits++;};return writer;};}return file;};
 const receive=()=>nativeTransaction(nativeBackend(root),async backend=>{const store=new NativeStore(backend),session=store.begin();for(const r of records)await store.record(session,r,definitions.get(r.binding.digest));await store.finish(session);});
 root.failCommit=true;await assert.rejects(receive(),/commit interrupted/);
 assert.deepEqual(await nativeBackend(root).list(),[]);assert.equal(commits,0);
 await receive();assert.equal(commits,1,'one commit for the complete response, not one per control/PAC');
 const backend=nativeBackend(root),store=new NativeStore(backend),[source]=await backend.list(),from=Date.UTC(2026,9,16,12),to=from+3600000;
 const scope={tap:source.tap,digest:source.digest,generation:source.generation,from,to};
 assert.equal((await store.resolveHour(scope)).source.key,source.key);
 assert.equal(await store.resolveHour({...scope,from:to,to:to+3600000}),undefined);
 assert.equal(await store.resolveHour({...scope,generation:'0'.repeat(32)}),undefined);
 assert.equal((await store.page(source.key,from,to)).rows.length,96);
 await store.rebase(source.tap);assert.equal(await store.resolveHour(scope),undefined);
});

test('hourly data is flushed before trie publication; failed flush publishes nothing',async()=>{
 const {nativeTransaction}=await import('./store/transaction.ts');
 const {Backend}=await import('./native-backend-fixture.mjs');
 const records=[],definitions=new Map();
 const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
 const parser=new PILStream(new Codec(await WebAssembly.instantiate(wasm)),definitions,()=>{},()=>{},async()=>{},async r=>{records.push(r);});
 await parser.feed(new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/ranges.pil',import.meta.url))));parser.finish();parser.close();
 const backend=new Backend(),events=[],pending=new Set();let fail=true,opens=0;
 const open=backend.file.bind(backend),publish=backend.put.bind(backend);
 backend.file=async(...args)=>{
  opens++;const raw=await open(...args),id=args.join(':');
  return {getSize:()=>raw.getSize(),read:(...a)=>raw.read(...a),
   write:(b,o)=>{pending.add(id);events.push(['write',args[1],b.length]);return raw.write(b,o);},
   truncate:n=>{pending.add(id);raw.truncate(n);},
   flush:()=>{events.push(['flush',args[1]]);if(fail&&args[1]==='population')throw Error('flush interrupted');pending.delete(id);raw.flush();},close:()=>raw.close()};
 };
 backend.put=async(...args)=>{assert.equal(pending.size,0,'every data file must be durable before publishing any trie leaf');events.push(['publish']);return publish(...args);};
 const receive=()=>nativeTransaction(backend,async b=>{const store=new NativeStore(b),session=store.begin();for(const r of records)await store.record(session,r,definitions.get(r.binding.digest));await store.finish(session);});
 await assert.rejects(receive(),/flush interrupted/);assert.equal(backend.sources.size,0);assert.equal(events.some(e=>e[0]==='publish'),false);
 fail=false;events.length=0;opens=0;pending.clear();await receive();
 assert.equal(events.filter(e=>e[0]==='publish').length,1);
 assert.equal(events.filter(e=>e[0]==='flush').length,opens,'one flush per dirty file in this batch');
 assert.equal(events.at(-1)[0],'publish');
 const [source]=await backend.list();assert.ok(committedRanges(source).sods.length);
 const population=events.filter(e=>e[0]==='write'&&e[1]==='population'&&e[2]>8);
 assert.equal(population.length,records.filter(r=>r.kind==='pac').length,'one contiguous SODS header+payload write per hourly PAC');
});

test('many branches reuse a bounded handle pool and flush evicted files before publication',async()=>{
 const {nativeTransaction}=await import('./store/transaction.ts');
 let open=0,peak=0,flushes=0,published=false;
 const backend={async file(){peak=Math.max(peak,++open);let size=0;return {getSize:()=>size,read:()=>0,write:(b,{at})=>{size=Math.max(size,at+b.length);return b.length;},truncate:n=>{size=n;},flush:()=>{flushes++;},close:()=>{open--;}};},async put(){assert.equal(flushes,200);published=true;}};
 await nativeTransaction(backend,async b=>{for(let i=0;i<200;i++){const h=await b.file(String(i),'population');h.write(new Uint8Array(128),{at:0});assert.equal(h.getSize(),128);h.close();}await b.put([{key:'test'}]);});
 assert.equal(peak,64);assert.equal(open,0);assert.equal(published,true);
});
