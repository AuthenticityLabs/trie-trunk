import {For,Show,createMemo,createSignal,onMount,onCleanup} from 'solid-js';
import {storageInventory,storageBytes,type StorageNode,type StorageInventory} from '../services/local-storage';
import {clearStorage} from '../services/storage-clear-client';
import {connectionScope,type ServerConnection} from '../services/connections';

export function ProfileStorage(props:{connections:ServerConnection[];tokens:Record<string,string>;onClearStart:()=>void}){
 const [inventory,setInventory]=createSignal<StorageInventory>(),[busy,setBusy]=createSignal(false),[error,setError]=createSignal('');
 const [path,setPath]=createSignal<string[]>([]),[filter,setFilter]=createSignal(''),[pending,setPending]=createSignal<{node:StorageNode;location:string}>();
 let confirmation:HTMLDivElement|undefined;
 let disposed=false;onCleanup(()=>{disposed=true});
 const root=createMemo<StorageNode>(()=>({id:'all',label:'Local Storage',bytes:inventory()?.bytes??0,target:{kind:'all'},children:(inventory()?.nodes??[]).flatMap(n=>n.id==='replicas'?n.children:[n])}));
 const trail=createMemo(()=>{const out=[root()];for(const id of path()){const next=out.at(-1)!.children.find(n=>n.id===id);if(!next)break;out.push(next)}return out});
 const current=()=>trail().at(-1)!;
 const rows=createMemo(()=>current().children.filter(n=>`${n.label} ${n.type??''} ${n.note??''}`.toLowerCase().includes(filter().trim().toLowerCase())).sort((a,b)=>Number(b.children.length>0)-Number(a.children.length>0)||a.label.localeCompare(b.label)));
 function navigate(ids:string[]){setPath(ids);setFilter('');setPending(undefined)}
 function select(node:StorageNode){setPending({node,location:[...trail().map(n=>n.label),...(node.id===current().id?[]:[node.label])].join(' / ')});queueMicrotask(()=>{confirmation?.scrollIntoView({block:'center'});confirmation?.focus({preventScroll:true})})}
 async function refresh(){setBusy(true);setError('');try{
  const labels:Record<string,string>={};
  for(const connection of props.connections){const key=connectionScope(connection,props.tokens[connection.id]??'');const hash=await crypto.subtle.digest('SHA-256',new TextEncoder().encode(key));let name=connection.name;
   if(name.toLowerCase()==='local server'){
    let endpoint=connection.url||location.origin;
    if(!connection.url&&document.documentElement.hasAttribute('data-snoot-desktop')){try{const response=await fetch('/__desktop/connection');if(response.ok){const value=await response.json();if(typeof value.endpoint==='string'&&value.endpoint)endpoint=value.endpoint}}catch{/* Origin remains an honest fallback. */}}
    name=new URL(endpoint).host;
   }
   labels[Array.from(new Uint8Array(hash),n=>n.toString(16).padStart(2,'0')).join('')]=name}
  const result=await storageInventory(labels);if(!disposed){setInventory(result);setPending(undefined)}
 }catch(cause){if(!disposed)setError(cause instanceof Error?cause.message:String(cause))}finally{if(!disposed)setBusy(false)}}
 onMount(()=>void refresh());
 async function clear(){
  const selection=pending();if(!selection?.node.target||busy())return;
  setBusy(true);setError('');
  try{
   if(selection.node.target.kind==='searches'){await clearStorage(selection.node.target);await refresh();return}
   props.onClearStart();await clearStorage(selection.node.target);const channel=new BroadcastChannel('snoot-storage-reset');channel.postMessage('reload');channel.close();sessionStorage.setItem('snoot:profile:tab','storage');location.reload()
  }
  catch(cause){setError(cause instanceof Error?cause.message:String(cause));setBusy(false)}
 }
 return <section id="profile-storage-panel" role="tabpanel" aria-labelledby="profile-storage-tab" class="card profile-card storage-panel">
  <div class="storage-heading"><div><h3>Local Storage</h3><p>Browse replicated trunks, services, resources, and branches.</p></div><button class="text-button" type="button" disabled={busy()} onClick={()=>void refresh()}>Refresh</button></div>
  <Show when={error()}><p class="notice error-notice" role="alert">{error()}</p></Show>
  <Show when={busy()}><p role="status">Working…</p></Show>
  <Show when={inventory()}>{data=><>
   <div class="storage-totals"><div><span>Local Copies</span><strong>{storageBytes(data().bytes)}</strong></div><Show when={data().usage!==undefined}><div><span>App Storage Used</span><strong>{storageBytes(data().usage!)}</strong></div></Show><Show when={data().quota!==undefined}><div><span>Storage Limit</span><strong>{storageBytes(data().quota!)}</strong></div></Show></div>
   <nav class="storage-breadcrumbs" aria-label="Storage Location"><For each={trail()}>{(node,index)=><><Show when={index()>0}><span aria-hidden="true">/</span></Show><button class="text-button" type="button" disabled={busy()} aria-current={node.id===current().id?'location':undefined} onClick={()=>navigate(trail().slice(1,index()+1).map(n=>n.id))}>{node.label}</button></>}</For></nav>
   <div class="storage-browser-heading"><div><h4>{current().id==='all'?'Replicated Trunks':current().label}</h4><span>{storageBytes(current().bytes)} · {current().children.length} {current().children.length===1?'Item':'Items'}</span></div><button class="text-button" type="button" disabled={busy()} onClick={()=>select(current())}>{current().id==='all'?'Clear All Local Caches':'Clear This Folder'}</button></div>
   <Show when={pending()}>{selection=><div ref={confirmation} tabIndex={-1} class="storage-confirm" role="region" aria-label="Confirm Local Deletion">
    <h4>Clear Local Copy?</h4><Show when={selection().node.type}><span class="storage-type">{selection().node.type}</span></Show><p class="storage-confirm-path">{selection().location}</p><Show when={selection().node.note}><p>{selection().node.note}</p></Show><Show when={selection().node.impact}><p>{selection().node.impact}</p></Show><p>Server data and profile settings are kept. Snoot will reload.</p>
    <div><button class="button secondary" type="button" disabled={busy()} onClick={()=>setPending(undefined)}>Cancel</button><button class="button primary" type="button" disabled={busy()} onClick={()=>void clear()}>Clear Local Copy</button></div>
   </div>}</Show>
   <label class="storage-filter"><span class="sr-only">Find in This Folder</span><input type="search" placeholder="Find in This Folder" value={filter()} onInput={event=>setFilter(event.currentTarget.value)}/></label>
   <ul class="storage-browser" aria-label={`Contents of ${current().label}`}><For each={rows()}>{node=><li class="storage-item" data-storage-id={node.id}>
    <Show when={node.children.length} fallback={<div class="storage-entry"><span class="storage-entry-icon" aria-hidden="true">·</span><span class="storage-name"><span class="storage-label">{node.label}<Show when={node.type}><span class="storage-type">{node.type}</span></Show></span><Show when={node.note}><span class="storage-description">{node.note}</span></Show></span></div>}>
     <button class="storage-entry storage-open" type="button" disabled={busy()} aria-label={`Open ${node.label}`} onClick={()=>navigate([...trail().slice(1).map(n=>n.id),node.id])}><span class="storage-entry-icon" aria-hidden="true">›</span><span class="storage-name"><span class="storage-label">{node.label}<Show when={node.type}><span class="storage-type">{node.type}</span></Show></span><span class="storage-description">{node.note??`${node.children.length} ${node.children.length===1?'Item':'Items'}`}</span></span></button>
    </Show>
    <span class="storage-size">{storageBytes(node.bytes)}</span><button class="text-button storage-clear" type="button" disabled={busy()} aria-label={`Clear ${node.label}${node.note?`: ${node.note}`:''}`} onClick={()=>select(node)}>Clear</button>
   </li>}</For></ul>
   <Show when={!rows().length}><p class="storage-empty">{filter()?'No matching items.':'No local copies in this folder.'}</p></Show>
   <div class="storage-preserved"><span>Profile Settings</span><span>Kept</span></div>
  </>}</Show>
 </section>;
}
