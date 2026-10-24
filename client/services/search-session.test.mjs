import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {SearchSession,credentialScope} from './search-session.ts';
import {ViewCache} from './view-archive-fixture.mjs';
import {Codec} from './pil/wasm.ts';
import {response} from './pil-fixtures.mjs';
const query={tap:'demo/payment/transactions.v1/write',from:'2026-09-01T12:00:00Z',to:'2026-09-01T14:00:00Z',size:2};
class Storage {
 values=new Map();writes=[];fail=false;
 get length(){return this.values.size}
 key(i){return [...this.values.keys()][i]??null}
 getItem(k){return this.values.get(k)??null}
 setItem(k,v){if(this.fail)throw Error('QuotaExceeded');this.writes.push(k);this.values.set(k,v)}
 removeItem(k){this.values.delete(k)}
}
async function pages(){
 const codec=new Codec(await WebAssembly.instantiate(await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)))));
 let i=0;
 const cache=new ViewCache(async()=>{
  const r=response(readFileSync(new URL(`./fixtures/pac-view/${i++?'next':'cold'}.pil`,import.meta.url)),'a'.repeat(64));
  r.headers.set('Trunk-Format','pac-view/0');return r;
 });
 const definitions=new Map(),signal=new AbortController().signal;
 const first=await cache.query(query,'',signal,codec,definitions);
 const second=await cache.query({...query,after:first.result.next},'',signal,codec,definitions);
 return [first,second];
}
test('tab checkpoint restores PIL pages, summary, cursor, selection and grid without fetching',async()=>{
 const [first,second]=await pages(),storage=new Storage(),session=new SearchSession(storage);
 session.append(query,query,'anonymous',first);
 session.append(query,query,'anonymous',second);
 const selected=second.result.page[0].key;
 session.position({top:431,left:28},selected);
 const saved=new SearchSession(storage).restore(query,'anonymous');
 assert.equal(saved.events.length,4);
 assert.deepEqual(saved.result,second.result);
 assert.deepEqual(saved.position,{top:431,left:28});assert.equal(saved.selected,selected);
 assert.deepEqual(saved.events[0].encoded(),new Uint8Array(first.batch.bytes,first.batch.rows[1],first.batch.rows[2]-first.batch.rows[1]));
 const pageWrites=storage.writes.filter(k=>k!=='snoot-search-v0');
 assert.equal(new Set(pageWrites).size,2);assert.equal(pageWrites.length,2,'scroll updates never rewrite PIL pages');
});
test('checkpoints are isolated by tab, query and credentials; tokens are not saved',async()=>{
 const [first]=await pages(),a=new Storage(),b=new Storage();
 const credential=await credentialScope('test-secret');assert.notEqual(credential,'test-secret');
 new SearchSession(a).append(query,query,credential,first);
 assert.equal(new SearchSession(b).restore(query,credential),undefined);
 assert.equal(new SearchSession(a).restore(query,'anonymous'),undefined);
 assert.equal(a.length,0);
 new SearchSession(a).append(query,query,credential,first);
 assert.equal(new SearchSession(a).restore({...query,tap:'other'},credential),undefined);
 assert.ok(!JSON.stringify([...a.values]).includes('test-secret'));
});
test('quota failure clears the old cursor rather than restoring an incomplete checkpoint',async()=>{
 const [first,second]=await pages(),storage=new Storage(),session=new SearchSession(storage);
 storage.setItem('unrelated','kept');session.append(query,query,'anonymous',first);storage.fail=true;
 assert.throws(()=>session.append(query,query,'anonymous',second),/Quota/);
 assert.equal(new SearchSession(storage).restore(query,'anonymous'),undefined);
 assert.equal(storage.getItem('unrelated'),'kept');
});
test('missing pages and invalid row offsets are rejected',async()=>{
 const [first]=await pages(),storage=new Storage(),session=new SearchSession(storage);
 session.append(query,query,'anonymous',first);
 const key=[...storage.values.keys()].find(k=>k!=='snoot-search-v0'),raw=storage.getItem(key);
 storage.removeItem(key);assert.throws(()=>new SearchSession(storage).restore(query,'anonymous'),/Missing/);
 const bad=JSON.parse(raw);bad.rows[1]=1;storage.setItem(key,JSON.stringify(bad));
 assert.throws(()=>new SearchSession(storage).restore(query,'anonymous'),/bounds/);
});
test('zoom and zero-result checkpoints restore without broadening the saved query',async()=>{
 const [first]=await pages(),storage=new Storage(),session=new SearchSession(storage);
 const zoom={...query,from:'2026-09-01T12:30:00Z'};
 const empty={...first,result:{...first.result,page:[],next:undefined,summary:{...first.result.summary,count:0,counts:first.result.summary.counts.map(()=>0),errors:first.result.summary.errors.map(()=>0)}},batch:{bytes:new ArrayBuffer(0),rows:new Uint32Array()},native:[]};
 session.append(query,zoom,'anonymous',empty);
 const restored=new SearchSession(storage).restore(query,'anonymous');
 assert.deepEqual(restored.query,zoom);assert.equal(restored.events.length,0);assert.equal(restored.result.summary.count,0);
 session.clear();assert.equal(storage.length,0);
});
test('mixed server snapshots cannot be appended to one checkpoint',async()=>{
 const [first,second]=await pages(),storage=new Storage(),session=new SearchSession(storage);
 session.append(query,query,'anonymous',first);
 assert.throws(()=>session.append(query,query,'anonymous',{...second,result:{...second.result,snapshot:'b'.repeat(64)}}),/snapshot changed/);
 assert.equal(new SearchSession(storage).restore(query,'anonymous').events.length,2);
});
test('session checkpoints retain each page bucket proof for worker cache restoration',async()=>{
 const storage=new Storage(),session=new SearchSession(storage),codec=new Codec(await WebAssembly.instantiate(await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)))));
 let i=0;
 const cache=new ViewCache(async()=>{const r=response(readFileSync(new URL(`./fixtures/pac-buckets/page-${i++}.pil`,import.meta.url)),'a'.repeat(64));r.headers.set('Trunk-Format','pac-view/0');return r});
 const defs=new Map(),signal=new AbortController().signal;let after;
 for(let n=0;n<3;n++){const view=await cache.query({...query,after},'',signal,codec,defs);after=view.result.next;session.append(query,query,'anonymous',view)}
 const saved=new SearchSession(storage).restore(query,'anonymous');
 assert.equal(saved.views.length,3);assert.ok(saved.views.every(v=>v.result.buckets.length===1));
 const resumed=new ViewCache(async(url,opts)=>{assert.equal(opts.headers['Bucket-Known'],saved.views[0].result.buckets[0].key);const r=response(readFileSync(new URL('./fixtures/pac-buckets/warm.pil',import.meta.url)),'a'.repeat(64));r.headers.set('Trunk-Format','pac-view/0');return r});
 saved.views.forEach(v=>resumed.remember(v));
 assert.equal((await resumed.query(query,'',signal,codec,defs)).result.coverage.complete,1);
});

test('a shared PAC prefix is checkpointed once and remains shared after reload',async()=>{
 const [first]=await pages(),storage=new Storage(),session=new SearchSession(storage);
 session.append(query,query,'anonymous',first);
 const key=[...storage.values.keys()].find(k=>k!=='snoot-search-v0');
 const page=JSON.parse(storage.getItem(key));
 assert.equal(page.prefixes.length,1);assert.deepEqual(page.common,[0,0]);
 const restored=new SearchSession(storage).restore(query,'anonymous');
 assert.equal(restored.events[0].prefix(),restored.events[1].prefix());
 page.common[0]=99;storage.setItem(key,JSON.stringify(page));
 assert.throws(()=>new SearchSession(storage).restore(query,'anonymous'),/PAC prefix/);
});
