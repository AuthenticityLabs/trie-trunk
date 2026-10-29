import { For, Show, createEffect, createSignal, createUniqueId, on, onCleanup, onMount } from 'solid-js';
import { eventId, expandEvent, jsonText, type LogEvent } from '../services/dictionary';
import { field } from '../services/events';
import type { DiagnosticTarget } from '../services/diagnostics';
import { loadView } from '../services/pil/client';
import { Icon } from './icon';

export default function DiagnosticDialog(props: {target:DiagnosticTarget;token:string;onClose:()=>void}) {
  let dialog!:HTMLDialogElement;
  let abort:AbortController|undefined;
  const title=createUniqueId();
  const opener=document.activeElement instanceof HTMLElement?document.activeElement:undefined;
  const [records,setRecords]=createSignal<LogEvent[]>([]);
  const [next,setNext]=createSignal<string>();
  const [busy,setBusy]=createSignal(false),[error,setError]=createSignal('');
  const name=()=>props.target.kind==='debug'?'Debug':'Trace';
  async function load(after?:string) {
    if(busy())return;
    const controller=new AbortController();abort=controller;
    setBusy(true);setError('');
    if(!after){setRecords([]);setNext(undefined)}
    try {
      const page=await loadView({...props.target.query,after},props.token,controller.signal);
      if(!controller.signal.aborted){setRecords(rows=>after?[...rows,...page.events]:page.events);setNext(page.result.next)}
    } catch(cause) {
      if(!controller.signal.aborted)setError(cause instanceof Error?cause.message:String(cause));
    } finally {if(abort===controller){abort=undefined;setBusy(false)}}
  }
  onMount(()=>dialog.showModal());
  createEffect(on(()=>props.token,()=>{abort?.abort();abort=undefined;setBusy(false);void load()}));
  onCleanup(()=>{abort?.abort();dialog.close();if(opener?.isConnected)opener.focus({preventScroll:true})});
  return <dialog ref={dialog} class="modal diagnostics-modal" aria-labelledby={title}
    onCancel={event=>{event.preventDefault();props.onClose()}}
    onClick={event=>{if(event.target===dialog)props.onClose()}}>
    <div class="modal-body">
      <div class="modal-heading"><h2 id={title}><Icon name="leaf"/> {name()} Data</h2>
        <button type="button" class="icon-button" autofocus aria-label={`Close ${name()} Data`} onClick={props.onClose}><Icon name="close"/></button>
      </div>
      <p class="diagnostic-link-context">{props.target.source}<br/>Linked by {props.target.linkedBy}: <code>{props.target.value}</code></p>
      <div class="diagnostic-toolbar"><span role="status">{busy()?'Loading…':`${records().length.toLocaleString()} records loaded`}</span>
        <button type="button" class="text-button" disabled={busy()} onClick={()=>void load()}>Refresh</button>
      </div>
      <Show when={error()}><div class="notice error-notice" role="alert">{error()}</div></Show>
      <div class="diagnostic-records">
        <For each={records()}>{event=><details class="diagnostic-record">
          <summary><time dateTime={new Date(event.millis).toISOString()}>{new Date(event.millis).toISOString().replace('T',' ').replace('Z',' UTC')}</time>
            <span>{field(event,'message')||eventId(event)}</span></summary>
          <pre>{jsonText(expandEvent(event),2)}</pre>
        </details>}</For>
        <Show when={!busy()&&!error()&&!records().length}><p>No {props.target.kind} data matches this link.</p></Show>
      </div>
      <Show when={next()}><button type="button" class="button secondary" disabled={busy()} onClick={()=>void load(next())}>Load More</button></Show>
    </div>
  </dialog>;
}
