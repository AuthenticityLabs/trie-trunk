import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { Codec } from './pil/wasm.ts';
import { PILStream } from './pil/stream.ts';
import { expandEvent, envelopeLength } from './pil/compression.ts';
import { cbor } from './pil/cbor.ts';
import { compile } from './pil/metadata.ts';
import { NativeStore } from './store/native.ts';
import { NativeEvent, NativeTrie } from './pil/native-event.ts';
import { StoreCodec } from './store/wasm.ts';
import { Backend } from './native-backend-fixture.mjs';
const f=JSON.parse(readFileSync(new URL('./fixtures/compression/event.json',import.meta.url),'utf8'));
const bytes=s=>new Uint8Array(Buffer.from(s,'base64')); // JSON fixture encoding only; HTTP bodies are binary.
const definition=cbor(bytes(f.Definition)),raw=bytes(f.Raw),packed=bytes(f.Packed);
const module=await WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
const codec=async()=>new Codec(await WebAssembly.instantiate(module));
const hash=async b=>Buffer.from(await crypto.subtle.digest('SHA-256',b)).toString('hex');
test('Go gzip envelope expands byte-for-byte through the browser WASM validator',async()=>{
 const c=await codec(),h=c.define(bytes(f.Definition));const validate=b=>{c.upload(b);assert.equal(c.scan(h,0,b.length),b.length)};
 assert.deepEqual(await expandEvent(packed,definition,validate,(b,n)=>c.inflate(b,n)),raw);c.release(h);
 for(const bad of [packed.slice(0,-1),Uint8Array.from(packed,(v,i)=>i===packed.length-5?v^1:v)])await assert.rejects(expandEvent(bad,definition));
 const tooBig=packed.slice();new DataView(tooBig.buffer).setUint32(Math.ceil(definition.fields.length/4)+8,0xffffffff,true);
 assert.throws(()=>envelopeLength(tooBig,definition),/length/);
});
test('fragmented collect and page streams persist only decompressed PIL',async()=>{
 for(const path of ['/collect','/events'])for(const chunk of [1,37,65536]){
  const batches=[],stream=new PILStream(await codec(),new Map(),()=>{},()=>{},async b=>batches.push(b),undefined,()=>{});
  const wire=bytes(f.Wires[path]);try{for(let i=0;i<wire.length;i+=chunk)await stream.feed(wire.subarray(i,i+chunk));stream.finish();}finally{stream.close();}
  const events=batches.flatMap(b=>Array.from({length:b.rows.length/3},(_,i)=>new Uint8Array(b.bytes,b.rows[i*3+1],b.rows[i*3+2]-b.rows[i*3+1])));
  assert.equal(events.length,1);assert.deepEqual(events[0],raw);
 }
});
test('per-event native population fixtures cannot certify a partial hour',async()=>{
 const records=[],defs=new Map(),stream=new PILStream(await codec(),new Map(),d=>defs.set(d.digest,d),()=>{},async()=>assert.fail('expanded transport'),async r=>records.push(r));
 const backend=new Backend(),store=new NativeStore(backend),session=store.begin();
 try{await assert.rejects(async()=>{await stream.feed(bytes(f.Wires['/collect?format=native']));stream.finish();for(const r of records)await store.record(session,r,defs.get(r.binding.digest));},/Partial hour|complete PAC|manifest binding mismatch/);}finally{stream.close();}
 assert.deepEqual(await store.coverage(),[]);
});
test('mixed compressed and ordinary records retain ordering when WASM buffers change',async()=>{
 const original=Buffer.from(bytes(f.Wires['/collect'])),at=original.indexOf(Buffer.from(packed));assert.ok(at>5);
 const prefix=original.subarray(0,at-5),tag=original.subarray(at-5,at),expected=[packed,raw,packed,raw,packed];
 const wire=new Uint8Array(Buffer.concat([prefix,...expected.flatMap(b=>[tag,b]),Buffer.from([255,254])]));
 for(const size of [37,wire.length]){
  const found=[],stream=new PILStream(await codec(),new Map(),()=>{},()=>{},async batch=>{for(let i=0;i<batch.rows.length;i+=3)found.push(new Uint8Array(batch.bytes,batch.rows[i+1],batch.rows[i+2]-batch.rows[i+1]).slice())});
  try{for(let i=0;i<wire.length;i+=size)await stream.feed(wire.subarray(i,i+size));stream.finish();}finally{stream.close();}
  assert.equal(found.length,5);for(const v of found)assert.deepEqual(v,raw);
 }
});
