import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {gzipSync} from 'node:zlib';
import {expandPAC,pacHeader} from './pil/pac.ts';
import {Codec} from './pil/wasm.ts';
import {nativePAC} from './pil/pac-native.ts';
const plain=new Uint8Array(readFileSync(new URL('../../../pill-packer/rust/tests/fixtures/pac/native.pac',import.meta.url)));
const module=WebAssembly.compile(readFileSync(new URL('../assets/pil.wasm',import.meta.url)));
function packed(body=plain){
 const zipped=gzipSync(body),out=new Uint8Array(16+zipped.length),v=new DataView(out.buffer);
 v.setUint32(0,out.length,true);v.setUint32(4,2,true);v.setUint32(8,body.length,true);
 v.setUint32(12,new DataView(body.buffer,body.byteOffset).getUint32(24,true),true);
 out.set(zipped,16);return out;
}
test('compressed PAC restores its complete inner header without retaining outer controls',async()=>{
 const codec=new Codec(await WebAssembly.instantiate(await module)),raw=packed();
 for(const inflate of [undefined,(b,n)=>codec.inflate(b,n)]){
  const out=await expandPAC(raw,0,0xffffffff,inflate);
  assert.deepEqual(out.bytes,plain);assert.equal(out.end,raw.length);
  assert.deepEqual(pacHeader(out.bytes,0,0xffffffff,new Set([1,nativePAC])),pacHeader(plain,0,0xffffffff,new Set([1,nativePAC])));
 }
 assert.equal(raw[16],0x1f);assert.equal(raw[17],0x8b);
});
test('compressed PAC framing supports partial headers and multiple adjacent packages',async()=>{
 const raw=packed(),codec=new Codec(await WebAssembly.instantiate(await module));
 const inflate=(b,n)=>codec.inflate(b,n);
 for(const n of [0,4,7,8,12,15,16,raw.length-1])assert.equal(await expandPAC(raw.subarray(0,n),0,0xffffffff,inflate),undefined);
 const joined=new Uint8Array(3+raw.length+plain.length);joined.set(raw,3);joined.set(plain,3+raw.length);
 const first=await expandPAC(joined,3,0xffffffff,inflate);
 const second=await expandPAC(joined,first.end,0xffffffff,inflate);
 assert.deepEqual(first.bytes,plain);assert.deepEqual(second.bytes,plain);assert.equal(second.end,joined.length);
});
test('compressed PAC validates outer controls against decoded headers and gzip bounds',async()=>{
 const raw=packed(),codec=new Codec(await WebAssembly.instantiate(await module));
 const expand=b=>expandPAC(b,0,0xffffffff,(v,n)=>codec.inflate(v,n));
 for(const [at,value]of [[4,3],[8,plain.length+1],[12,999]]){
  const bad=raw.slice();new DataView(bad.buffer).setUint32(at,value,true);
  await assert.rejects(expand(bad));
 }
 const bad=raw.slice();bad[bad.length-8]^=1;await assert.rejects(expand(bad));
 const trailing=new Uint8Array(raw.length+1);trailing.set(raw);new DataView(trailing.buffer).setUint32(0,trailing.length,true);
 await assert.rejects(expand(trailing));
 await assert.rejects(expandPAC(raw,0,plain.length-1,(b,n)=>codec.inflate(b,n)),/bounds|controls/);
});
