import { SearchPerformance, type SearchTiming } from './search-performance';
import { SearchSession, credentialScope, type Position } from "../services/search-session";
import { ViewInsights } from "./view-insights";
import { Show, batch, createEffect, createMemo, createSignal, onCleanup } from 'solid-js';
import { loadView, rememberView, persistView } from '../services/pil/client';
import type { ViewResponse } from '../services/pil/view';
import type { ViewQuery, ViewResult } from '../services/pil/view';
import type { LogEvent } from '../services/events';
import type { EventField, Matcher, Annotations } from '../services/indicators';
import { Events } from './events';
import { Histogram } from './charts';
import type { ObjectHistoryTarget } from '../services/replay';

export type PagedViewState = {
 result?: ViewResult;
 loaded: number;
 busy: boolean;
 error: string;
 timing?: SearchTiming;
};

/** Cursor pages are fetched on scroll; loaded rows never certify complete replication. */
export function PagedResults(props:{
 query:ViewQuery; token:string; pageSize:number; active:boolean;
 matchers:Matcher[]; onIndicator:(field:EventField)=>void; indicatorsLoading:boolean;
 onState:(state:PagedViewState)=>void;
 collections?:readonly string[];onObjectHistory?:(target:ObjectHistoryTarget)=>void;
}) {
 const [result,setResult]=createSignal<ViewResult>();
 const [events,setEvents]=createSignal<LogEvent[]>([]);
 const [selected,setSelected]=createSignal<LogEvent>();
 const [busy,setBusy]=createSignal(false),[error,setError]=createSignal('');
 const [revision,setRevision]=createSignal(0);
 const [timing,setTiming]=createSignal<SearchTiming>();
 const [pageTiming,setPageTiming]=createSignal<SearchTiming>();
 let abort:AbortController|undefined, query:ViewQuery;
 const keys=new Set<string>();
 const session=new SearchSession();
 const [sessionWarning,setSessionWarning]=createSignal('');
 const [cacheWarning,setCacheWarning]=createSignal('');
 const cacheJobs=new Set<AbortController>();
 function cancelCacheJobs(){for(const job of cacheJobs)job.abort();cacheJobs.clear();}
 function persist(view:ViewResponse){
  const controller=new AbortController();cacheJobs.add(controller);
  void persistView(view,props.token,controller.signal).catch(e=>{
   if(!controller.signal.aborted)setCacheWarning(`Local cache could not be saved. Loaded Events are still available. ${e instanceof Error?e.message:String(e)}`);
  }).finally(()=>cacheJobs.delete(controller));
 }
 const [position,setPosition]=createSignal<Position>({top:0,left:0});
 let credential='', input:ViewQuery, epoch=0, persistence=true, selectedKey:string|undefined;
 const [restoring,setRestoring]=createSignal(true);
 function storageFailure(){persistence=false;setSessionWarning('Tab storage is unavailable or full. Search settings are retained when possible, but loaded rows cannot survive refresh.');}
 function rememberPosition(next=position()) {
  setPosition(next);
  if(!persistence)return;
  try{session.position(next,selectedKey)}catch{storageFailure()}
 }
 function choose(event:LogEvent){
  const next=selected()===event?undefined:event;
  selectedKey=next?[...keys][events().indexOf(next)]:undefined;
  setSelected(next);rememberPosition();
 }

 const annotations=createMemo<Annotations>(()=>new Map(events().map(e=>[e,props.matchers.filter(m=>m.matches(e)).map(m=>m.indicator)])));
 async function fetchPage(after?:string){
  if(busy())return;
  const controller=new AbortController();abort=controller;
  const started=performance.now();
  if(after)setPageTiming(undefined);else setTiming(undefined);
  setBusy(true);setError('');
  try{
   const next=await loadView({...query,size:props.pageSize,after},props.token,controller.signal);
   if(controller.signal.aborted||abort!==controller)return;
   if(after && result() && (next.result.snapshot!==result()?.snapshot || next.result.next===after ||
      (next.events.length===0 && next.result.next) || next.result.page.some(ref=>keys.has(ref.key))))
    throw Error('Search continuation changed; restart search.');
   next.result.page.forEach(ref=>keys.add(ref.key));
   if(persistence){try{session.append(input,query,credential,next.view)}catch{storageFailure()}}
   batch(()=>{
    const elapsed=performance.now()-started;
    if(after)setPageTiming({kind:'Page',ms:elapsed,phases:next.view.timings});else setTiming({kind:'Search',ms:elapsed,phases:next.view.timings});
    setResult({...next.result,summary:next.result.summary??result()?.summary});
    setEvents(rows=>after?[...rows,...next.events]:next.events);
   });
   persist(next.view);
  }catch(e){if(!controller.signal.aborted&&abort===controller)setError(e instanceof Error?e.message:String(e))}
  finally{if(abort===controller){abort=undefined;setBusy(false)}}
 }
 function restart(nextQuery=query){
  cancelCacheJobs();setCacheWarning('');setTiming(undefined);setPageTiming(undefined);
  try{session.clear()}catch{storageFailure()}
  setPosition({top:0,left:0});selectedKey=undefined;
  abort?.abort();abort=undefined;query={...nextQuery};keys.clear();
  batch(()=>{setBusy(false);setError('');setResult(undefined);setEvents([]);setSelected(undefined);setRevision(r=>r+1)});
  void fetchPage(query.after);
 }
 createEffect(()=>{
  const next={...props.query,size:props.pageSize}, token=props.token;
  const current=++epoch, restoreStarted=performance.now();
  setTiming(undefined);setPageTiming(undefined);
  cancelCacheJobs();setCacheWarning('');
  abort?.abort();abort=undefined;setBusy(false);setRestoring(true);
  input=next;
  void credentialScope(token).then(async scope=>{
   if(current!==epoch)return;
   credential=scope;persistence=true;setSessionWarning('');
   try{
    const saved=session.restore(next,scope);
    if(saved){
     const controller=new AbortController();abort=controller;
     for(const view of saved.views){
      await rememberView(view,token,controller.signal);
      if(current!==epoch||controller.signal.aborted)return;
     }
     abort=undefined;
     selectedKey=saved.selected;query=saved.query;keys.clear();saved.keys.forEach(key=>keys.add(key));
     batch(()=>{setTiming({kind:'Restore',ms:performance.now()-restoreStarted});setEvents(saved.events);setResult(saved.result);setPosition(saved.position);
      setSelected(saved.selected?saved.events[saved.keys.indexOf(saved.selected)]:undefined);setError('');setRevision(r=>r+1)});
     if(saved.views.length)persist(saved.views[saved.views.length-1]);
     return;
    }
   }catch{if(current!==epoch)return;try{session.clear()}catch{}setSessionWarning('The saved search could not be restored. Reloading its first page.');}
   restart(next);
  }).catch(()=>{if(current===epoch){storageFailure();restart(next)}})
    .finally(()=>{if(current===epoch)setRestoring(false)});
 });
 createEffect(()=>props.onState({result:result(),loaded:events().length,busy:busy(),error:error(),timing:timing()}));
 onCleanup(()=>{epoch++;abort?.abort();abort=undefined;cancelCacheJobs()});
 const more=()=>{const cursor=result()?.next;if(cursor&&!busy()&&!restoring()&&!error())void fetchPage(cursor)};
 return <div class="paged-results search-layout" aria-busy={busy()}>
  <div class="results-column">
  <Show when={result()?.summary}>{s=><section class="card activity-card">
   <div class="results-toolbar"><strong>Event Activity</strong><span>{s().count.toLocaleString()} matched</span>
    <button class="text-button" onClick={()=>restart(props.query)}>Reset Time Window</button></div>
   <Histogram counts={s().counts} errors={s().errors} first={s().from} last={s().from+s().step*s().counts.length}
    count={s().count} onWindow={(from,to)=>restart({...query,from:new Date(Math.max(from,s().from)).toISOString(),to:new Date(Math.min(to,s().to)).toISOString()})} />
   <Show when={result()?.provisional}><div class="results-footer">Committed events only · the recent arrival window is still open</div></Show>
  </section>}</Show>
  <section class="card results-card">
   <div class="results-toolbar"><strong>Events</strong><div class="results-actions">
    <span role="status">{events().length.toLocaleString()} loaded{busy()?' · Loading…':''}<Show when={result()?.coverage?.complete}>{n=>` · ${n()} complete buckets`}</Show></span>
    <SearchPerformance timing={pageTiming()} />
    <button class="text-button" type="button" disabled={busy()} onClick={()=>restart()}>Refresh</button>
   </div></div>
   <Show when={sessionWarning()}><div class="notice" role="status">{sessionWarning()}</div></Show>
   <Show when={cacheWarning()}><div class="notice" role="status">{cacheWarning()}</div></Show>
   <Show when={error()}><div class="notice error-notice" role="alert">{error()}
    <button class="text-button" disabled={busy()} onClick={()=>void fetchPage(result()?.next??query.after)}>Retry</button>
    <button class="text-button" onClick={()=>restart()}>Restart Search</button>
   </div></Show>
   <Show when={events().length} fallback={<Show when={!busy()&&!error()}><div class="empty-state">No committed events in this window.</div></Show>}>
    <Events events={events()} revision={revision()} selected={selected()} select={choose} position={position()} onPosition={rememberPosition}
     annotations={annotations()} onIndicator={props.onIndicator} indicatorsLoading={props.indicatorsLoading}
     collections={props.collections} onObjectHistory={props.onObjectHistory}
     reveal={undefined} pageSize={props.pageSize} onNearEnd={more} hasMore={!!result()?.next&&!error()} loading={busy()||restoring()} active={props.active} />
   </Show>
  </section>
  </div>
  <Show when={result()?.summary}>{summary=><ViewInsights summary={summary()} loaded={events().length}/>}</Show>
 </div>
}
