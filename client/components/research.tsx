import { ChangeHistory } from '../services/history-diff';
import { credentialScope } from '../services/search-session';
import { readResearch, writeResearch, type ResearchDraft, type ResearchState } from '../services/research-session';
import { capturePosition, resolvePosition } from '../services/ui-position';
import { For, Show, createEffect, createSignal, onCleanup, on, onMount, createUniqueId } from "solid-js";
import { streamObjectHistory } from "../services/pil/client";
import { streamRejections, type RejectedEvent } from "../services/rejections";
import { validateObject, type ObservedState, type ObjectQuery } from "../services/replay";
import { eventId, type LogEvent } from "../services/dictionary";
import { type EventField } from "../services/indicators";
import { validSource } from "../services/source";
import { Icon } from "./icon";
import { HistoryEntry } from "./history-entry";
import { ScrollEnd } from "./scroll-end";
import { NotebookPicker } from "./notebook";

export type ResearchMode = "Object History" | "Lost Events";
export type ResearchProps = {mode:ResearchMode;token:string;sources:string[];initialSource:string;initialObject?:ObjectQuery;onObjectOpened?:()=>void;pageSize:number;onIndicator:(field:EventField)=>void;indicatorsLoading:boolean;transient?:boolean};
export default function Research(props:ResearchProps) {
  const history=()=>props.mode==="Object History";
  const options=()=>[...new Set(props.sources.filter(s=>validSource(s)&&s.split("/").length===4)
    .map(s=>history()?s.split("/").slice(0,3).join("/")+"/write":s))];
  const initial=()=>history()?props.initialSource.split("/").slice(0,3).join("/")+"/write":props.initialSource;
  const [source,setSource]=createSignal(options().includes(initial())?initial():"");
  const [id,setID]=createSignal("");
  const [from,setFrom]=createSignal(new Date(Date.now()-7*86400000).toISOString().slice(0,16));
  const [to,setTo]=createSignal(new Date().toISOString().slice(0,16));
  const [events,setEvents]=createSignal<LogEvent[]>([],{equals:false});
  const [rejections,setRejections]=createSignal<RejectedEvent[]>([],{equals:false});
  const [busy,setBusy]=createSignal(false),[error,setError]=createSignal("");
  const [finished,setFinished]=createSignal(false),[requested,setRequested]=createSignal("");
  const [observed,setObserved]=createSignal<ObservedState>(),[validation,setValidation]=createSignal("");
  const [visibleCount,setVisibleCount]=createSignal(props.pageSize),[selected,setSelected]=createSignal<LogEvent>();
  const [notebook,setNotebook]=createSignal<{event:LogEvent;anchor:HTMLElement}>();
  const [saved,setSaved]=createSignal<{event:LogEvent;name:string}>();
  let changes=new ChangeHistory();
  let archive:HTMLDivElement|undefined;
  const latest=()=>events().at(-1);
  const earlier=()=>events().slice(0,-1);
  let abort:AbortController|undefined;
  const count=()=>history()?events().length:rejections().length;
  const slice=<T,>(rows:T[])=>rows.slice(0,visibleCount());
  let scroll: HTMLDivElement | undefined;
  function cancel(){abort?.abort();abort=undefined;cancelAnimationFrame(restoreFrame);restoring=false;setBusy(false);}
  let scope='',generation=0,submitted:ResearchDraft|undefined;
  const [sessionReady,setSessionReady]=createSignal(false);
  let restoreFrame=0;
  let restoring=false,position={top:0,left:0},timer:ReturnType<typeof setTimeout>|undefined;
  const draft=():ResearchDraft=>({source:source(),id:id(),from:from(),to:to()});
  function applyDraft(value:ResearchDraft){setSource(value.source);setID(value.id);setFrom(value.from);setTo(value.to)}
  function checkpoint(){
    if(props.transient||!scope||restoring)return;
    if(scroll?.getClientRects().length)position=capturePosition(scroll);
    writeResearch(props.mode,scope,{draft:draft(),query:submitted,visible:visibleCount(),selected:selected()?eventId(selected()!):undefined,position,archiveLeft:archive?.scrollLeft??0});
  }
  createEffect(on(()=>props.token,token=>{
    const current=++generation;cancel();scope='';restoring=true;submitted=undefined;setSessionReady(false);
    setEvents([]);setRejections([]);setObserved(undefined);setSelected(undefined);setFinished(false);setRequested('');setError('');setValidation('');
    void credentialScope(token).then(async value=>{
      if(current!==generation)return;
      scope=value;
      const saved=props.transient?undefined:readResearch(props.mode,scope);
      if(saved){applyDraft(saved.draft);position=saved.position;}
      const explicit=props.initialObject;
      setSessionReady(true);
      if(explicit)return; // Explicit ID clicks take precedence over the saved research.
      if(saved?.query)await load(saved.query,saved);
      else {restoring=false;checkpoint()}
    });
  }));
  createEffect(on(()=>[props.initialObject,sessionReady()] as const,([object,ready])=>{
    if(!object||!ready||!history())return;
    applyDraft({source:object.source,id:object.id,from:'1970-01-01T00:00:00',to:new Date().toISOString().slice(0,-1)});
    void load();props.onObjectOpened?.();
  }));
  createEffect(()=>{draft();visibleCount();selected();checkpoint()});
  const flush=()=>{clearTimeout(timer);checkpoint()};
  addEventListener('pagehide',flush);
  onCleanup(()=>{flush();generation++;cancel();removeEventListener('pagehide',flush)});
  async function load(query=draft(),saved?:ResearchState){
    const current=generation;
    if(props.transient&&history()){
      query={...query,to:new Date().toISOString().slice(0,-1)};
      setTo(query.to);
    }
    cancel();changes=new ChangeHistory();submitted={...query};restoring=!!saved;position=saved?.position??{top:0,left:0};
    const controller=new AbortController();abort=controller;
    setBusy(true);setError("");setFinished(false);setObserved(undefined);setValidation("");setVisibleCount(props.pageSize);scroll?.scrollTo({top:0});setSelected(undefined);
    setEvents([]);setRejections([]);
    setRequested(history()?`${query.source.split("/").slice(0,3).join("/")} · ${query.id}`:query.source||"All Sources");
    try{
      if(history()){
        const state=await streamObjectHistory({source:query.source,id:query.id,from:new Date(query.from+"Z").toISOString(),to:new Date(query.to+"Z").toISOString()},props.token,controller.signal,
          batch=>{if(!controller.signal.aborted)setEvents(rows=>{for(const event of batch){changes.append(event);rows.push(event);}return rows;});});
        if(!controller.signal.aborted)setObserved(state);
      }else{
        await streamRejections(query.source,props.token,controller.signal,
          batch=>{if(!controller.signal.aborted)setRejections(rows=>{for(const event of batch)rows.push(event);return rows;});});
      }
      if(!controller.signal.aborted)setFinished(true);
    }catch(cause){if(!controller.signal.aborted)setError(cause instanceof Error?cause.message:String(cause));}
    finally{if(abort===controller){
      abort=undefined;setBusy(false);
      if(saved){
        setVisibleCount(saved.visible);setSelected(events().find(e=>eventId(e)===saved.selected));
        restoreFrame=requestAnimationFrame(()=>{if(current!==generation)return;if(scroll)scroll.scrollTo(resolvePosition(saved.position,scroll));if(archive)archive.scrollLeft=saved.archiveLeft??0;restoring=false;checkpoint()});
      }else{restoring=false;checkpoint()}
    }}
  }
  async function check(){
    const state=observed();if(!state)return;
    cancel();const controller=new AbortController();abort=controller;setBusy(true);setValidation("");setError("");
    try{
      const current=await validateObject(state,props.token,controller.signal);
      if(!controller.signal.aborted)setValidation(current?"No new invalidations since this history was loaded.":"New invalidation received. Refresh the full object from its origin before replay.");
    }catch(cause){if(!controller.signal.aborted)setError(cause instanceof Error?cause.message:String(cause));}
    finally{if(abort===controller){abort=undefined;setBusy(false);}}
  }
  return <div class="inspection-page">
    <Show when={!props.transient}><form class="card query-card" aria-label={props.mode} onSubmit={e=>{e.preventDefault();void load();}}>
      <div class="query-heading"><strong>{props.mode}</strong></div>
      <div class="query-controls">
        <label class="control"><span>{history()?"Resource":"Source"}</span>
          <select aria-label={history()?"History Resource":"Rejection Source"} value={source()} disabled={busy()} required={history()} onChange={e=>setSource(e.currentTarget.value)}>
            <option value="">{history()?"Choose a Resource":"All Sources"}</option>
            <For each={options()}>{value=><option value={value}>{history()?value.split("/").slice(0,3).join("/"):value}</option>}</For>
          </select>
        </label>
        <Show when={history()}>
          <label class="control"><span>Object ID</span><input aria-label="Object ID" required value={id()} disabled={busy()} onInput={e=>setID(e.currentTarget.value)} spellcheck={false}/></label>
          <label class="control"><span>From · UTC</span><input aria-label="History from UTC" type="datetime-local" step="any" required value={from()} disabled={busy()} onInput={e=>setFrom(e.currentTarget.value)}/></label>
          <label class="control"><span>To · UTC</span><input aria-label="History to UTC" type="datetime-local" step="any" required value={to()} disabled={busy()} onInput={e=>setTo(e.currentTarget.value)}/></label>
        </Show>
        <button class="button primary" type="submit" disabled={busy()}>{history()?"Load History":"Load Lost Events"}</button>
        <Show when={busy()}><button class="button secondary" type="button" onClick={cancel}>Stop</button></Show>
      </div>
      <Show when={history()}><p class="helper">Retained changes only. Omitted fields are unchanged; supplied null values remain null.</p></Show>
    </form></Show>
    <Show when={error()}><div class="notice error-notice" role="alert">{error()}</div></Show>
    <Show when={requested()}>
      <section class="card results-card" aria-label={`${props.mode} results`}>
        <div class="results-toolbar"><div><strong>{count().toLocaleString()} {history()?(count()===1?"event":"events"):(count()===1?"lost event":"lost events")}</strong><div class="inspection-source">{requested()}</div></div>
          <Show when={props.transient}><button class="text-button" disabled={busy()} onClick={()=>void load()}>Refresh</button></Show>
          <span role="status">{busy()?"Loading…":finished()?"Complete":"Partial results"}</span>
          <Show when={observed()}><button class="text-button" disabled={busy()} onClick={()=>void check()}>Check Invalidations</button></Show>
        </div>
        <Show when={validation()}><p class="inspection-status" role="status">{validation()}</p></Show>
        <Show when={!count()&&!busy()&&finished()}><p class="inspection-status">No {history()?"retained writes":"lost events"} found.</p></Show>
        <div class="table-scroll" classList={{'history-scroller':history()}} ref={scroll} onScroll={()=>{clearTimeout(timer);timer=setTimeout(checkpoint,150)}}>
        <Show when={history()} fallback={<div class="inspection-list"><For each={slice(rejections())}>{record=><details class="inspection-record">
          <summary><span>{record.received_at.replace("T"," ")}</span><strong>{record.reason.replaceAll("_"," ")}</strong><code>{record.event_id}</code></summary>
          <p>{record.source} · definition {record.definition_version}</p><p>Finalized before {record.finalized_before}</p>
          <pre>{record.event_json}</pre>
        </details>}</For></div>}>
          <div class="history-trail" aria-label="Revision Trail" onKeyDown={e=>{
            if(!['ArrowRight','ArrowLeft','ArrowDown','ArrowUp','Home','End'].includes(e.key)||!(e.target instanceof HTMLButtonElement)||!e.target.classList.contains('history-toggle'))return;
            const toggles=Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('.history-toggle'));
            const index=toggles.indexOf(e.target),next=e.key==='Home'?0:e.key==='End'?toggles.length-1:(index+(['ArrowRight','ArrowDown'].includes(e.key)?1:-1)+toggles.length)%toggles.length;
            e.preventDefault();toggles[next]?.focus();
          }}>
            <Show when={earlier().length}><div class="history-archive" ref={archive} aria-label="Earlier Revisions"
              onScroll={()=>{clearTimeout(timer);timer=setTimeout(checkpoint,150);if(archive&&archive.scrollLeft+archive.clientWidth>=archive.scrollWidth-150)setVisibleCount(n=>Math.min(n+props.pageSize,earlier().length))}}>
              <For each={earlier().slice(0,visibleCount())}>{event=><HistoryEntry event={event} diff={changes.get(event)} open={selected()===event}
                onToggle={()=>{setSelected(selected()===event?undefined:event);queueMicrotask(()=>archive?.querySelector('.compared')?.scrollIntoView({block:'nearest',inline:'nearest'}))}}
                onIndicator={props.onIndicator} indicatorsLoading={props.indicatorsLoading}
                onSave={anchor=>setNotebook({event,anchor})} saved={saved()?.event===event?saved()?.name:undefined}/>}</For>
            </div></Show>
            <Show when={latest()} keyed>{event=><HistoryEntry event={event} diff={changes.get(event)} open latest onToggle={()=>{}}
              onIndicator={props.onIndicator} indicatorsLoading={props.indicatorsLoading}
              onSave={anchor=>setNotebook({event,anchor})} saved={saved()?.event===event?saved()?.name:undefined}/>}</Show>
          </div>
        </Show>
        <ScrollEnd root={()=>scroll} enabled={!history()&&visibleCount()<count()} version={visibleCount()}
          onNearEnd={()=>setVisibleCount(n=>n+props.pageSize)} />
        </div>
      </section>
    </Show>
    <Show when={notebook()} keyed>{selection=><NotebookPicker event={selection.event} anchor={selection.anchor} onClose={()=>setNotebook(undefined)} onSaved={name=>{setSaved({event:selection.event,name});setNotebook(undefined);}}/>}</Show>
  </div>;
}


export function HistoryDialog(props:Omit<ResearchProps,'mode'|'transient'> & {onClose:()=>void}) {
  let dialog!:HTMLDialogElement;
  const title=createUniqueId();
  const opener=document.activeElement instanceof HTMLElement?document.activeElement:undefined;
  onMount(()=>dialog.showModal());
  onCleanup(()=>{dialog.close();if(opener?.isConnected)opener.focus({preventScroll:true})});
  return <dialog ref={dialog} class="modal history-modal" aria-labelledby={title}
    onCancel={event=>{event.preventDefault();props.onClose()}}
    onClick={event=>{if(event.target===dialog)props.onClose()}}>
    <div class="modal-body">
      <div class="modal-heading"><h2 id={title}>Object History</h2>
        <button type="button" class="icon-button" autofocus aria-label="Close Object History" onClick={props.onClose}><Icon name="close"/></button>
      </div>
      <Research {...props} mode="Object History" transient/>
    </div>
  </dialog>;
}
