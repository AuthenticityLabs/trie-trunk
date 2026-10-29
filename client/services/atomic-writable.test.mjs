import {test} from 'node:test';
import assert from 'node:assert/strict';
import {atomicFile,atomicReplace} from './store/atomic-file.ts';
// Reproduce native WebKit: asynchronous writes use the whole backing buffer,
// even when the caller supplies a shorter typed-array view.
function directory(){
 const files=new Map();
 return {files,async getFileHandle(name){return {async createWritable(){
  let pending=new Uint8Array();
  const resize=n=>{const next=new Uint8Array(n);next.set(pending.subarray(0,n));pending=next};
  return {async truncate(n){resize(n)},async write(input){
   const view=input instanceof Uint8Array?input:input.data,at=input instanceof Uint8Array?0:input.position;
   const bytes=new Uint8Array(view.buffer);
   if(at+bytes.length>pending.length)resize(at+bytes.length);
   pending.set(bytes,at);
  },async close(){files.set(name,pending)},async abort(){}};
 }}}};
}
test('sparse asynchronous writes preserve view boundaries and the declared file extent',async()=>{
 const dir=directory(),backing=Uint8Array.from([1,2,3,4,5,6,7,8]);
 await atomicReplace(dir,'branch.tri',20,[{at:0,bytes:backing.subarray(0,2)},{at:18,bytes:backing.subarray(2,4)}]);
 const bytes=dir.files.get('branch.tri');assert.equal(bytes.length,20);
 assert.deepEqual([...bytes],[1,2,...Array(16).fill(0),3,4]);
});
test('whole-file asynchronous writes exclude bytes outside the supplied value',async()=>{
 const dir=directory(),backing=Uint8Array.from([99,10,20,88]);
 await atomicFile(dir,'commit.pil',backing.subarray(1,3));
 assert.deepEqual([...dir.files.get('commit.pil')],[10,20]);
});
