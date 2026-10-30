import {test} from 'node:test';
import assert from 'node:assert/strict';
import {inspectLocalStorage,clearLocalStorage,clearReplicaContext,storageBytes} from './local-storage.ts';
import {nativeBackend,atomicFile} from './store/native-files.ts';
import {NativeStore} from './store/native.ts';
class Directory{
 kind='directory';children=new Map();failCommit=false;
 constructor(name='root'){this.name=name}
 async getDirectoryHandle(name,{create}={}){if(!this.children.has(name)){if(!create)throw new DOMException('Missing','NotFoundError');this.children.set(name,new Directory(name))}return this.children.get(name)}
 async getFileHandle(name,{create}={}){if(!this.children.has(name)){
  if(!create)throw new DOMException('Missing','NotFoundError');const file={name,kind:'file',data:new Uint8Array()};
  file.getFile=async()=>({size:file.data.length,arrayBuffer:async()=>{if(name==='events.sods')throw Error('Payload read forbidden');return file.data.slice().buffer}});
  file.createWritable=async()=>{let pending=new Uint8Array();return {write:async b=>{pending=b.slice()},truncate:async n=>{pending=new Uint8Array(n)},close:async()=>{if(name==='commit.pil'&&this.failCommit)throw Error('Injected commit failure');file.data=pending},abort:async()=>{}}};
  this.children.set(name,file);
 }return this.children.get(name)}
 async removeEntry(name){if(!this.children.delete(name))throw new DOMException('Missing','NotFoundError')}
 async *values(){yield* this.children.values()}
}
const namespace='a'.repeat(64);
test('clearing search metadata never routes to clearing native replica files',async()=>{
 const {root,dir}=await fixture(),before=[...dir.children.keys()];
 await clearLocalStorage(root,{kind:'searches'});assert.deepEqual([...dir.children.keys()],before);
});
const source=(key,tap)=>({key:key.repeat(64),tap,digest:'b'.repeat(64),generation:'c'.repeat(32),trieNodes:1,files:Object.fromEntries(['map','trie','state','index','sods'].map(k=>[k,{end:0,extents:[],blobs:[]}]))});
async function fixture(){
 const root=new Directory(),stores=await root.getDirectoryHandle('snoot-store',{create:true}),dir=await stores.getDirectoryHandle(namespace,{create:true});
 const a=source('1','demo/payment/transactions.v1/read'),b=source('2','demo/identity/sessions.v1/read');
 await nativeBackend(dir).put([a,b]);
 const branches=await dir.getDirectoryHandle('branches');
 for(const s of [a,b])await atomicFile(await branches.getDirectoryHandle(s.key),'events.sods',new Uint8Array(1024));
 await atomicFile(dir,'trunk.pil',new Uint8Array(16));
 await atomicFile(root,'unrelated.bin',new Uint8Array(12));return {root,dir,a,b};
}
test('storage tree uses actual resource scopes and file lengths without decoding event payloads',async()=>{
 const {root}=await fixture();const nodes=await inspectLocalStorage(root,{[namespace]:'Production'});
 const context=nodes[0].children[0];assert.equal(context.label,'Production');
 const services=context.children.filter(n=>n.type==='Service');assert.deepEqual(services.map(n=>n.label),['payment','identity']);assert.equal(services[0].note,'demo');
 const resource=services[0].children[0];assert.equal(context.type,'Trunk');assert.equal(resource.type,'Resource');assert.equal(resource.children[0].type,'Branch');assert.equal(resource.label,'transactions.v1');assert.deepEqual(resource.target.scope,['demo','payment','transactions.v1']);
 assert.equal(resource.children[0].children.find(n=>n.type==='Datastore'&&n.label==='Events').bytes,1024);
 assert.equal(context.bytes,context.children.reduce((n,c)=>n+c.bytes,0));
 assert.equal(nodes[0].bytes,context.bytes);assert.ok(!JSON.stringify(nodes).includes('unrelated.bin'));
});
test('scoped clearing invalidates before reclaiming and preserves sibling resources',async()=>{
 const {root,dir,a,b}=await fixture(),before=await nativeBackend(dir).control();
 await clearLocalStorage(root,{kind:'replicas',namespace,scope:['demo','payment']});
 const backend=nativeBackend(dir);assert.equal((await backend.control()).serial,before.serial+1);
 assert.equal(await backend.get(a.key),undefined);assert.equal((await backend.get(b.key)).retiredBy,undefined);
 const branches=await dir.getDirectoryHandle('branches');assert.equal(branches.children.has(a.key),false);assert.equal((await branches.getDirectoryHandle(b.key)).children.has('events.sods'),true);
 assert.equal(root.children.has('unrelated.bin'),true);
});
test('whole-context reset keeps durable epoch and removes payloads, snapshots and bootstrap',async()=>{
 const {dir,a}=await fixture();const serial=await new NativeStore(nativeBackend(dir)).serial();
 await clearReplicaContext(dir);assert.deepEqual([...dir.children.keys()],['commit.pil']);
 const backend=nativeBackend(dir);assert.deepEqual(await backend.list(),[]);assert.equal((await backend.control()).serial,serial+1);assert.equal((await backend.control()).scopes[a.tap.split('/').slice(0,3).join('/')],1);
 await clearReplicaContext(dir);assert.equal((await nativeBackend(dir).control()).serial,serial+2);
});
test('failed reset publication leaves committed cache files intact',async()=>{
 const {dir}=await fixture();dir.failCommit=true;await assert.rejects(clearReplicaContext(dir),/Injected/);
 assert.equal((await nativeBackend(dir).list()).length,2);assert.ok(dir.children.has('branches'));assert.ok(dir.children.has('trunk.pil'));
});
test('all-caches reset removes known old stores while preserving unrelated origin files',async()=>{
 const {root,dir}=await fixture();await root.getDirectoryHandle('trie-trunk-v2',{create:true});
 await clearLocalStorage(root,{kind:'all'});assert.equal(root.children.has('trie-trunk-v2'),false);assert.equal(root.children.has('unrelated.bin'),true);assert.deepEqual([...dir.children.keys()],['commit.pil']);
 await assert.rejects(clearLocalStorage(root,{kind:'legacy',name:'unrelated.bin'}),/Unknown/);
 await assert.rejects(clearLocalStorage(root,{kind:'replicas',namespace:'../outside'}),/Invalid/);
});
test('byte labels use decimal units',()=>{assert.equal(storageBytes(1000),'1 KB');assert.equal(storageBytes(1e9),'1 GB');assert.equal(storageBytes(0),'0 B')});

test('catalog artifacts show Definition metadata and all shared resource paths without duplicate bytes',async()=>{
 const {encodeObject}=await import('./pil/control.ts');
 const {root,dir}=await fixture(),blobs=await dir.getDirectoryHandle('trunk-blobs',{create:true});
 const definition='d'.repeat(64),state='e'.repeat(64),orphan='f'.repeat(64);
 await atomicFile(blobs,definition,encodeObject({definition:'payment/transactions',fields:[{name:'id',type:'UUID'},{name:'duration',type:'U32'}]}));
 await atomicFile(blobs,state,new Uint8Array(32));await atomicFile(blobs,orphan,new Uint8Array(7));
 const files=[{path:'demo/payment/transactions.v1/0/resource.rx',kind:'definition',digest:definition,bootstrap:true},{path:'prod/payment/transactions.v1/resource.rx',kind:'definition',digest:definition,bootstrap:true},{path:'demo/payment/transactions.v1/state.tri',kind:'state',digest:state,bootstrap:true}];
 await atomicFile(dir,'trunk.pil',encodeObject({catalog:{files,sources:['demo/payment/transactions.v1/read','demo/payment/transactions.v1/write']}}));
 const context=(await inspectLocalStorage(root,{[namespace]:'Production'}))[0].children[0];
 const artifacts=context.children.find(n=>n.label==='Catalog & Definitions');assert.equal(artifacts.children.length,3);
 const def=artifacts.children.find(n=>n.label==='payment/transactions');
 assert.equal(def.type,'Definition');assert.match(def.note,/2 fields/);assert.match(def.note,/demo \/ payment \/ transactions.v1/);assert.match(def.note,/prod \/ payment \/ transactions.v1/);
 assert.equal(artifacts.children.find(n=>n.type==='Trie').note,'demo / payment / transactions.v1');
 assert.equal(artifacts.children.find(n=>n.label==='Unreferenced Catalog File').bytes,7);
 assert.ok(artifacts.bytes>=artifacts.children.reduce((n,c)=>n+c.bytes,0));
 assert.equal(artifacts.note,'1 resource · 2 storage branches');
 assert.ok(!artifacts.children.some(n=>/^[a-f0-9]{64}$/.test(n.label)));
});
test('unreadable catalog and Definitions do not prevent inventory or cleanup',async()=>{
 const {encodeObject}=await import('./pil/control.ts');const {root,dir}=await fixture(),blobs=await dir.getDirectoryHandle('trunk-blobs',{create:true}),digest='d'.repeat(64);
 await atomicFile(blobs,digest,new Uint8Array([255]));
 await atomicFile(dir,'trunk.pil',encodeObject({catalog:{sources:[],files:[{path:'demo/payment/resource.rx',kind:'definition',digest,bootstrap:true}]}}));
 let context=(await inspectLocalStorage(root))[0].children[0];assert.equal(context.label,'Unidentified Trunk');
 assert.match(context.children.find(n=>n.label==='Catalog & Definitions').children[0].note,/Definition metadata unavailable/);
 await atomicFile(dir,'trunk.pil',new Uint8Array([255]));context=(await inspectLocalStorage(root))[0].children[0];
 assert.equal(context.children.find(n=>n.label==='Catalog & Definitions').children[0].label,'Unidentified Catalog File');
 await clearLocalStorage(root,{kind:'replicas',namespace});assert.deepEqual([...dir.children.keys()],['commit.pil']);
});

test('every displayed storage node offers a deletion target, including branch artifacts',async()=>{
 const {root}=await fixture();
 const walk=nodes=>{for(const node of nodes){assert.ok(node.target,node.label);walk(node.children)}};
 walk(await inspectLocalStorage(root));
});
test('branch deletion removes only the selected branch and invalidates its resource epoch',async()=>{
 const {root,dir,a,b}=await fixture();await clearLocalStorage(root,{kind:'branch',namespace,key:a.key});
 const backend=nativeBackend(dir);assert.equal(await backend.get(a.key),undefined);assert.ok(await backend.get(b.key));
 assert.equal((await backend.control()).scopes['demo/payment/transactions.v1'],1);
 assert.equal((await dir.getDirectoryHandle('branches')).children.has(a.key),false);
 await assert.rejects(clearLocalStorage(root,{kind:'branch',namespace,key:'../bad'}),/Invalid/);
});
test('deleting a shared catalog Definition evicts its dependent branch and preserves other resources',async()=>{
 const {encodeObject,decodeObject}=await import('./pil/control.ts');const {root,dir,a,b}=await fixture();
 b.digest='e'.repeat(64);await nativeBackend(dir).put([b]);
 const blobs=await dir.getDirectoryHandle('trunk-blobs',{create:true});
 await atomicFile(blobs,a.digest,new Uint8Array(8));await atomicFile(blobs,b.digest,new Uint8Array(9));
 const files=[{kind:'definition',bootstrap:true,path:'demo/payment/transactions.v1/0/resource.rx',digest:a.digest,definition:a.digest},{kind:'definition',bootstrap:true,path:'demo/identity/sessions.v1/0/resource.rx',digest:b.digest,definition:b.digest}];
 const bindings=[{tap:a.tap,digest:a.digest},{tap:b.tap,digest:b.digest}];
 await atomicFile(dir,'trunk.pil',encodeObject({catalog:{files,sources:[a.tap,b.tap],collections:[]},bindings}));
 await clearLocalStorage(root,{kind:'catalog',namespace,digest:a.digest});
 const backend=nativeBackend(dir);assert.equal(await backend.get(a.key),undefined);assert.ok(await backend.get(b.key));
 assert.equal(blobs.children.has(a.digest),false);assert.equal(blobs.children.has(b.digest),true);
 const saved=decodeObject(new Uint8Array(await(await(await dir.getFileHandle('trunk.pil')).getFile()).arrayBuffer()));
 assert.deepEqual(JSON.parse(JSON.stringify(saved.catalog.files)),files.slice(1));assert.deepEqual(JSON.parse(JSON.stringify(saved.bindings)),bindings.slice(1));assert.deepEqual(saved.catalog.sources,[b.tap]);
});
test('failed invalidation never removes catalog artifacts',async()=>{
 const {encodeObject}=await import('./pil/control.ts');const {root,dir,a}=await fixture(),blobs=await dir.getDirectoryHandle('trunk-blobs',{create:true});
 await atomicFile(blobs,a.digest,new Uint8Array(8));
 await atomicFile(dir,'trunk.pil',encodeObject({catalog:{files:[{kind:'definition',digest:a.digest,definition:a.digest}],sources:[a.tap]}}));
 dir.failCommit=true;await assert.rejects(clearLocalStorage(root,{kind:'catalog',namespace,digest:a.digest}),/Injected/);
 assert.equal(blobs.children.has(a.digest),true);assert.ok(await nativeBackend(dir).get(a.key));
});
test('legacy child deletion is confined to its own directory',async()=>{
 const root=new Directory(),old=await root.getDirectoryHandle('trie-trunk-v2',{create:true});
 await atomicFile(old,'selected',new Uint8Array(1));await atomicFile(old,'kept',new Uint8Array(2));
 await clearLocalStorage(root,{kind:'legacy',name:'trie-trunk-v2',path:['selected']});assert.equal(old.children.has('selected'),false);assert.equal(old.children.has('kept'),true);
 await assert.rejects(clearLocalStorage(root,{kind:'legacy',name:'trie-trunk-v2',path:['..','kept']}),/Invalid/);
});

test('same-named services retain distinct cache scopes after removing the cache navigation level',async()=>{
 const {root,dir,a}=await fixture(),prod=source('3','prod/payment/transactions.v1/read');await nativeBackend(dir).put([prod]);
 const context=(await inspectLocalStorage(root,{[namespace]:'snoot.example.com'}))[0].children[0];
 const payment=context.children.filter(n=>n.type==='Service'&&n.label==='payment');assert.equal(payment.length,2);
 assert.deepEqual(payment.map(n=>n.note).sort(),['demo','prod']);assert.notEqual(payment[0].id,payment[1].id);
 const selected=payment.find(n=>n.note==='prod');assert.deepEqual(selected.target.scope,['prod','payment']);
 await clearLocalStorage(root,selected.target);assert.ok(await nativeBackend(dir).get(a.key));assert.equal(await nativeBackend(dir).get(prod.key),undefined);
});


test('clearing an unidentified trunk leaves durable reset epochs but no phantom inventory nodes',async()=>{
 const {root,dir}=await fixture();assert.equal((await inspectLocalStorage(root))[0].children[0].label,'Unidentified Trunk');
 await clearLocalStorage(root,{kind:'replicas',namespace});
 assert.deepEqual(await inspectLocalStorage(root),[]);
 assert.deepEqual([...dir.children.keys()],['commit.pil']);assert.ok((await nativeBackend(dir).control()).serial>0);
 await clearLocalStorage(root,{kind:'all'});assert.deepEqual(await inspectLocalStorage(root),[]);
});
test('empty branch directories and reset-only contexts do not advertise replicated data',async()=>{
 const {root,dir}=await fixture();await clearReplicaContext(dir);await dir.getDirectoryHandle('branches',{create:true});
 assert.deepEqual(await inspectLocalStorage(root),[]);
 await atomicFile(dir,'leftover.bin',new Uint8Array(10));assert.equal((await inspectLocalStorage(root))[0].children[0].bytes>10,true);
});

test('invalid PIL metadata remains visible and can be cleared without decoding an older format',async()=>{
 const {root,dir}=await fixture();await atomicFile(dir,'commit.pil',new Uint8Array([255]));
 const context=(await inspectLocalStorage(root))[0].children[0];assert.equal(context.label,'Unidentified Trunk');assert.match(context.note,/unreadable/);assert.ok(context.children[0].target);
 await clearLocalStorage(root,context.children[0].target);assert.deepEqual(await inspectLocalStorage(root),[]);assert.deepEqual([...dir.children.keys()],['commit.pil']);
 assert.ok((await nativeBackend(dir).control()).serial>0);
});
test('unreadable branch layout does not prevent inspecting or clearing its whole replica',async()=>{
 const {root,dir,a}=await fixture(),branch=await(await dir.getDirectoryHandle('branches')).getDirectoryHandle(a.key);
 await atomicFile(branch,'layout.1.pil',new Uint8Array([255]));const nodes=await inspectLocalStorage(root);assert.ok(nodes[0].children[0].children.some(n=>n.label==='Unreferenced Branch'));
 await clearLocalStorage(root,{kind:'replicas',namespace});assert.deepEqual(await inspectLocalStorage(root),[]);
});
