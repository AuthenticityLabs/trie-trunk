import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Codec } from "./pil/wasm.ts";
import { PILStream } from "./pil/stream.ts";
const wire = new Uint8Array(readFileSync(new URL("./fixtures/native-ranges/ranges.pil", import.meta.url)));
const wasm = await WebAssembly.compile(readFileSync(new URL("../assets/pil.wasm", import.meta.url)));
async function reader(onNative) {
 return new PILStream(new Codec(await WebAssembly.instantiate(wasm)),new Map(),()=>{},()=>{},async()=>{throw Error("unexpected expanded event")},onNative);
}
test("Go native range stream retains complete index and lazy population",async()=>{
 const records=[],s=await reader(async r=>records.push(r));
 try{
  for(let i=0;i<wire.length;i+=17)await s.feed(wire.subarray(i,i+17));
  assert.equal(s.finish(),true);
  const m=records.find(r=>r.kind==="manifest");
  assert.equal(m.manifest.RowWidth,16);
  assert.equal(m.manifest.Provisional,false);
  assert.deepEqual(m.manifest.Index.map(r=>[r.Start,r.End]),[[1,7]]);
  assert.equal(records.filter(r=>r.kind==="package-end").length,1);
  assert.equal(records.find(r=>r.kind==="chunk"&&r.file==="index").bytes.length,6*16);
  assert.equal(records.filter(r=>r.kind==="pac").length,1);
 }finally{s.close()}
});
test("native controls require capability and complete body",async()=>{
 const no=await reader();try{await assert.rejects(no.feed(wire),/not negotiated/)}finally{no.close()}
 const s=await reader(async()=>{});try{await s.feed(wire.subarray(0,wire.length-1));assert.throws(()=>s.finish(),/completion/)}finally{s.close()}
});
test("manifest identity bytes do not carry the network buffer into storage messages",async()=>{
 const records=[],s=await reader(async r=>records.push(structuredClone(r)));
 try {
  await s.feed(wire);s.finish();
  const m=records.find(r=>r.kind==="manifest").manifest;
  for(const value of Object.values(m))if(value instanceof Uint8Array)
   assert.equal(value.buffer.byteLength,value.byteLength,"identity must own only its visible bytes");
 } finally {s.close()}
});
test("native payload corruption fails before coverage can be published",async()=>{
 const altered=wire.slice();const index=readFileSync(new URL("./fixtures/native-ranges/index.bin",import.meta.url));const offset=Buffer.from(altered).indexOf(index);assert.ok(offset>0);altered[offset]^=1;
 const s=await reader(async()=>{});try{await assert.rejects(s.feed(altered),/checksum/)}finally{s.close()}
});
