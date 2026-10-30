import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Codec} from './pil/wasm.ts';
import {PILStream} from './pil/stream.ts';
import {NativeStore} from './store/native.ts';
import {Backend} from './native-backend-fixture.mjs';
import {ViewCache,validateStatistics} from './pil/view.ts';
import {NativeTrie,NativeEvent} from './seeds/index.ts';
import {compile} from './pil/metadata.ts';
import {parseQuery,formatQuery} from './query.ts';
import {valueAt} from './dictionary.ts';
const wasm=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
async function setup({twoBranches=false}={}){
 const wire=new Uint8Array(readFileSync(new URL('./fixtures/native-ranges/ranges.pil',import.meta.url))),records=[],definitions=new Map(),codec=new Codec(await WebAssembly.instantiate(wasm));
 const parser=new PILStream(codec,definitions,()=>{},()=>{},async()=>{},async r=>records.push(r));await parser.feed(wire);parser.finish();parser.close();
 const first=records.find(r=>r.kind==='manifest'),m=first.manifest,b=first.binding,definition=definitions.get(b.digest),generation=Buffer.from(m.Generation).toString('hex');
 const from=Date.UTC(2026,9,16,12),to=from+3600000,path=b.tap.slice(0,b.tap.lastIndexOf('/'))+'/0/'+b.tap.split('/').at(-1);
 const trunk={version:'fixture',catalog:{files:['tri','pods'].map(kind=>({path:path+'.'+kind,kind,definition:b.digest,bootstrap:false,size:0}))},bindings:[b],definitions:[definition]};
 if(twoBranches){const alias=b.tap.replace(/\/write$/,'/read');assert.notEqual(alias,b.tap);trunk.bindings.push({...b,stream:2,tap:alias});trunk.catalog.files.push(...trunk.catalog.files.map(f=>({...f,path:f.path.replace('/write.','/read.')})))}
 const backend=new Backend(),store=new NativeStore(backend),sessions=new Map(),requests=[],commands=[];
 const archive=async(_token,c)=>{
  commands.push(c);
  if(twoBranches){c=structuredClone(c);if(c.query?.tap)c.query.tap=b.tap;if(c.queries)for(const q of c.queries)q.tap=b.tap;if(c.tap)c.tap=b.tap;}
  switch(c.kind){
   case 'time-read-many':return {times:await store.timeWindows(c.queries)};
   case 'time-read':return {time:await store.timeWindow(c.query)};
   case 'time-save':await store.importTime(c.input);return {};
   case 'native-begin':sessions.set(c.session,store.begin(await store.serial()));return {};
   case 'native-coverage':return {nativeCoverage:await store.coverage(c.tap)};
   case 'native-records':for(const r of c.records)await store.record(sessions.get(c.session),r.record,r.definition);return {};
   case 'native-finish':{const s=sessions.get(c.session);for(const r of c.records??[])await store.record(s,r.record,r.definition);await store.finish(s);sessions.delete(c.session);return {nativeSources:[]}};
   case 'native-abort':sessions.delete(c.session);return {};
   case 'native-resolve':return {nativeDependencies:await store.resolveHour(c)};
   case 'native-page':return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit,c.descending)};
   default:throw Error('Unexpected archive '+c.kind);
  }
 };
 const fetcher=async url=>{
  requests.push(url);if(twoBranches)url=url.replace('/read.','/write.');const headers={'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Definition-Version':b.digest,'Trunk-Generation':generation,'Trunk-Generation-Sequence':String(m.GenerationSequence??1),'Trunk-Source':b.tap};
  if(url.startsWith('/'+path+'.tri?'))return new Response(m.TimeTrie,{headers:{...headers,'Trunk-Format':'time-trie/0','Trunk-Path':path+'.tri'}});
  if(url.startsWith('/'+path+'.pods?'))return new Response(wire,{headers:{...headers,'Trunk-Format':'native-pac/0','Trunk-Path':path+'.pods'}});
  throw Error('Unexpected network request '+url);
 };
 let cache=new ViewCache(fetcher);cache.setTrunk(trunk);
 const query={tap:'',from:new Date(from).toISOString(),to:new Date(to).toISOString(),size:2,engine:'local'};
 const search=(changes={},onPage)=>cache.query({...query,...changes},'',new AbortController().signal,codec,definitions,archive,false,undefined,onPage);
 const events=view=>view.native.map((n,i)=>{const binding=view.bindings.find(b=>b.stream===view.batch.rows[i*3]),trie=new NativeTrie(compile(definitions.get(binding.digest),binding.tap));for(const t of n.trie)trie.append(t.start,t.bytes);return new NativeEvent(n.row,new Uint8Array(view.batch.bytes,view.batch.rows[i*3+1],view.batch.rows[i*3+2]-view.batch.rows[i*3+1]),trie,view.batch.common[i],n.base)});
 return {search,requests,commands,events,store,backend,query,restart(){cache=new ViewCache(fetcher);cache.setTrunk(trunk)}};
}
test('local cold search collects whole packs, publishes the first page, and resumes warm without server search',async()=>{
 const f=await setup(),progress=[];const first=await f.search({},p=>progress.push(p));
 assert.equal(first.result.summary.count,6);assert.equal(first.result.page.length,2);assert.equal(progress.length,1);assert.equal(progress[0].result.summary,undefined);
 validateStatistics(first.result.summary);assert.ok(first.result.summary.statistics.operations.latency.every(row=>row.statistic==='average'));assert.equal(f.requests.length,2);assert.ok(f.requests.every(u=>!u.startsWith('/events')));
 const seen=[...first.result.page.map(p=>p.key)];let cursor=first.result.next;
 while(cursor){const page=await f.search({after:cursor});seen.push(...page.result.page.map(p=>p.key));cursor=page.result.next;}
 assert.equal(new Set(seen).size,6);assert.equal(f.requests.length,2);
 f.restart();const again=await f.search();assert.deepEqual(again.result.page,first.result.page);assert.equal(f.requests.length,2);
});
test('local duration predicates and definition-bound text run against the same replicated hour',async()=>{
 const f=await setup(),all=await f.search({size:10});const events=f.events(all),durations=events.map(e=>valueAt(e,'duration'));
 const filtered=await f.search({size:10,query:{filters:[{field:'duration',op:'>',value:{kind:'number',value:'12'}}]}});
 assert.equal(filtered.result.summary.count,durations.filter(n=>n>12).length);assert.ok(f.events(filtered).every(e=>valueAt(e,'duration')>12));
 const unknown=await f.search({query:{filters:[],search:{text:'request completed',mode:'contains'}}});
 assert.equal(unknown.result.summary.count,6);assert.match(unknown.result.notice,/Ignored Text/);
 const known=await f.search({size:10,query:{filters:[],search:{text:'pending',mode:'contains'}}});
 assert.ok(known.result.summary.count>0);assert.ok(f.events(known).every(e=>valueAt(e,'state')==='pending'));
 assert.equal(f.requests.length,2);
});
test('descending local pages and partial-hour bounds preserve order without omissions',async()=>{
 const f=await setup(),all=await f.search({size:10}),ascending=f.events(all).map(e=>e.millis);let cursor,found=[];
 do{const page=await f.search({after:cursor,query:{filters:[],order:'desc'}});found.push(...f.events(page).map(e=>e.millis));cursor=page.result.next;}while(cursor);
 assert.deepEqual(found,ascending.toReversed());
 const clipped=await f.search({from:new Date(ascending[1]).toISOString(),to:new Date(ascending[4]).toISOString(),size:10});
 assert.deepEqual(f.events(clipped).map(e=>e.millis),ascending.slice(1,4));
});

test('completed aggregates are reused for paging, but reset and query changes rescan',async()=>{
 const f=await setup();const first=await f.search();const old=f.commands.length;
 const next=await f.search({after:first.result.next});
 assert.deepEqual(next.result.summary,first.result.summary);
 const pages=f.commands.slice(old).filter(c=>c.kind==='native-page');
 assert.equal(pages.length,1);assert.equal(pages[0].from,f.events(first).at(-1).millis);
 const changed=await f.search({query:{filters:[{field:'duration',op:'<',value:{kind:'number',value:'0'}}]}});assert.equal(changed.result.summary.count,0);
 await f.store.rebase(f.query.tap||f.commands.find(c=>c.kind==='native-resolve').tap);
 await assert.rejects(f.search({after:first.result.next}),/snapshot changed/);
});
test('Boolean set operations do not duplicate matches and preserve mixed state/event predicates',async()=>{
 const f=await setup(),all=await f.search({size:10}),events=f.events(all);
 const fields=[{name:'state',type:'STRING',values:['pending','authorized','captured']},{name:'duration',type:'U64',values:[]}];
 for(const [expression,match] of [
  ['state == pending OR state == pending',e=>valueAt(e,'state')==='pending'],
  ['state == pending XOR state == pending',()=>false],
  ['state == pending OR duration > 12',e=>valueAt(e,'state')==='pending'||valueAt(e,'duration')>12],
  ['NOT (state == pending AND duration > 12)',e=>!(valueAt(e,'state')==='pending'&&valueAt(e,'duration')>12)],
  ['state == pending XOR duration > 12',e=>(valueAt(e,'state')==='pending')!==(valueAt(e,'duration')>12)],
 ]){
  const ast=parseQuery('events | where '+expression,fields),result=await f.search({size:10,query:ast.plan});
  assert.equal(result.result.summary.count,events.filter(match).length,expression);
  assert.ok(f.events(result).every(match));assert.equal(new Set(result.result.page.map(p=>p.key)).size,result.result.page.length);
  for(const syntax of ['builder','pipeline'])assert.deepEqual(parseQuery(formatQuery(ast,syntax),fields),ast);
 }
 const before=f.requests.length;
 await assert.rejects(f.search({engine:'tantivy',query:parseQuery('events | where state == pending OR duration > 12',fields).plan}),/Logical expressions require Local Search/);
 assert.equal(f.requests.length,before);
});
test('equal timestamps across branches page without collisions in both directions',async()=>{
 const f=await setup({twoBranches:true});
 // Populate the physical fixture once. The two logical branches borrow its
 // immutable rows to exercise exact ties and source-qualified result keys.
 await f.search({tap:'demo/payment/transactions.v1/write',size:10});
 for(const order of ['asc','desc']){
  const all=await f.search({size:20,query:{filters:[],order}});assert.equal(all.result.summary.count,12);
  const expected=all.result.page.map(p=>p.key);assert.equal(new Set(expected).size,12);
  let after,keys=[];
  do{const page=await f.search({size:3,after,query:{filters:[],order}});keys.push(...page.result.page.map(p=>p.key));after=page.result.next}while(after);
  assert.deepEqual(keys,expected);
 }
});


test('repeated local queries reuse the existing event cache only while replica coverage is complete',async()=>{
 const f=await setup();const first=await f.search();const old=f.commands.length;
 const again=await f.search();assert.deepEqual(again.result,first.result);assert.deepEqual(f.events(again).map(e=>e.millis),f.events(first).map(e=>e.millis));
 assert.deepEqual(f.commands.slice(old).map(c=>c.kind),['time-read-many']);
 const tap=f.commands.find(c=>c.kind==='native-resolve').tap;await f.store.rebase(tap);
 const restart=f.commands.length;await f.search();
 assert.ok(f.commands.slice(restart).some(c=>c.kind==='native-page'));
});
