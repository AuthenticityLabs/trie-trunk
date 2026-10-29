import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {connectionWorker} from './pil/connection-worker.ts';
import {connectionScope} from './connections.ts';
const module=WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
const body=new Uint8Array(readFileSync(new URL('./fixtures/trunk/bootstrap.pil',import.meta.url)));
const version=readFileSync(new URL('./fixtures/trunk/bootstrap.version',import.meta.url),'utf8');
const reply=()=>new Response(body.slice(),{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Trunk-Format':'trunk/0','Definition-Version':version}});
function client(connect,archive,t){
 const pair=new MessageChannel(),jobs=new Map();let next=0;
 connect(pair.port2,archive);
 pair.port1.onmessage=({data})=>{const job=jobs.get(data.id);if(!job)return;job.replies.push(data);if(data.kind==='done'||data.kind==='error'){jobs.delete(data.id);data.kind==='done'?job.resolve(job.replies):job.reject(Error(data.message));}};
 t.after(()=>{pair.port1.postMessage({kind:'close'});pair.port1.close();});
 return {
  start(command){const id=++next;return {id,promise:new Promise((resolve,reject)=>{jobs.set(id,{resolve,reject,replies:[]});pair.port1.postMessage({id,...command});})};},
  run(command){return this.start(command).promise;},
  cancel(id){pair.port1.postMessage({kind:'cancel',id});jobs.get(id)?.reject(new DOMException('Canceled','AbortError'));jobs.delete(id);},
 };
}
const one={id:'one',name:'One',url:'https://one.example/api',enabled:true};
const two={id:'two',name:'Two',url:'https://two.example',enabled:true};
test('concurrent registrations isolate credentials, worker sessions, and durable bootstrap namespaces',{timeout:5000},async t=>{
 const calls=[],stored=[];
 const connect=connectionWorker(module,async(url,init)=>{calls.push({url,auth:init.headers.Authorization});return reply();});
 const archive=async(namespace,command)=>{stored.push({namespace,command});return {definitions:[]};};
 const a=client(connect,archive,t),b=client(connect,archive,t);
 const requests=[{connection:one,token:'shared'},{connection:two,token:'shared'},{connection:one,token:'other'},{connection:two,token:''}];
 const results=await Promise.all(requests.map((request,i)=>(i%2?a:b).run({kind:'trunk',...request})));
 for(const result of results){assert.equal(result.find(m=>m.kind==='trunk').trunk.definitions.length,2);assert.equal(result.at(-1).kind,'done');}
 assert.deepEqual(calls.map(c=>[c.url,c.auth]).sort(),requests.map(r=>[r.connection.url+'/trunk',r.token?'Bearer '+r.token:undefined]).sort());
 for(const request of requests){const namespace=connectionScope(request.connection,request.token),commands=stored.filter(s=>s.namespace===namespace).map(s=>s.command);assert.deepEqual(commands.map(c=>c.kind),['definitions','trunk-save']);assert.equal(commands[1].snapshot.definitions.length,2);}
 assert.equal(new Set(stored.map(s=>s.namespace)).size,4);
 await a.run({kind:'trunk',connection:one,token:'shared'});
 assert.equal(stored.filter(s=>s.namespace===connectionScope(one,'shared')&&s.command.kind==='definitions').length,1,'the correct server/credential session is reused');
 assert.equal(results[0].find(m=>m.kind==='trunk').trunk.definitions[0].pil.byteLength>0,true,'another worker reply cannot detach earlier results');
});
test('canceling one registration leaves other bootstrap jobs running',{timeout:5000},async t=>{
 let started,aborted;const ready=new Promise(resolve=>started=resolve),cancelled=new Promise(resolve=>aborted=resolve),saved=[];
 const connect=connectionWorker(module,async(url,init)=>{
  if(url.startsWith(one.url)){started();return new Promise((_,reject)=>init.signal.addEventListener('abort',()=>{aborted();reject(init.signal.reason);},{once:true}));}
  return reply();
 });
 const current=client(connect,async(namespace,command)=>{if(command.kind==='trunk-save')saved.push(namespace);return {definitions:[]};},t);
 const slow=current.start({kind:'trunk',connection:one,token:'one-token'});const failure=assert.rejects(slow.promise,{name:'AbortError'});
 await ready;
 const fast=current.run({kind:'trunk',connection:two,token:'two-token'});
 current.cancel(slow.id);await cancelled;await failure;
 assert.equal((await fast).find(m=>m.kind==='trunk').trunk.definitions.length,2);
 assert.deepEqual(saved,[connectionScope(two,'two-token')]);
});
