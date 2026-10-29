import {For,Show,createEffect,createMemo,createSignal,on} from 'solid-js';
import {ProfileStorage} from './profile-storage';
import {Icon} from './icon';
import {DesktopConnection} from './desktop-connection';
import {pageSizes} from '../services/preferences';
import type {UserProfile} from '../services/profile';
import type {ServerConnection} from '../services/connections';
export type ProfileTab='preferences'|'connections'|'storage';
export default function Profile(props:{profile:UserProfile;tokens:Record<string,string>;tab:ProfileTab;onTab:(tab:ProfileTab)=>void;onSave:(profile:UserProfile,tokens?:Record<string,string>)=>Promise<void>;loading?:boolean;loadError?:string;onReload?:()=>void;statuses?:Record<string,{loading:boolean;error?:string;trunk?:{definitions:unknown[]}}>;onStorageClearStart:()=>void}){
 const zones=Array.from(new Set(['UTC',Intl.DateTimeFormat().resolvedOptions().timeZone,...Intl.supportedValuesOf('timeZone')]));
 const [draft,setDraft]=createSignal(structuredClone(props.profile));
 const [tokens,setTokens]=createSignal({...props.tokens});
 const [busy,setBusy]=createSignal(false),[error,setError]=createSignal(''),[message,setMessage]=createSignal('');
 let dirty=false;
 createEffect(on(()=>props.profile,value=>{if(!dirty){setDraft(structuredClone(value));setTokens({...props.tokens})}}));
 const edit=(values:Partial<UserProfile>)=>{dirty=true;setDraft(p=>({...p,...values}));setMessage('');};
 const editConnection=(id:string,values:Partial<ServerConnection>)=>edit({connections:draft().connections.map(c=>c.id===id?{...c,...values}:c)});
 async function save(){
  if(busy()||props.loading)return;
  setBusy(true);setError('');setMessage('');
  try{const value=structuredClone(draft());await props.onSave(value,tokens());dirty=false;setDraft(structuredClone(value));setMessage('Saved locally.');}
  catch(cause){setError(cause instanceof Error?cause.message:String(cause))}finally{setBusy(false)}
 }
 return <section class="profile-page" aria-label="User Profile">
  <div class="page-intro"><div><h2>Profile</h2><p>{props.profile.name}</p></div></div>
  <Show when={props.loading}><p role="status">Loading profile…</p></Show>
  <Show when={props.loadError}><p class="notice error-notice" role="alert">{props.loadError} <button type="button" class="text-button" disabled={props.loading} onClick={props.onReload}>Retry profile</button></p></Show>
  <div class="view-tabs" role="tablist" aria-label="Profile" onKeyDown={event=>{
   if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;
   event.preventDefault();const tabs=['preferences','connections','storage'] as const,index=tabs.indexOf(props.tab);const tab=tabs[event.key==='Home'?0:event.key==='End'?2:(index+(event.key==='ArrowRight'?1:2))%3];props.onTab(tab);document.getElementById(`profile-${tab}-tab`)?.focus();
  }}><For each={['preferences','connections','storage'] as const}>{tab=><button id={`profile-${tab}-tab`} type="button" role="tab" disabled={busy()} aria-selected={props.tab===tab} aria-controls={`profile-${tab}-panel`} tabIndex={props.tab===tab?0:-1} classList={{active:props.tab===tab}} onClick={()=>{props.onTab(tab);setMessage('');setError('')}}>{tab==='preferences'?'Preferences':tab==='connections'?'Connections':'Storage'}</button>}</For></div>
  <form hidden={props.tab==='storage'} class="card profile-card" onSubmit={event=>{event.preventDefault();void save()}}>
   <div id="profile-preferences-panel" role="tabpanel" aria-labelledby="profile-preferences-tab" hidden={props.tab!=='preferences'}>
    <label class="form-label">Name<input required maxlength={128} value={draft().name} disabled={busy()||props.loading} onInput={e=>edit({name:e.currentTarget.value})}/></label>
    <label class="form-label">Time Zone<select value={draft().timeZone} disabled={busy()||props.loading} onChange={e=>edit({timeZone:e.currentTarget.value})}><For each={zones}>{zone=><option value={zone}>{zone}</option>}</For></select></label>
    <label class="form-label">Appearance<select value={draft().appearance} disabled={busy()||props.loading} onChange={e=>edit({appearance:e.currentTarget.value as UserProfile['appearance']})}><option value="light">Light</option><option value="dark">Dark</option><option value="auto">System</option></select></label>
    <label class="form-label">Theme<select value={draft().palette} disabled={busy()||props.loading} onChange={e=>edit({palette:e.currentTarget.value as UserProfile['palette']})}><option value="trunk">Trunk</option><option value="snoot">Snoot</option></select></label>
    <label class="form-label">Events per page<select value={draft().pageSize} disabled={busy()||props.loading} onChange={e=>edit({pageSize:Number(e.currentTarget.value)})}><For each={pageSizes}>{size=><option value={size}>{size}</option>}</For></select></label>
   </div>
   <div id="profile-connections-panel" role="tabpanel" aria-labelledby="profile-connections-tab" hidden={props.tab!=='connections'}>
    <p>Enabled servers supply the resource catalog and definitions when the workspace opens.</p>
    <For each={draft().connections.map(c=>c.id)}>{id=>{const connection=createMemo(()=>draft().connections.find(c=>c.id===id)!);return <fieldset class="profile-connection" disabled={busy()||props.loading}>
     <legend>{connection().name||'Server connection'}</legend>
     <label class="form-label">Name<input required maxlength={128} value={connection().name} onInput={e=>editConnection(id,{name:e.currentTarget.value})}/></label>
     <label class="form-label">Server address<input type="url" value={connection().url} placeholder={location.origin} onInput={e=>{editConnection(id,{url:e.currentTarget.value});setTokens(current=>({...current,[id]:''}));}}/><small>Leave empty to use this app’s server.</small></label>
     <label class="form-label">Bearer token<input type="password" value={tokens()[id]??''} autocomplete="off" spellcheck={false} onInput={e=>{dirty=true;setTokens(current=>({...current,[id]:e.currentTarget.value}));}}/><small>Kept in memory for this tab. Each server has its own token.</small></label>
     <div class="profile-connection-actions"><label><input type="checkbox" checked={connection().enabled} onChange={e=>editConnection(id,{enabled:e.currentTarget.checked})}/> Enabled</label>
      <button type="button" class="text-button" onClick={()=>edit({connections:draft().connections.filter(c=>c.id!==id)})}>Remove</button></div>
     <Show when={props.statuses?.[id]}>{status=><p class="profile-connection-status" role="status">{status().loading?'Connecting…':status().error??`${status().trunk?.definitions.length??0} definitions received`}</p>}</Show>
    </fieldset>}}</For>
    <button type="button" class="button secondary" disabled={busy()||props.loading} onClick={()=>edit({connections:[...draft().connections,{id:crypto.randomUUID(),name:'New server',url:'',enabled:true}]})}><Icon name="plus" size={15}/> Add connection</button>
    <Show when={document.documentElement.hasAttribute('data-snoot-desktop')}><div class="profile-desktop"><h3>Desktop transport</h3><DesktopConnection/></div></Show>
   </div>
   <div class="profile-save"><button class="button primary" type="submit" disabled={busy()||props.loading}>{busy()?'Saving…':'Save profile'}</button><Show when={message()}><span role="status">{message()}</span></Show></div>
   <Show when={error()}><p class="notice error-notice" role="alert">{error()}</p></Show>
  </form>
  <Show when={props.tab==='storage'}><ProfileStorage connections={props.profile.connections} tokens={props.tokens} onClearStart={props.onStorageClearStart}/></Show>
 </section>;
}
