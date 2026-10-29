import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {readTrunk} from './pil/trunk.ts';
import {Codec} from './pil/wasm.ts';
const raw=new Uint8Array(readFileSync(new URL('./fixtures/trunk/bootstrap.pil',import.meta.url)));
const version=readFileSync(new URL('./fixtures/trunk/bootstrap.version',import.meta.url),'utf8');
const module=WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
const reply=bytes=>new Response(bytes,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Completion':'body','Trunk-Format':'trunk/0','Definition-Version':version}});
async function options(fetcher){return {token:'secret',signal:new AbortController().signal,codec:new Codec(await WebAssembly.instantiate(await module)),definitions:new Map(),fetcher};}
test('bootstrap receives every definition and both trie roles in one request',async()=>{
 const calls=[],input=await options(async(url,init)=>{calls.push(url);assert.equal(init.headers.Authorization,'Bearer secret');assert.equal(init.cache,'no-store');return reply(raw)});
 const trunk=await readTrunk(input);assert.deepEqual(calls,['/trunk']);assert.equal(trunk.definitions.length,2);assert.equal(trunk.bindings.length,10);assert.equal(trunk.catalog.sources.length,5);
 const paths=new Set(trunk.files.map(f=>f.path));for(const path of ['demo/resource.tri','demo/payment/transactions.v1/0/state.tri','demo/payment/transactions.v1/1/state.tri','demo/payment/transactions.v1/0/resource.rx','demo/payment/transactions.v1/1/resource.rx'])assert(paths.has(path),path);
 assert(trunk.catalog.files.some(f=>f.path==='demo/payment/transactions.v1/0/write.tri'&&!f.bootstrap));
 assert.equal((await readTrunk(input)).definitions.length,2);
});
test('truncation and corrupt state bytes cannot publish a bootstrap',async()=>{
 await assert.rejects(readTrunk(await options(async()=>reply(raw.subarray(0,-1)))),/completion|success|incomplete/i);
 const bad=raw.slice(),marker=Buffer.from(bad).indexOf('NUTSTRI6');assert(marker>0);bad[marker+64]^=1;
 await assert.rejects(readTrunk(await options(async()=>reply(bad))),/digest/);
});
test('folder traversal must return the requested scope',async()=>{
 const input=await options(async url=>{assert.equal(url,'/trunk/demo/');return reply(raw)});
 await assert.rejects(readTrunk({...input,folder:'demo'}),/folder response mismatch/);
 await assert.rejects(readTrunk({...input,folder:'demo/../etc'}),/folder/);
});
