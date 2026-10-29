import { nativeCacheIssue, cacheErrorMessage, cacheRefreshSources, type NativeCacheIssue } from "../services/store/errors";
import { SearchPerformance, type SearchTiming } from './search-performance';
import { SearchSession, credentialScope, type Position } from "../services/search-session";
import { ViewInsights } from "./view-insights";
import { Show, batch, createEffect, createMemo, createSignal, onCleanup } from 'solid-js';
import { loadView, persistView, rebaseCollections } from '../services/pil/client';
import type { ViewResponse } from '../services/pil/view';
import type { ViewQuery, ViewResult } from '../services/pil/view';
import type { LogEvent } from '../services/events';
import type { EventField, Matcher, Annotations } from '../services/indicators';
import { Events } from './events';
import { Histogram } from './charts';
import type { ObjectHistoryTarget } from '../services/replay';
import type { DiagnosticTarget } from '../services/diagnostics';

export type PagedViewState = {
 result?: ViewResult;
 loaded: number;
 busy: boolean;
 error: string;
 timing?: SearchTiming;
};

/** Cursor pages are fetched on scroll; loaded rows never certify complete replication. */
export function PagedResults(props:{
 timeZone?:string;
 query:ViewQuery; token:string; pageSize:number; active:boolean;
 matchers:Matcher[]; onIndicator:(field:EventField)=>void; indicatorsLoading:boolean;
 onState:(state:PagedViewState)=>void;
 collections?:readonly string[];onObjectHistory?:(target:ObjectHistoryTarget)=>void;
 diagnosticSources?:readonly string[];onDiagnostic?:(target:DiagnosticTarget)=>void;
}) {
 const [result,setResult]=createSignal<ViewResult>();
 const [events,setEvents]=createSignal<LogEvent[]>([]);
 const [selected,setSelected]=createSignal<LogEvent>();
 const [busy,setBusy]=createSignal(false),[error,setError]=createSignal('');
 const [chartLoading,setChartLoading]=createSignal(false),[chartError,setChartError]=createSignal('');
 const [revision,setRevision]=createSignal(0);
 const [timing,setTiming]=createSignal<SearchTiming>();
 const [pageTiming,setPageTiming]=createSignal<SearchTiming>();
 const [chartTiming,setChartTiming]=createSignal<SearchTiming>();
 let abort:AbortController|undefined, query:ViewQuery;
 const keys=new Set<string>();
 const session=new SearchSession();
 const [sessionWarning,setSessionWarning]=createSignal('');
 const [cacheWarning,setCacheWarning]=createSignal('');
 const [cacheIssue,setCacheIssue]=createSignal<NativeCacheIssue>();
 const [repairing,setRepairing]=createSignal(false);
 const refreshedResources=new Set<string>();
 const resource=(source:string)=>source.split('/').slice(0,3).join('/');
 const repeatedConflict=()=>{const issue=cacheIssue();return !!issue&&cacheRefreshSources(issue).some(source=>refreshedResources.has(resource(source)));};
 function describeError(cause:unknown){
  const issue=nativeCacheIssue(cause);
  if(issue){setCacheIssue(issue);console.warn('Native Cache Conflict',issue);if(cacheRefreshSources(issue).some(source=>refreshedResources.has(resource(source))))return 'Local cache refresh did not resolve this conflict. See Conflict Details.';}
  return cacheErrorMessage(cause);
 }
 async function repairLocalCache(){
  const issue=cacheIssue();if(!issue||repairing())return;
  const sources=cacheRefreshSources(issue).filter(source=>!refreshedResources.has(resource(source)));
  if(!sources.length)return;
  const current=epoch,token=props.token,controller=new AbortController();
  cancelCacheJobs();abort?.abort();abort=controller;setBusy(false);setRepairing(true);
  try{
   await rebaseCollections(sources,token,controller.signal);
   if(current===epoch&&!controller.signal.aborted){for(const source of sources)refreshedResources.add(resource(source));restart();}
  }catch(cause){if(current===epoch&&!controller.signal.aborted)setError(cacheErrorMessage(cause));}
  finally{if(abort===controller)abort=undefined;setRepairing(false);}
 }
 const canRepair=()=>cacheIssue()?.code==='native-cache-conflict'&&cacheRefreshSources(cacheIssue()!).some(source=>!refreshedResources.has(resource(source)));
 const repairButton=()=> <button class="button secondary" disabled={repairing()} onClick={()=>void repairLocalCache()}>{repairing()?'Refreshing Local Cache…':'Refresh Local Cache'}</button>;
 const cacheJobs=new Set<AbortController>();
 function cancelCacheJobs(){for(const job of cacheJobs)job.abort();cacheJobs.clear();}
 function persist(view:ViewResponse){
  const controller=new AbortController();cacheJobs.add(controller);
  void persistView(view,props.token,controller.signal).catch(e=>{
   if(!controller.signal.aborted)setCacheWarning(`Loaded events are still available. ${describeError(e)}`);
  }).finally(()=>cacheJobs.delete(controller));
 }
 const [position,setPosition]=createSignal<Position>({top:0,left:0});
 let credential='', input:ViewQuery, epoch=0, persistence=true, selectedKey:string|undefined;
 const [restoring,setRestoring]=createSignal(true);
 function storageFailure(){persistence=false;setSessionWarning('Tab storage is unavailable. Search settings may reset on refresh.');}
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
  setBusy(true);setError('');setCacheIssue(undefined);
  let delivered=false;
  try{
   const next=await loadView({...query,size:props.pageSize,after},props.token,controller.signal,page=>{
    if(controller.signal.aborted||abort!==controller)return;
    if(after && result() && (page.result.snapshot!==result()?.snapshot || page.result.next===after ||
       (page.events.length===0 && page.result.next) || page.result.page.some(ref=>keys.has(ref.key))))
     throw Error('Search continuation changed; restart search.');
    page.result.page.forEach(ref=>keys.add(ref.key));
    delivered=true;
    batch(()=>{
     const elapsed=performance.now()-started;
     if(after)setPageTiming({kind:'Page',ms:elapsed,phases:page.view.timings});else setTiming({kind:'Search',ms:elapsed,phases:page.view.timings});
     setResult({...page.result,summary:result()?.summary});
     setEvents(rows=>after?[...rows,...page.events]:page.events);
     if(selectedKey)setSelected(page.events[page.result.page.findIndex(ref=>ref.key===selectedKey)]);
     setChartLoading(true);setChartError('');
    });
   });
   if(controller.signal.aborted||abort!==controller)return;
   // Page latency is distinct from chart completion. Persist the completed
   // snapshot once, with summaries matching the already delivered events.
   setChartTiming({kind:'Charts',ms:performance.now()-started,phases:next.view.timings});
   setResult({...next.result,summary:next.result.summary??result()?.summary});
   if(persistence){try{session.append(input,query,credential,next.view)}catch{storageFailure()}}
   persist(next.view);
  }catch(e){if(!controller.signal.aborted&&abort===controller){
   const message=describeError(e);
   if(delivered)setChartError(message);else setError(message);
  }}
  finally{if(abort===controller){abort=undefined;setBusy(false);setChartLoading(false)}}
 }

 function restart(nextQuery=query){
  cancelCacheJobs();setCacheWarning('');setCacheIssue(undefined);setChartLoading(false);setChartError('');setChartTiming(undefined);setTiming(undefined);setPageTiming(undefined);
  try{session.clear()}catch{storageFailure()}
  setPosition({top:0,left:0});selectedKey=undefined;
  abort?.abort();abort=undefined;query={...nextQuery};keys.clear();
  batch(()=>{setBusy(false);setError('');setResult(undefined);setEvents([]);setSelected(undefined);setRevision(r=>r+1)});
  void fetchPage(query.after);
 }
 createEffect(()=>{
  const next={...props.query,size:props.pageSize}, token=props.token;
  const current=++epoch;
  refreshedResources.clear();
  setTiming(undefined);setPageTiming(undefined);
  cancelCacheJobs();setCacheWarning('');setCacheIssue(undefined);setChartLoading(false);setChartError('');setChartTiming(undefined);
  abort?.abort();abort=undefined;setBusy(false);setRestoring(true);
  input=next;
  void credentialScope(token).then(async scope=>{
   if(current!==epoch)return;
   credential=scope;persistence=true;setSessionWarning('');
   try{
    const saved=session.restore(next,scope);
    restart(saved?.query??next);
    if(saved){selectedKey=saved.selected;setPosition(saved.position);}
    return;
   }catch{if(current!==epoch)return;try{session.clear()}catch{}setSessionWarning('The saved search could not be restored. Reloading its first page.');}
   restart(next);
  }).catch(()=>{if(current===epoch){storageFailure();restart(next)}})
    .finally(()=>{if(current===epoch)setRestoring(false)});
 });
 createEffect(()=>props.onState({result:result(),loaded:events().length,busy:busy(),error:error(),timing:timing()}));
 onCleanup(()=>{epoch++;abort?.abort();abort=undefined;cancelCacheJobs()});
 const more=()=>{const cursor=result()?.next;if(cursor&&!busy()&&!restoring()&&!error()&&!chartError())void fetchPage(cursor)};
 return <div class="paged-results search-layout" aria-busy={busy()}>
  <div class="results-column">
  <Show when={result()?.notice}><p class="notice" role="status">{result()!.notice}</p></Show>
  <Show when={chartLoading()&&!result()?.summary}><section class="card activity-card" aria-busy="true">
   <div class="results-toolbar"><strong>Event Activity</strong><span role="status">Loading Charts…</span></div>
   <div style={{height:'148px'}} />
  </section></Show>
  <Show when={chartError()}><div class="notice" role="alert">Charts could not be loaded: {chartError()}
   <button class="text-button" onClick={()=>restart()}>Retry</button>
  </div></Show>
  <Show when={result()?.summary}>{s=><section class="card activity-card">
   <div class="results-toolbar"><strong>Event Activity</strong><span>{s().count.toLocaleString()} matched</span><SearchPerformance timing={chartTiming()} />
    <button class="text-button" onClick={()=>restart(props.query)}>Reset Time Window</button></div>
   <Histogram counts={s().counts} errors={s().errors} first={s().from} last={s().from+s().step*s().counts.length}
    count={s().count} onWindow={(from,to)=>restart({...query,from:new Date(Math.max(from,s().from)).toISOString(),to:new Date(Math.min(to,s().to)).toISOString()})} />
   <Show when={result()?.provisional}><div class="results-footer">Committed events only · the recent arrival window is still open</div></Show>
  </section>}</Show>
  <section class="card results-card">
   <div class="results-toolbar"><strong>Events</strong><div class="results-actions">
    <span role="status">{events().length.toLocaleString()} loaded{busy()&&!chartLoading()?' · Loading…':''}<Show when={result()?.coverage?.complete}>{n=>` · ${n()} complete buckets`}</Show></span>
    <SearchPerformance timing={pageTiming()} />
    <button class="text-button" type="button" disabled={busy()} onClick={()=>restart()}>Refresh</button>
   </div></div>
   <Show when={sessionWarning()}><div class="notice" role="status">{sessionWarning()}</div></Show>
   <Show when={cacheWarning()}><div class="notice" role="status">{cacheWarning()}<Show when={repeatedConflict()}><details class="cache-conflict-details"><summary>Conflict Details</summary><pre>{JSON.stringify(cacheIssue(),null,2)}</pre></details></Show><Show when={canRepair()}>{repairButton()}</Show></div></Show>
   <Show when={error()}><div class="notice error-notice" role="alert">{error()}
    <Show when={repeatedConflict()}><details class="cache-conflict-details"><summary>Conflict Details</summary><pre>{JSON.stringify(cacheIssue(),null,2)}</pre></details></Show>
    <Show when={canRepair()} fallback={<>
     <button class="text-button" disabled={busy()||repairing()} onClick={()=>void fetchPage(result()?.next??query.after)}>Retry</button>
     <button class="text-button" onClick={()=>restart()}>Restart Search</button>
    </>}>{repairButton()}</Show>
   </div></Show>
   <Show when={events().length} fallback={<Show when={!busy()&&!error()}><div class="empty-state">No events match this query in the selected time window. Try a wider window or remove a filter.</div></Show>}>
    <Events timeZone={props.timeZone} events={events()} revision={revision()} selected={selected()} select={choose} position={position()} onPosition={rememberPosition}
     annotations={annotations()} onIndicator={props.onIndicator} indicatorsLoading={props.indicatorsLoading}
     collections={props.collections} onObjectHistory={props.onObjectHistory}
     diagnosticSources={props.diagnosticSources} diagnosticWindow={props.query} onDiagnostic={props.onDiagnostic}
     reveal={undefined} pageSize={props.pageSize} onNearEnd={more} hasMore={!!result()?.next&&!error()&&!chartError()} loading={busy()||restoring()} active={props.active} />
   </Show>
  </section>
  </div>
  <Show when={result()?.summary}>{summary=><ViewInsights summary={summary()} loaded={events().length}/>}</Show>
 </div>
}
