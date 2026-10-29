import {PILStream,consume,type StoredDefinition} from './stream.ts';
import {compile,type Metadata,type Binding} from './metadata.ts';
import {decodeObject} from './control.ts';
import {NativeTrie} from './native-event.ts';
import type {Codec} from './wasm.ts';
import {validSource} from '../source.ts';

export type TrunkFile={path:string;kind:string;definition?:string;digest?:string;size:number;bootstrap:boolean};
export type TrunkCatalog={scope:string;directories:string[];files:TrunkFile[];sources:string[];collections:string[]};
export type TrunkBootstrap={version:string;catalog:TrunkCatalog;definitions:Metadata[];bindings:Binding[]};
export type TrunkSnapshot=TrunkBootstrap & {files:{path:string;bytes:Uint8Array}[]};
const digest=/^[a-f0-9]{64}$/;
const hex=(bytes:Uint8Array)=>Array.from(bytes,b=>b.toString(16).padStart(2,'0')).join('');
export const registeredPath=(path:unknown):path is string=>typeof path==='string'&&path.length>0&&path.split('/').every(p=>p!=='.'&&p!=='..'&&/^[a-z0-9][a-z0-9._-]*$/.test(p));
async function hash(raw:Uint8Array){return hex(new Uint8Array(await crypto.subtle.digest('SHA-256',raw.slice().buffer)));}
function catalog(value:unknown):TrunkCatalog {
 const c=value as TrunkCatalog;
 if(!c||typeof c.scope!=='string'||c.scope!==''&&!registeredPath(c.scope)||!Array.isArray(c.directories)||!c.directories.every(registeredPath)||!Array.isArray(c.files)||!Array.isArray(c.sources)||!c.sources.every(validSource)||!Array.isArray(c.collections)||!c.collections.every(s=>c.sources.includes(s)&&s.endsWith('/write')))throw Error('Invalid trunk catalog');
 const paths=new Set<string>();
 for(const f of c.files){
  if(!f||!registeredPath(f.path)||paths.has(f.path)||typeof f.kind!=='string'||!Number.isSafeInteger(f.size)||f.size<0||typeof f.bootstrap!=='boolean'||f.definition!==undefined&&!digest.test(f.definition)||f.bootstrap&&!digest.test(f.digest??'')||c.scope&&f.path!==c.scope&&!f.path.startsWith(c.scope+'/'))throw Error('Invalid trunk artifact');
  paths.add(f.path);
 }
 return c;
}
function validateState(raw:Uint8Array,definition:Metadata,codec:Codec){
 if(raw.length<72||new TextDecoder().decode(raw.subarray(0,8))!=='NUTSTRI6'||hex(raw.subarray(8,40))!==definition.digest)throw Error('Invalid state trie binding');
 const view=new DataView(raw.buffer,raw.byteOffset,raw.length),trie=new NativeTrie(compile(definition,'bootstrap'));
 let start=1;
 for(let at=72;at<raw.length;){
  if(at+8>raw.length)throw Error('Truncated state trie record');
  const length=view.getUint32(at,true),end=at+8+length;if(!length||end>raw.length)throw Error('Invalid state trie record');
  const nodes=raw.subarray(at+8,end);codec.verifyCRC32C(nodes,view.getUint32(at+4,true));trie.append(start,nodes);
  start+=(decodeObject(nodes) as unknown[]).length;at=end;
 }
}
export async function readTrunk(options:{token:string;signal:AbortSignal;codec:Codec;definitions:Map<string,StoredDefinition>;fetcher?:typeof fetch;folder?:string}):Promise<TrunkSnapshot>{
 const {token,signal,codec,definitions}=options,folder=options.folder??'';
 if(folder&&!registeredPath(folder))throw Error('Invalid trunk folder');
 const response=await (options.fetcher??fetch)(folder?`/trunk/${folder}/`:'/trunk',{signal,cache:'no-store',headers:{Accept:'application/pil','PIL-Version':'2',...(token.trim()?{Authorization:`Bearer ${token.trim()}`}:{})}});
 const version=response.headers.get('Definition-Version');
 if(response.ok&&(response.headers.get('Trunk-Format')!=='trunk/0'||!digest.test(version??'')))throw Error('Invalid trunk bootstrap representation');
 const received=new Map<string,{size:number;chunks:Uint8Array[]}>(),bindings:Binding[]=[],defined=new Map<string,Metadata>();
 let manifest:TrunkCatalog|undefined;
 await consume(response,new PILStream(codec,definitions,d=>defined.set(d.digest,d),b=>bindings.push(b),async()=>{throw Error('Event in trunk bootstrap')},undefined,undefined,async value=>{
  signal.throwIfAborted();const record=value as {kind:string;path?:string;offset?:number;bytes?:Uint8Array;catalog?:unknown};
  if(manifest)throw Error('Data after trunk catalog');
  if(record?.kind==='catalog'){manifest=catalog(record.catalog);return;}
  if(record?.kind!=='file'||!registeredPath(record.path)||!Number.isSafeInteger(record.offset)||record.offset!<0||!(record.bytes instanceof Uint8Array)||!record.bytes.length)throw Error('Invalid trunk file chunk');
  const file=received.get(record.path)??{size:0,chunks:[]};
  if(file.size!==record.offset)throw Error('Trunk file range gap');
  file.chunks.push(record.bytes);file.size+=record.bytes.length;received.set(record.path,file);
 }),signal);
 signal.throwIfAborted();
 if(!manifest)throw Error('Missing trunk catalog');
 const result:TrunkCatalog=manifest;
 if(result.scope!==folder)throw Error('Trunk folder response mismatch');
 const files:{path:string;bytes:Uint8Array}[]=[],used=new Set<string>();
 for(const f of result.files){if(!f.bootstrap)continue;
  let bytes:Uint8Array;
  if(f.kind==='definition'){
   const d=f.definition&&defined.get(f.definition);if(!d||d.digest!==f.digest)throw Error('Missing trunk definition');bytes=d.store;
  }else{
   const chunks=received.get(f.path);if(!chunks||chunks.size!==f.size)throw Error('Missing trunk artifact bytes');
   bytes=new Uint8Array(chunks.size);let at=0;for(const chunk of chunks.chunks){bytes.set(chunk,at);at+=chunk.length;}received.delete(f.path);
  }
  if(bytes.length!==f.size||await hash(bytes)!==f.digest)throw Error('Trunk artifact digest mismatch');
  if(f.kind==='resource')decodeObject(bytes);
  if(f.kind==='state'){const d=f.definition&&defined.get(f.definition);if(!d)throw Error('State trie has no definition');validateState(bytes,d,codec);}
  if(f.definition)used.add(f.definition);files.push({path:f.path,bytes});
 }
 if(received.size||[...defined.keys()].some(d=>!used.has(d))||bindings.some(b=>!used.has(b.digest)||!result.sources.includes(b.tap))||result.sources.some(s=>!bindings.some(b=>b.tap===s)))throw Error('Trunk catalog and definitions disagree');
 return {version:version!,catalog:result,definitions:[...defined.values()],bindings,files};
}
