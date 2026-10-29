import {test} from 'node:test';
import assert from 'node:assert/strict';
import {atomicFile,useSyncFileWrites} from './store/atomic-file.ts';
useSyncFileWrites();
function directory(failure){
 const files=new Map([['commit.pil',new Uint8Array([1,2,3])]]),operations=[];
 return {files,operations,
  async removeEntry(name){files.delete(name)},
  async getFileHandle(name,{create}={}){
   if(!files.has(name)&&create)files.set(name,new Uint8Array());
   let closed=false;
   return {async createSyncAccessHandle(){return {
    truncate(size){files.set(name,new Uint8Array(size))},
    write(bytes,{at}){files.get(name)[at]=bytes[0];return 1},
    flush(){operations.push('flush');if(failure==='flush')throw Error('flush failed')},
    close(){closed=true;operations.push('close')}
   }},async move(parent,dest){assert.equal(closed,true);operations.push('move');if(failure==='move')throw Error('move failed');files.set(dest,files.get(name));files.delete(name)}};
  }
 };
}
test('sync publication flushes and closes a temporary file before replacing the live commit',async()=>{
 const dir=directory();await atomicFile(dir,'commit.pil',new Uint8Array([4,5]));assert.deepEqual([...dir.files.get('commit.pil')],[4,5]);assert.deepEqual(dir.operations,['flush','close','move']);assert.deepEqual([...dir.files.keys()],['commit.pil']);
});
for(const failure of ['flush','move'])test(`${failure} failure preserves the prior commit and removes the temporary file`,async()=>{
 const dir=directory(failure);await assert.rejects(atomicFile(dir,'commit.pil',new Uint8Array([9])),new RegExp(failure));assert.deepEqual([...dir.files.get('commit.pil')],[1,2,3]);assert.deepEqual([...dir.files.keys()],['commit.pil']);
});
