import {test} from 'node:test';
import assert from 'node:assert/strict';
import {atomicFile,useSyncFileWrites} from './store/atomic-file.ts';
import {readPublishedFile,recoverPublications,removePublishedFile} from './store/publication.ts';
useSyncFileWrites();
function directory(failure){
 const files=new Map([['commit.pil',new Uint8Array([1,2,3])]]),operations=[];
 const absent=()=>new DOMException('missing','NotFoundError');
 return {files,operations,
  async removeEntry(name){if(!files.delete(name))throw absent()},
  async *values(){for(const name of [...files.keys()])yield {name,kind:'file'}},
  async getFileHandle(name,{create}={}){
   if(!files.has(name)){if(!create)throw absent();files.set(name,new Uint8Array())}
   let closed=true;
   return {get name(){return name},async getFile(){return {arrayBuffer:async()=>files.get(name).slice().buffer}},async createSyncAccessHandle(){closed=false;return {
    truncate(size){files.set(name,new Uint8Array(size))},
    write(bytes,{at}){files.get(name)[at]=bytes[0];return 1},
    flush(){operations.push('flush');if(failure==='flush')throw Error('flush failed')},
    close(){closed=true;operations.push('close')}
   }},async move(parent,dest){
    assert.equal(closed,true);operations.push(['move',name,dest]);
    // Reproduce WebKit rejecting replacement rather than POSIX rename semantics.
    if(files.has(dest))throw new DOMException('cannot replace existing file','UnknownError');
    if(failure==='move'&&name.startsWith('.pending-'))throw Error('move failed');
    files.set(dest,files.get(name));files.delete(name);name=dest;
   }};
  }
 };
}
test('publication succeeds repeatedly when move cannot overwrite an existing file',async()=>{
 const dir=directory();
 for(const bytes of [[4,5],[6],[7,8,9,10]]){
  await atomicFile(dir,'commit.pil',new Uint8Array(bytes));
  assert.deepEqual([...await readPublishedFile(dir,'commit.pil')],bytes);
  assert.deepEqual([...dir.files.keys()],['commit.pil']);
 }
 assert.deepEqual(dir.operations.slice(0,2),['flush','close']);
});
for(const failure of ['flush','move'])test(`${failure} failure preserves the prior commit and removes the temporary file`,async()=>{
 const dir=directory(failure);await assert.rejects(atomicFile(dir,'commit.pil',new Uint8Array([9])),new RegExp(failure));assert.deepEqual([...dir.files.get('commit.pil')],[1,2,3]);assert.deepEqual([...dir.files.keys()],['commit.pil']);
});
test('first publication does not attempt to remove its now-absent temporary name',async()=>{
 const dir=directory();dir.files.clear();dir.removeEntry=async()=>{throw new DOMException('transient','UnknownError')};
 await atomicFile(dir,'commit.pil',new Uint8Array([8]));assert.deepEqual([...dir.files.get('commit.pil')],[8]);
});
test('failed temporary cleanup does not mask the failed publication',async()=>{
 const dir=directory('move');dir.removeEntry=async()=>{throw new DOMException('cleanup failed','UnknownError')};
 await assert.rejects(atomicFile(dir,'commit.pil',new Uint8Array([9])),/Publish Local File commit.pil — Error: move failed/);
 assert.deepEqual([...dir.files.get('commit.pil')],[1,2,3]);
});
test('reopen restores a commit interrupted after preserving the old file',async()=>{
 const dir=directory();dir.files.set('.previous-commit.pil',dir.files.get('commit.pil'));dir.files.delete('commit.pil');
 dir.files.set('.pending-interrupted',new Uint8Array([9]));
 assert.deepEqual([...await readPublishedFile(dir,'commit.pil')],[1,2,3]);
 assert.equal(dir.files.has('.previous-commit.pil'),false);
});
test('reopen retains the new commit if interrupted before previous-file cleanup',async()=>{
 const dir=directory();dir.files.set('.previous-commit.pil',new Uint8Array([0]));
 assert.deepEqual([...await readPublishedFile(dir,'commit.pil')],[1,2,3]);
 await recoverPublications(dir);assert.deepEqual([...dir.files.keys()],['commit.pil']);
});
test('a cleanup failure after publication leaves one bounded recovery file, reclaimed on the next write',async()=>{
 const dir=directory(),remove=dir.removeEntry;dir.removeEntry=async()=>{throw Error('cleanup failed')};
 await atomicFile(dir,'commit.pil',new Uint8Array([8]));
 assert.deepEqual([...await readPublishedFile(dir,'commit.pil')],[8]);assert.equal(dir.files.size,2);
 dir.removeEntry=remove;await atomicFile(dir,'commit.pil',new Uint8Array([9]));
 assert.deepEqual([...dir.files.get('commit.pil')],[9]);assert.equal(dir.files.size,1);
});
test('an interrupted rollback remains recoverable on next open',async()=>{
 const dir=directory('move'),get=dir.getFileHandle;
 dir.getFileHandle=async(...args)=>{const f=await get(...args),move=f.move;f.move=async(parent,name)=>{
  if(f.name==='.previous-commit.pil')throw Error('recovery unavailable');return move(parent,name);
 };return f};
 await assert.rejects(atomicFile(dir,'commit.pil',new Uint8Array([9])),/Publish Local File/);
 assert.deepEqual([...dir.files.get('.previous-commit.pil')],[1,2,3]);
 dir.getFileHandle=get;assert.deepEqual([...await readPublishedFile(dir,'commit.pil')],[1,2,3]);
});
test('explicit deletion removes recovery state so metadata cannot reappear',async()=>{
 const dir=directory();dir.files.set('.previous-commit.pil',new Uint8Array([0]));
 await removePublishedFile(dir,'commit.pil');assert.equal(await readPublishedFile(dir,'commit.pil'),undefined);assert.equal(dir.files.size,0);
});
