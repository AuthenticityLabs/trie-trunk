import {storageStep} from './io-error.ts';
import {decodeObject,encodeObject} from '../pil/control.ts';
import {registeredPath,type TrunkBootstrap,type TrunkSnapshot} from '../pil/trunk.ts';
import type {Metadata} from '../pil/metadata.ts';
import {directory,immutable,readFile,handle} from './files.ts';
import {atomicFile} from './native-files.ts';
const digest=/^[a-f0-9]{64}$/;
type SavedTrunk=Pick<TrunkBootstrap,'version'|'catalog'|'bindings'>;
async function rootManifest(root:FileSystemDirectoryHandle):Promise<SavedTrunk|undefined>{
 try{const bytes=await storageStep('Read Local Catalog',async()=>new Uint8Array(await (await (await root.getFileHandle('trunk.pil')).getFile()).arrayBuffer()));return bytes.length?decodeObject(bytes) as SavedTrunk:undefined;}
 catch(error){if(error instanceof DOMException&&error.name==='NotFoundError')return;throw error;}
}
export async function saveTrunk(root:FileSystemDirectoryHandle,snapshot:TrunkSnapshot){
 if(!digest.test(snapshot.version))throw Error('Invalid bootstrap version');
 const blobs=await directory(root,'trunk-blobs'),files=new Map(snapshot.files.map(f=>[f.path,f.bytes]));
 for(const descriptor of snapshot.catalog.files){if(!descriptor.bootstrap)continue;
  if(!registeredPath(descriptor.path)||!digest.test(descriptor.digest??''))throw Error('Invalid bootstrap artifact');
  const bytes=files.get(descriptor.path);if(!bytes||bytes.length!==descriptor.size)throw Error('Missing bootstrap artifact');
  const hash=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',bytes.slice().buffer)),b=>b.toString(16).padStart(2,'0')).join('');
  if(hash!==descriptor.digest)throw Error('Invalid bootstrap artifact digest');
  await immutable(blobs,hash,bytes);
 }
 // The commit contains descriptors and bindings, with definition bytes held in
 // immutable blobs. It becomes visible only after every artifact is flushed.
 const metadata={version:snapshot.version,catalog:snapshot.catalog,bindings:snapshot.bindings};
 await atomicFile(root,'trunk.pil',encodeObject(metadata));
}
export async function trunkDefinitions(root:FileSystemDirectoryHandle):Promise<Metadata[]>{
 const manifest=await rootManifest(root);if(!manifest)return [];
 const blobs=await directory(root,'trunk-blobs'),out=new Map<string,Metadata>();
 for(const file of manifest.catalog.files){if(file.kind!=='definition')continue;
  if(!digest.test(file.digest??'')||file.definition!==file.digest)throw Error('Invalid saved bootstrap definition');
  const h=await handle(blobs,file.digest!);let bytes:Uint8Array;try{bytes=readFile(h);}finally{h.close()}
  const hash=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',bytes.slice().buffer)),b=>b.toString(16).padStart(2,'0')).join('');
  if(hash!==file.digest||bytes.length!==file.size)throw Error('Incomplete saved bootstrap definition');
  out.set(hash,{digest:hash,store:bytes,pil:bytes});
 }
 return [...out.values()];
}
