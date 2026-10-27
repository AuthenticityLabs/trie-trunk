import { credentialScope, readTab, writeTab } from '../services/search-session';
import { Show, createEffect, createSignal, onCleanup, type JSX } from "solid-js";
import { lostEventsAvailable } from "../services/rejections";

export function AlertCenter(props:{token:string;children:JSX.Element;lostEvents:()=>JSX.Element}) {
  const [available,setAvailable]=createSignal(false);
  const [selected,setSelected]=createSignal(false);
  let sessionKey='';
  const select=(value:boolean)=>{setSelected(value);if(sessionKey)writeTab(sessionKey,value)};
  const [error,setError]=createSignal("");
  createEffect(()=>{
    const token=props.token;
    setAvailable(false);setSelected(false);setError("");sessionKey='';
    let active:AbortController|undefined,disposed=false;
    void credentialScope(token).then(scope=>{if(!disposed){sessionKey=`snoot:alerts:view:${scope}`;setSelected(readTab<boolean>(sessionKey)===true)}});
    async function check(){
      if(active||disposed)return;
      const controller=new AbortController();active=controller;
      try{
        const exists=await lostEventsAvailable(token,controller.signal);
        if(disposed)return;
        setAvailable(exists);setError("");
        if(!exists)select(false);
      }catch(cause){if(!disposed)setError(cause instanceof Error?cause.message:String(cause))}
      finally{if(active===controller)active=undefined}
    }
    const refresh=()=>{if(!document.hidden)void check()};
    void check();
    window.addEventListener("focus",refresh);
    const interval=setInterval(refresh,30000);
    onCleanup(()=>{disposed=true;active?.abort();clearInterval(interval);window.removeEventListener("focus",refresh)});
  });
  return <>
    <Show when={error()}><div class="notice error-notice" role="alert">{error()}</div></Show>
    <Show when={available()}>
      <div class="view-tabs" role="tablist" aria-label="Alert Categories" onKeyDown={event=>{
        if(!["ArrowLeft","ArrowRight","Home","End"].includes(event.key))return;
        event.preventDefault();
        const lost=event.key==="End"?true:event.key==="Home"?false:!selected();
        select(lost);
        event.currentTarget.querySelector<HTMLButtonElement>(lost?"#alerts-lost-tab":"#alerts-overview-tab")?.focus();
      }}>
        <button type="button" role="tab" id="alerts-overview-tab" aria-controls="alerts-panel" tabIndex={selected()?-1:0} aria-selected={!selected()} classList={{active:!selected()}} onClick={()=>select(false)}>Overview</button>
        <button type="button" role="tab" id="alerts-lost-tab" aria-controls="alerts-panel" tabIndex={selected()?0:-1} aria-selected={selected()} classList={{active:selected()}} onClick={()=>select(true)}>Lost Events</button>
      </div>
    </Show>
    <div id="alerts-panel" role={available()?"tabpanel":undefined} aria-labelledby={available()?(selected()?"alerts-lost-tab":"alerts-overview-tab"):undefined}>
      <Show when={available()&&selected()} fallback={props.children}>{props.lostEvents()}</Show>
    </div>
  </>;
}
