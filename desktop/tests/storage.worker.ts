// Native WebKit regression probe, bundled only by the storage test runner.
import {atomicFile,useSyncFileWrites} from '../../client/services/store/atomic-file.ts';
import {readPublishedFile} from '../../client/services/store/publication.ts';
useSyncFileWrites();
self.onmessage=async()=>{
 const checks:Record<string,boolean>={};let result:unknown;let root:FileSystemDirectoryHandle|undefined,folder='';
 const proto=FileSystemFileHandle.prototype as FileSystemFileHandle & {move:(dir:FileSystemDirectoryHandle,name:string)=>Promise<void>};
 const move=proto.move;let failPublication=false,interruptPreserve=false;
 try{
  root=await navigator.storage.getDirectory();folder=`__snoot_publication_${crypto.randomUUID()}`;
  const dir=await root.getDirectoryHandle(folder,{create:true});
  proto.move=async function(parent,name){
   let exists=false;try{await parent.getFileHandle(name);exists=true}catch(e){if(!(e instanceof DOMException&&e.name==='NotFoundError'))throw e}
   if(exists)throw new DOMException('Replacement refused by WebKit','UnknownError');
   if(failPublication&&this.name.startsWith('.pending-'))throw new DOMException('Interrupted publication','UnknownError');
   await move.call(this,parent,name);
   if(interruptPreserve&&name.startsWith('.previous-'))throw Error('Interrupted after preserving current file');
  };
  const equal=async(expected:number[])=>JSON.stringify([...await readPublishedFile(dir,'trunk.pil')??[]])===JSON.stringify(expected);
  await atomicFile(dir,'trunk.pil',new Uint8Array([1,2,3]));
  const candidate=await dir.getFileHandle('candidate',{create:true}) as typeof proto;
  try{await candidate.move(dir,'trunk.pil');throw Error('Expected rename failure')}catch(e){checks.reproducedRenameFailure=e instanceof DOMException&&e.name==='UnknownError'}
  await dir.removeEntry('candidate');
  await atomicFile(dir,'trunk.pil',new Uint8Array([4,5]));checks.repeatedPublication=await equal([4,5]);
  failPublication=true;
  try{await atomicFile(dir,'trunk.pil',new Uint8Array([9]));throw Error('Expected publication failure')}catch(e){checks.publicationRollback=String(e).includes('Interrupted publication')&&await equal([4,5])}
  failPublication=false;interruptPreserve=true;
  try{await atomicFile(dir,'trunk.pil',new Uint8Array([8]));throw Error('Expected interrupted preserve')}catch(e){checks.interruptedPreserve=String(e).includes('Interrupted after preserving')}
  interruptPreserve=false;checks.reopenRecovery=await equal([4,5]);
  await atomicFile(dir,'trunk.pil',new Uint8Array([6]));checks.writeAfterRecovery=await equal([6]);
  const names:string[]=[];for await(const file of (dir as any).values())names.push(file.name);
  checks.recoveryFilesReclaimed=names.length===1&&names[0]==='trunk.pil';
  result={ok:Object.values(checks).every(Boolean),checks};
 }catch(e){result={ok:false,checks,error:String(e)}}
 finally{proto.move=move;if(root&&folder)await root.removeEntry(folder,{recursive:true})}
 self.postMessage(result);
};
