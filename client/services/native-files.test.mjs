import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {nativeBackend,NativeSnapshotCache,NativeSnapshotPool} from './store/native-files.ts';
import {NativeStore,committedRanges} from './store/native.ts';
import {Codec} from './pil/wasm.ts';
import {PILStream} from './pil/stream.ts';
import {decodeObject} from './pil/control.ts';
import {readTrunk} from './pil/trunk.ts';
import {saveTrunk,trunkDefinitions} from './store/trunk.ts';
class Directory{
 kind='directory';children=new Map();failCommit=false;commitName='commit.pil';events=[];
 constructor(name='root'){this.name=name;}
 async getDirectoryHandle(name,{create}={}){if(!this.children.has(name)){if(!create)throw new DOMException('missing','NotFoundError');this.children.set(name,new Directory(name));}return this.children.get(name);}
 async getFileHandle(name,{create}={}){
  if(!this.children.has(name)){
   if(!create)throw new DOMException('missing','NotFoundError');
   const file={name,kind:'file',data:new Uint8Array()};
   file.getFile=async()=>({arrayBuffer:async()=>file.data.slice().buffer});
   file.createWritable=async()=>{
    let pending=new Uint8Array(),written=0;
    const resize=n=>{const bytes=new Uint8Array(n);bytes.set(pending.subarray(0,n));pending=bytes;};
    return {truncate:async n=>resize(n),write:async input=>{const bytes=input instanceof Uint8Array?input:input.data,at=input instanceof Uint8Array?0:input.position;if(at+bytes.length>pending.length)resize(at+bytes.length);pending.set(bytes,at);written+=bytes.length;},close:async()=>{if(name===this.commitName&&this.failCommit){this.failCommit=false;throw Error('commit interrupted');}file.data=pending;file.written=written;this.events.push(['commit',name]);},abort:async()=>{}};
   };
   file.createSyncAccessHandle=async()=>({getSize:()=>file.data.length,read:(out,{at})=>{const b=file.data.subarray(at,at+out.length);out.set(b);return b.length;},write:(b,{at})=>{if(at+b.length>file.data.length){const next=new Uint8Array(at+b.length);next.set(file.data);file.data=next;}file.data.set(b,at);return b.length;},truncate:n=>{const next=new Uint8Array(n);next.set(file.data.subarray(0,n));file.data=next;},flush:()=>this.events.push(['flush',name]),close(){}});
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
 const page=await reloaded.page(source.key,from,to);assert.equal(page.rows.length,36);assert.ok(page.payload.length);
 assert.equal(source.contexts[0].placement.block,0);assert.equal(typeof source.contexts[0].placement.firstRow,'bigint');
 const branch=root.children.get('branches').children.get(source.key);
 const layout=[...branch.children.values()].find(f=>f.name.startsWith('layout.'));
 const metadata=decodeObject(layout.data);
 for(const key of ['timeTrie','packages','index','sods'])assert.equal(metadata[key],undefined,key);
 assert.ok([...branch.children.keys()].some(n=>/^branch\.\d+\.tri$/.test(n)));
 const tri=[...branch.children.values()].find(f=>/^branch\.\d+\.tri$/.test(f.name));
 assert.ok(tri.written<tri.data.length/10,'time replication must skip unreceived calendar gaps');
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
 root.getFileHandle=async(...args)=>{const file=await open(...args);if(args[0]==='commit.pil'&&!file.counted){file.counted=true;const create=file.createWritable;file.createWritable=async()=>{const writer=await create(),close=writer.close;writer.close=async()=>{await close();commits++;};return writer;};}return file;};
 const receive=()=>nativeTransaction(nativeBackend(root),async backend=>{const store=new NativeStore(backend),session=store.begin();for(const r of records)await store.record(session,r,definitions.get(r.binding.digest));await store.finish(session);});
 root.failCommit=true;await assert.rejects(receive(),/commit interrupted/);
 assert.deepEqual(await nativeBackend(root).list(),[]);assert.equal(commits,0);
 await receive();assert.equal(commits,1,'one commit for the complete response, not one per control/PAC');
 const activity=dir=>dir.events.length+[...dir.children.values()].filter(e=>e.kind==='directory').reduce((n,d)=>n+activity(d),0);
 const before=activity(root);
 await receive();
 assert.equal(commits,1,'an identical sealed response does not publish another revision');
 assert.equal(activity(root),before,'an identical sealed response does not write metadata or flush data files');
 const backend=nativeBackend(root),store=new NativeStore(backend),[source]=await backend.list(),from=Date.UTC(2026,9,16,12),to=from+3600000;
 const scope={tap:source.tap,digest:source.digest,generation:source.generation,from,to};
 assert.equal((await store.resolveHour(scope)).source.key,source.key);
 assert.equal(await store.resolveHour({...scope,from:to,to:to+3600000}),undefined);
 assert.equal(await store.resolveHour({...scope,generation:'0'.repeat(32)}),undefined);
 assert.equal((await store.page(source.key,from,to)).rows.length,36);
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
   flush:()=>{events.push(['flush',args[1]]);if(fail&&args[1]==='sods')throw Error('flush interrupted');pending.delete(id);raw.flush();},close:()=>raw.close()};
 };
 backend.put=async(...args)=>{assert.equal(pending.size,0,'every data file must be durable before publishing any trie leaf');events.push(['publish']);return publish(...args);};
 const receive=()=>nativeTransaction(backend,async b=>{const store=new NativeStore(b),session=store.begin();for(const r of records)await store.record(session,r,definitions.get(r.binding.digest));await store.finish(session);});
 await assert.rejects(receive(),/flush interrupted/);assert.equal(backend.sources.size,0);assert.equal(events.some(e=>e[0]==='publish'),false);
 fail=false;events.length=0;opens=0;pending.clear();await receive();
 assert.equal(events.filter(e=>e[0]==='publish').length,1);
 assert.equal(events.filter(e=>e[0]==='flush').length,opens,'one flush per dirty file in this batch');
 assert.equal(events.at(-1)[0],'publish');
 const [source]=await backend.list();assert.ok(committedRanges(source).sods.length);
 const payload=events.filter(e=>e[0]==='write'&&e[1]==='sods'&&e[2]>8);
 assert.equal(payload.length,records.filter(r=>r.kind==='pac').length,'one contiguous SODS header+payload write per hourly PAC');
});

test('many branches reuse a bounded handle pool and flush evicted files before publication',async()=>{
 const {nativeTransaction}=await import('./store/transaction.ts');
 let open=0,peak=0,flushes=0,published=false;
 const backend={async file(){peak=Math.max(peak,++open);let size=0;return {getSize:()=>size,read:()=>0,write:(b,{at})=>{size=Math.max(size,at+b.length);return b.length;},truncate:n=>{size=n;},flush:()=>{flushes++;},close:()=>{open--;}};},async put(){assert.equal(flushes,200);published=true;}};
 await nativeTransaction(backend,async b=>{for(let i=0;i<200;i++){const h=await b.file(String(i),'sods');h.write(new Uint8Array(128),{at:0});assert.equal(h.getSize(),128);h.close();}await b.put([{key:'test'}]);});
 assert.equal(peak,64);assert.equal(open,0);assert.equal(published,true);
});


test('bootstrap commits all artifacts before publication and restores definitions before event replication',async()=>{
 const raw=new Uint8Array(readFileSync(new URL('./fixtures/trunk/bootstrap.pil',import.meta.url)));
 const version=readFileSync(new URL('./fixtures/trunk/bootstrap.version',import.meta.url),'utf8');
 const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
 const snapshot=await readTrunk({token:'',signal:new AbortController().signal,codec:new Codec(await WebAssembly.instantiate(wasm)),definitions:new Map(),fetcher:async()=>new Response(raw,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Trunk-Format':'trunk/0','Definition-Version':version}})});
 const root=new Directory();root.commitName='trunk.pil';root.failCommit=true;
 await assert.rejects(saveTrunk(root,snapshot),/commit interrupted/);
 assert.deepEqual(await trunkDefinitions(root),[],'failed first publication leaves no catalog');
 root.events.length=0;const blobs=root.children.get('trunk-blobs');blobs.events.length=0;
 await saveTrunk(root,snapshot);
 assert.equal(blobs.events.filter(e=>e[0]==='flush').length,snapshot.files.length,'reused staged blobs are flushed before commit too');
 assert.deepEqual(root.events,[['commit','trunk.pil']]);
 const saved=root.children.get('trunk.pil').data.slice();
 const definitions=await trunkDefinitions(root);
 assert.deepEqual(definitions.map(d=>d.digest).sort(),snapshot.definitions.map(d=>d.digest).sort());
 assert.equal((await nativeBackend(root).list()).length,0,'definitions do not require any event packs');
 root.failCommit=true;
 await assert.rejects(saveTrunk(root,{...snapshot,version:'b'.repeat(64)}),/commit interrupted/);
 assert.deepEqual(root.children.get('trunk.pil').data,saved,'failed refresh retains the previous root');
 assert.deepEqual((await trunkDefinitions(root)).map(d=>d.digest).sort(),definitions.map(d=>d.digest).sort());
 const corrupt=structuredClone(snapshot);corrupt.files[0].bytes[0]^=1;
 await assert.rejects(saveTrunk(root,corrupt),/digest/);
 assert.deepEqual(root.children.get('trunk.pil').data,saved);
});


test('decoded metadata cache avoids repeated file reads but observes new and failed commits across leases',async()=>{
 const root=new Directory(),cache=new NativeSnapshotCache(),key='a'.repeat(64);
 await nativeBackend(root).put([{key,tap:'demo/payment/events.v1/read',contexts:[]}]);
 const branch=root.children.get('branches').children.get(key);let metadataReads=0,rootReads=0;
 for(const file of branch.children.values()){const get=file.getFile;file.getFile=async()=>{metadataReads++;return get();};}
 const commit=root.children.get('commit.pil'),get=commit.getFile;commit.getFile=async()=>{rootReads++;return get();};
 const first=await nativeBackend(root,cache).get(key);assert.equal(metadataReads,2);
 first.tap='do-not-publish';
 for(let i=0;i<5;i++)assert.equal((await nativeBackend(root,cache).get(key)).tap,'demo/payment/events.v1/read');
 assert.equal(metadataReads,2,'immutable layout and trie are decoded once across leases');assert.ok(rootReads>=6,'every lease rechecks the published root');
 const next={...first,tap:'demo/payment/events.v1/write'};
 root.failCommit=true;await assert.rejects(nativeBackend(root).put([next]),/commit interrupted/);
 assert.equal((await nativeBackend(root,cache).get(key)).tap,'demo/payment/events.v1/read','failed publication never changes visible state');
 await nativeBackend(root).put([next]);assert.equal((await nativeBackend(root,cache).get(key)).tap,next.tap,'another writer invalidates the cached revision');
});

test('no-op publication preserves revisions; real field, byte and control changes remain durable',async()=>{
 const root=new Directory(),cache=new NativeSnapshotCache(),key='b'.repeat(64);
 const source={key,tap:'demo/payment/events.v1/read',contexts:[{common:new Uint8Array([1,2]),placement:{from:0n,to:1n,firstRow:0n,rows:1n,memberStart:8n,memberBytes:8n,pages:[]}}]};
 await nativeBackend(root,cache).put([source]);
 const branch=root.children.get('branches').children.get(key),committed=()=>decodeObject(root.children.get('commit.pil').data);
 const first=committed(),writes=branch.events.length;
 await nativeBackend(root,cache).put([structuredClone(source)],structuredClone(first.control));
 assert.deepEqual(committed(),first);assert.equal(branch.events.length,writes);
 const next=structuredClone(source);next.contexts[0].common[1]=3;next.contexts[0].placement.rows=2n;
 await nativeBackend(root,cache).put([next]);
 assert.equal(committed().serial,first.serial+1);
 // Published metadata is already decoded; a new lease need only read the root.
 for(const file of branch.children.values())file.getFile=async()=>{throw Error('unnecessary metadata reload');};
 next.contexts[0].common[1]=9;
 const saved=await nativeBackend(root,cache).get(key);
 assert.equal(saved.contexts[0].common[1],3);assert.equal(saved.contexts[0].placement.rows,2n);
 const revision=committed().sources[key],branchWrites=branch.events.length;
 await nativeBackend(root,cache).put([],{serial:1,scopes:{'demo/payment':1}});
 assert.equal(committed().sources[key],revision);assert.equal(branch.events.length,branchWrites);
 assert.deepEqual(await nativeBackend(root,cache).control(),{serial:1,scopes:{'demo/payment':1}});
});

test('connections share one bounded snapshot budget without mixing identities or revisions',()=>{
 const pool=new NativeSnapshotPool(30),a=new NativeSnapshotCache(pool),b=new NativeSnapshotCache(pool);
 const first={tap:'first'},second={tap:'second'},third={tap:'third'};
 a.put('key:1',first,10);a.put('other:1',second,10);a.put('third:1',third,10);
 assert.equal(a.get('key:1'),first,'one active connection can use the entire shared budget');
 b.put('key:1',second,10);
 assert.equal(a.get('other:1'),undefined,'the shared budget evicts the least recently used entry');
 assert.equal(a.get('key:1'),first);assert.equal(b.get('key:1'),second,'identical source IDs cannot cross connection scopes');
 b.retain({key:2});assert.equal(b.get('key:1'),undefined);assert.equal(a.get('key:1'),first,'revision checks are connection-scoped');
 b.put('key:2',second,10);a.clear();assert.equal(a.get('key:1'),undefined);assert.equal(b.get('key:2'),second,'closing a context releases only its own snapshots');
 b.put('oversized:1',third,31);assert.equal(b.get('oversized:1'),undefined,'oversized snapshots cannot exceed the budget');
});
