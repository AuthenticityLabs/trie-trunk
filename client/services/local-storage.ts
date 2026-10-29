import {decodeObject,encodeObject} from './pil/control.ts';
import {atomicFile} from './store/native-files.ts';
import type {NativeSource,NativeControl} from './store/native.ts';
import {entries} from './store/files.ts';
import type {TrunkCatalog,TrunkFile} from './pil/trunk.ts';

export type StorageTarget={kind:'replicas';namespace?:string;scope?:string[]}|{kind:'branch';namespace:string;key:string}|{kind:'catalog';namespace:string;digest?:string}|{kind:'legacy';name:string;path?:string[]}|{kind:'summaries'}|{kind:'all'};
export type StorageNode={id:string;label:string;bytes:number;children:StorageNode[];target?:StorageTarget;note?:string;impact?:string;type?:string};
export type StorageInventory={nodes:StorageNode[];bytes:number;usage?:number;quota?:number};
const legacy=['trie-trunk-v1','trie-trunk-v2'];
const namespacePattern=/^[a-f0-9]{64}$/;
async function child(root:FileSystemDirectoryHandle,name:string){
 try{return await root.getDirectoryHandle(name)}catch(e){if(e instanceof DOMException&&e.name==='NotFoundError')return;throw e}
}
async function object(root:FileSystemDirectoryHandle,name:string):Promise<any>{
 try{const handle=await root.getFileHandle(name),file=await handle.getFile();return decodeObject(new Uint8Array(await file.arrayBuffer()))}catch(e){if(e instanceof DOMException&&e.name==='NotFoundError')return;throw e}
}
async function files(root:FileSystemDirectoryHandle,prefix=''):Promise<StorageNode[]>{
 const out:StorageNode[]=[];
 for await(const entry of entries(root)){
  const id=prefix+'/'+entry.name;
  if(entry.kind==='directory'){const children=await files(await root.getDirectoryHandle(entry.name),id);out.push({id,label:entry.name,children,bytes:sum(children)})}
  else out.push({id,label:entry.name,bytes:(await(await root.getFileHandle(entry.name)).getFile()).size,children:[]});
 }
 return out.sort((a,b)=>a.label.localeCompare(b.label));
}
const sum=(nodes:StorageNode[])=>nodes.reduce((n,node)=>n+node.bytes,0);
const fileInfo=(name:string):Pick<StorageNode,'label'|'type'>=>{
 if(name==='events.sods')return {label:'Events',type:'Datastore'};
 if(name==='events.idx')return {label:'Events',type:'Index'};
 if(name==='branch.sum')return {label:'Activity',type:'Summaries'};
 if(/^branch\.\d+\.tri$/.test(name))return {label:'Time',type:'Trie'};
 if(/^layout\./.test(name))return {label:'Storage',type:'Layout'};
 if(name==='resource.rx')return {label:'Resource',type:'Definition'};
 if(name==='state.tri')return {label:'State',type:'Trie'};
 if(/^collection.state/.test(name))return {label:'Collection',type:'State'};
 return {label:name.replace(/\.[^.]+$/,''),type:'File'};
};
const artifactKind=(kind:string)=>({definition:'Definition',resource:'Trie',state:'Trie',time:'Trie',summary:'Summaries'}[kind]??'File');
const branchLabel=(name:string)=>name==='read'?'Info - Read':name==='write'?'Info - Write':name.replace(/(^|[ /-])([a-z])/g,(_,gap,letter)=>gap+letter.toUpperCase());
async function catalogFiles(dir:FileSystemDirectoryHandle,node:StorageNode,namespace:string,catalog?:TrunkCatalog):Promise<StorageNode>{
 const descriptors=new Map<string,TrunkFile[]>();
 for(const file of catalog?.files??[]){if(!file.bootstrap||!file.digest)continue;const list=descriptors.get(file.digest)??[];list.push(file);descriptors.set(file.digest,list)}
 const blobs=await child(dir,'trunk-blobs');
 const children:StorageNode[]=[];
 for(const stored of node.children){
  const file={...stored,target:{kind:'catalog',namespace,digest:stored.label} as StorageTarget,impact:catalog?'Any local branches that depend on this artifact are also cleared. Other resources are kept.':'The catalog cannot identify dependencies. Clearing this file resets this connection’s local copies.'};
  const refs=descriptors.get(file.label);
  if(!refs?.length){children.push({...file,label:catalog?'Unreferenced Catalog File':'Unidentified Catalog File',note:catalog?'Not referenced by the current server catalog.':'Server catalog metadata is unavailable.'});continue}
  const kind=artifactKind(refs[0].kind),paths=[...new Set(refs.map(ref=>ref.path.replace(/^([^/]+\/[^/]+\/[^/]+)\/\d+\//,'$1/').split('/').slice(0,-1).join(' / ')))];
  let label=refs[0].kind==='resource'?'Resources':refs[0].kind==='state'?'State':refs[0].kind==='time'?'Time':kind,note=paths.join(' · ');
  if(refs[0].kind==='definition'&&blobs){
   try{
    const definition=await object(blobs,file.label);
    if(typeof definition?.definition==='string'&&definition.definition)label=definition.definition;
    const fields=Array.isArray(definition?.fields)?definition.fields.length:undefined;
    note=[fields===undefined?undefined:`${fields} ${fields===1?'field':'fields'}`,note].filter(Boolean).join(' · ');
   }catch{note=`Definition metadata unavailable · ${note}`}
  }
  children.push({...file,label,type:kind,note});
 }
 return {...node,label:'Catalog & Definitions',type:'Catalog',target:{kind:'catalog',namespace},impact:'The local catalog and branches that depend on it are cleared. They can be downloaded again.',children:children.sort((a,b)=>a.label.localeCompare(b.label))};
}
/** Inventory file lengths, including sparse reservations, without reading event payloads. */
export async function inspectLocalStorage(root:FileSystemDirectoryHandle,labels:Record<string,string>={}):Promise<StorageNode[]>{
 const nodes:StorageNode[]=[];const stores=await child(root,'snoot-store');
 if(stores){
  const contexts:StorageNode[]=[];
  for await(const entry of entries(stores)){
   if(entry.kind!=='directory'||!namespacePattern.test(entry.name))continue;
   const namespace=entry.name,dir=await stores.getDirectoryHandle(namespace),raw=await files(dir,namespace);
   let commit:any;
   try{commit=await object(dir,'commit.pil')}
   catch{
    const target:StorageTarget={kind:'replicas',namespace};
    contexts.push({id:namespace,label:labels[namespace]??'Unidentified Trunk',type:'Trunk',bytes:sum(raw),note:'Metadata unreadable. This replica can still be cleared.',target,children:[{id:namespace+'/unreadable',label:'Unreadable Local Copy',type:'Datastore',bytes:sum(raw),children:[],target,impact:'Clears all local files for this replica. Server data and profile settings are kept.'}]});
    continue;
   }
   // Reset epochs must survive deletion, but are not replicated user data.
   if(!Object.keys(commit?.sources??{}).length&&!raw.some(n=>n.label!=='commit.pil'&&n.bytes>0))continue;
   const context:StorageNode={id:namespace,label:labels[namespace]??'Unidentified Trunk',type:'Trunk',note:labels[namespace]?undefined:'Origin unavailable in this profile.',bytes:sum(raw),children:[],target:{kind:'replicas',namespace}};
   const branches=await child(dir,'branches');
   let catalog:TrunkCatalog|undefined;
   try{catalog=(await object(dir,'trunk.pil'))?.catalog}catch{/* Keep inventory and cleanup available when catalog metadata is unreadable. */}
   const metadata=raw.filter(n=>['trunk-blobs','trunk.pil','commit.pil'].includes(n.label));
   if(metadata.length){
    const blobs=raw.find(n=>n.label==='trunk-blobs')??{id:namespace+'/catalog',label:'trunk-blobs',bytes:0,children:[]};
    const group=await catalogFiles(dir,blobs,namespace,catalog);group.bytes=sum(metadata);
    const count=new Set(catalog?.sources.map(tap=>tap.split('/').slice(0,3).join('/'))??[]).size;
    group.note=catalog?`${count} ${count===1?'resource':'resources'} · ${catalog.sources.length} storage ${catalog.sources.length===1?'branch':'branches'}`:'Local catalog and cache bookkeeping';
    context.children.push(group);
   }
   for(const item of raw){
    if(metadata.includes(item))continue;
    if(item.label!=='branches'){
     context.children.push({...item,target:{kind:'replicas',namespace},impact:'This internal file belongs to the connection. Clearing it resets all local copies for this connection.'});continue;
    }
    for(const branch of item.children){
     const revision=commit?.sources?.[branch.label];let source:NativeSource|undefined;
     if(revision!==undefined&&branches){try{source=await object(await branches.getDirectoryHandle(branch.label),`layout.${revision}.pil`)}catch{/* Preserve an explicit clear target for unreadable branch metadata. */}}
     const scope=source?.tap.split('/').slice(0,3);
     if(!scope||scope.length!==3){context.children.push({...branch,label:'Unreferenced Branch',note:'Not referenced by the current local catalog.',target:{kind:'branch',namespace,key:branch.label},children:[]});continue}
     let parent=context;
     for(let depth=0;depth<3;depth++){
      const id=namespace+'/'+scope.slice(0,depth+1).join('/');let group=parent.children.find(n=>n.id===id);
      if(!group){group={id,label:scope[depth],type:['Cache','Service','Resource'][depth],bytes:0,children:[],target:{kind:'replicas',namespace,scope:scope.slice(0,depth+1)}};parent.children.push(group)}
      group.bytes+=branch.bytes;parent=group;
     }
     const target:StorageTarget={kind:'branch',namespace,key:branch.label};
     parent.children.push({...branch,target,type:'Branch',label:branchLabel(source!.tap.split('/').slice(3).join(' / '))||'Resource Files',note:source!.retiredBy?'Previous Copy':undefined,children:branch.children.filter(n=>!/^layout\.\d+\.pil$/.test(n.label)).map(n=>({...n,target,impact:`Clears the complete local branch (${source!.tap}) so its data and indexes stay consistent.`,...fileInfo(n.label)}))});
    }
   }
   // Keep cache identity in the deletion scope, without an extra navigation level.
   context.children=context.children.flatMap(node=>node.type==='Cache'?node.children.map(service=>({...service,note:node.label})): [node]);
   contexts.push(context);
  }
  if(contexts.length)nodes.push({id:'replicas',label:'Replicated Trunks',bytes:sum(contexts),children:contexts,target:{kind:'replicas'}});
 }
 for(const name of legacy){const dir=await child(root,name);if(dir){const children=await files(dir,name);const target=(node:StorageNode):StorageNode=>({...node,target:{kind:'legacy',name,path:node.id.split('/').slice(1)},children:node.children.map(target)});nodes.push({id:name,label:'Previous Local Store',note:name,bytes:sum(children),children:children.map(target),target:{kind:'legacy',name}})}}
 const targets=(node:StorageNode,parent?:StorageNode):StorageNode=>{const next={...node,target:node.target??parent?.target,impact:node.impact??(!node.target?parent?.impact:undefined)};return {...next,children:next.children.map(child=>targets(child,next))}};
 return nodes.map(node=>targets(node));
}
async function remove(root:FileSystemDirectoryHandle,name:string){try{await root.removeEntry(name,{recursive:true})}catch(e){if(!(e instanceof DOMException&&e.name==='NotFoundError'))throw e}}
/** Keep reset counters durable: in-flight imports from any tab must not republish deleted pointers. */
export async function clearReplicaContext(dir:FileSystemDirectoryHandle,scope?:string[],keys?:string[]){
 if(keys?.some(key=>!namespacePattern.test(key)))throw Error('Invalid saved branch');
 const all=!scope&&!keys;
 if(scope&&(scope.length<1||scope.length>3||scope.some(s=>!s||s.includes('/'))))throw Error('Invalid storage scope');
 let old:any;
 try{old=await object(dir,'commit.pil')}
 catch{
  if(!all)throw Error('Replica metadata is unreadable. Clear the entire trunk to remove its local files.');
  // No usable counter exists. A fresh 52-bit reset ticket avoids reusing zero
  // for work issued before this explicit reset of an unreadable replica.
  const nonce=crypto.getRandomValues(new Uint32Array(2)),serial=(nonce[0]&0xfffff)*2**32+nonce[1];
  await atomicFile(dir,'commit.pil',encodeObject({serial:1,sources:{},control:{serial,scopes:{}}}));
  for await(const entry of entries(dir))if(entry.name!=='commit.pil')await remove(dir,entry.name);
  return;
 }
 const control:NativeControl=old?.control??{serial:0,scopes:{}};
 if(!Number.isSafeInteger(control.serial+1)||!Number.isSafeInteger((old?.serial??0)+1))throw Error('Local reset sequence exhausted');
 const branches=await child(dir,'branches'),selected:string[]=[],scopes=new Set<string>();
 const sources:Record<string,number>={...old?.sources};
 for(const [key,revision] of Object.entries(sources)){
  if(!namespacePattern.test(key))throw Error('Invalid saved branch');
  const branch=branches&&await child(branches,key);let source:NativeSource|undefined;
  try{source=branch&&await object(branch,`layout.${revision}.pil`)}catch{/* Unreadable branches can still be explicitly cleared. */}
  if(all||keys?.includes(key)||scope&&source&&scope.every((value,i)=>source.tap.split('/')[i]===value)){
   selected.push(key);delete sources[key];if(source)scopes.add(source.tap.split('/').slice(0,3).join('/'));
  }
 }
 if(all)for(const key of Object.keys(control.scopes))scopes.add(key);
 control.serial++;
 for(const key of scopes){const next=(control.scopes[key]??0)+1;if(!Number.isSafeInteger(next))throw Error('Local reset sequence exhausted');control.scopes[key]=next}
 // Publish the reset epoch and remove pointers before reclaiming any data.
 await atomicFile(dir,'commit.pil',encodeObject({serial:(old?.serial??0)+1,sources,control}));
 if(!all){if(branches)for(const key of new Set([...selected,...keys??[]]))await remove(branches,key)}
 else for await(const entry of entries(dir))if(entry.name!=='commit.pil')await remove(dir,entry.name);
}

/** Invalidate dependent branches before removing shared catalog bytes. */
async function clearCatalog(dir:FileSystemDirectoryHandle,digest?:string){
 if(digest!==undefined&&!namespacePattern.test(digest))throw Error('Invalid catalog artifact');
 let saved:any;try{saved=await object(dir,'trunk.pil')}catch{/* Unreadable metadata cannot establish dependencies. */}
 const catalog:TrunkCatalog|undefined=saved?.catalog;
 if(!catalog){await clearReplicaContext(dir);return}
 const removed=catalog.files.filter(f=>!digest||f.digest===digest);
 const definitions=new Set(removed.filter(f=>f.kind==='definition').map(f=>f.definition??f.digest));
 const resources=removed.filter(f=>f.kind!=='definition').map(f=>f.path.split('/').slice(0,-1).filter((p,i)=>i!==3||!/^\d+$/.test(p)));
 const commit=await object(dir,'commit.pil'),branches=await child(dir,'branches'),keys:string[]=[];
 for(const [key,revision] of Object.entries(commit?.sources??{})){
  if(!namespacePattern.test(key))throw Error('Invalid saved branch');
  const branch=branches&&await child(branches,key);let source:NativeSource|undefined;
  try{source=branch&&await object(branch,`layout.${revision}.pil`)}catch{/* Unreadable branches can still be explicitly cleared. */}
  if(!source||definitions.has(source.digest)||resources.some(scope=>scope.every((part,i)=>source.tap.split('/')[i]===part)))keys.push(key);
 }
 await clearReplicaContext(dir,undefined,keys);
 if(!digest){await remove(dir,'trunk.pil');await remove(dir,'trunk-blobs');return}
 // An artifact shared by several catalog paths is removed from all of them.
 const bindings=(saved.bindings??[]).filter((b:{digest:string})=>!definitions.has(b.digest));
 await atomicFile(dir,'trunk.pil',encodeObject({...saved,bindings,catalog:{...catalog,files:catalog.files.filter(f=>f.digest!==digest),sources:catalog.sources.filter(tap=>bindings.some((b:{tap:string})=>b.tap===tap)),collections:catalog.collections?.filter(tap=>bindings.some((b:{tap:string})=>b.tap===tap))??[]}}));
 const blobs=await child(dir,'trunk-blobs');if(blobs)await remove(blobs,digest);
}

export async function clearLocalStorage(root:FileSystemDirectoryHandle,target:StorageTarget){
 if(target.kind==='legacy'){
  if(!legacy.includes(target.name))throw Error('Unknown local store');
  const parts=target.path??[];if(parts.some(p=>!p||p==='.'||p==='..'||p.includes('/')||p.includes('\\')))throw Error('Invalid storage path');
  if(!parts.length){await remove(root,target.name);return}
  let dir=await child(root,target.name);for(const part of parts.slice(0,-1)){if(!dir)return;dir=await child(dir,part)}
  if(dir)await remove(dir,parts.at(-1)!);return;
 }
 if(target.kind==='summaries')return;
 const stores=await child(root,'snoot-store');
 if(stores){
  if(target.kind==='branch'||target.kind==='catalog'){
   if(!namespacePattern.test(target.namespace))throw Error('Invalid saved connection');const dir=await child(stores,target.namespace);if(dir){if(target.kind==='branch')await clearReplicaContext(dir,undefined,[target.key]);else await clearCatalog(dir,target.digest)}
  }else if(target.kind==='replicas'&&target.namespace){if(!namespacePattern.test(target.namespace))throw Error('Invalid saved connection');const dir=await child(stores,target.namespace);if(dir)await clearReplicaContext(dir,target.scope)}
  else for await(const entry of entries(stores))if(entry.kind==='directory'&&namespacePattern.test(entry.name))await clearReplicaContext(await stores.getDirectoryHandle(entry.name));
 }
 if(target.kind==='all')for(const name of legacy)await remove(root,name);
}
export async function storageInventory(labels:Record<string,string>):Promise<StorageInventory>{
 if(!navigator.storage?.getDirectory||!navigator.locks)throw Error('Local storage management is unavailable in this browser.');
 const nodes=await navigator.locks.request('trie-trunk-files-v1',async()=>inspectLocalStorage(await navigator.storage.getDirectory(),labels));
 if(typeof caches!=='undefined'&&(await caches.keys()).includes('snoot-summary-days')){
  const cache=await caches.open('snoot-summary-days');let bytes=0;const keys=await cache.keys();
  for(const key of keys){const response=await cache.match(key);if(response){const size=Number(response.headers.get('Content-Length'));bytes+=Number.isFinite(size)&&size>0?size:(await response.blob()).size}}
  nodes.push({id:'summary-responses',label:'Cached Summary Responses',bytes,children:[],note:`${keys.length} packets`,target:{kind:'summaries'}});
 }
 const estimate=await navigator.storage.estimate();return {nodes,bytes:sum(nodes),usage:estimate.usage,quota:estimate.quota};
}
export async function clearStorage(target:StorageTarget){
 await navigator.locks.request('trie-trunk-files-v1',async()=>{
  await clearLocalStorage(await navigator.storage.getDirectory(),target);
  if((target.kind==='all'||target.kind==='summaries')&&typeof caches!=='undefined')await caches.delete('snoot-summary-days');
 });
}
export function storageBytes(bytes:number){if(!Number.isFinite(bytes))return 'Unavailable';if(bytes===0)return '0 B';const units=['B','KB','MB','GB','TB'];const index=Math.min(4,Math.floor(Math.log10(Math.max(1,bytes))/3));return `${new Intl.NumberFormat(undefined,{maximumFractionDigits:index?2:0}).format(bytes/1000**index)} ${units[index]}`}
