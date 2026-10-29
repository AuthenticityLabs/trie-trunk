import {createEffect,createMemo,createSignal,onCleanup,Show} from 'solid-js';
import {readTimeWindow} from '../services/pil/client';
import type {TrunkBootstrap} from '../services/pil/trunk';
import type {TimeWindowResult} from '../services/pil/time-window';
import {Histogram} from './charts';
export function TimeActivity(props:{trunk:TrunkBootstrap;scope:string;from:string;to:string;token:string;active:boolean;onWindow:(from:number,to:number)=>void}){
 const [result,setResult]=createSignal<TimeWindowResult>(),[error,setError]=createSignal(''),[loading,setLoading]=createSignal(false);
 const chart=createMemo(()=>{
  const r=result();if(!r)return;
  const factor=Math.max(1,Math.ceil(r.counts.length/300)),counts:number[]=[],errors:number[]=[];
  r.counts.forEach((n,i)=>{const at=Math.floor(i/factor);counts[at]=(counts[at]??0)+n;errors[at]=(errors[at]??0)+r.errors[i]});
  return {...r,counts,errors,to:r.from+counts.length*factor*3_600_000};
 });
 const [retry,setRetry]=createSignal(0);
 const bounds=createMemo(()=>({from:Date.parse(props.from),to:Date.parse(props.to)}));
 createEffect(()=>{
  const query={trunk:props.trunk,scope:props.scope,...bounds()},token=props.token,active=props.active;retry();
  const controller=new AbortController();onCleanup(()=>controller.abort());
  setResult(undefined);setError('');setLoading(false);
  if(!active||!Number.isFinite(query.from)||!Number.isFinite(query.to)||query.from>=query.to)return;
  setLoading(true);
  // A short coalescing interval avoids requesting every intermediate calendar edit.
  const timer=setTimeout(()=>void readTimeWindow(query,token,controller.signal).then(value=>{if(!controller.signal.aborted)setResult(value)},cause=>{if(!controller.signal.aborted)setError(cause instanceof Error?cause.message:String(cause))}).finally(()=>{if(!controller.signal.aborted)setLoading(false)}),120);
  onCleanup(()=>clearTimeout(timer));
 });
 return <section class="card activity-card" aria-label="Hourly Event Activity" aria-busy={loading()}>
  <div class="results-toolbar"><strong>Hourly Event Activity</strong><Show when={result()}>{r=><span>{r().count.toLocaleString()} Events in Sealed Hours</span>}</Show><Show when={loading()}><span role="status">Loading Hourly Counts…</span></Show></div>
  <Show when={error()}><div class="notice error-notice" role="alert">{error()}<button class="text-button" onClick={()=>setRetry(n=>n+1)}>Retry</button></div></Show>
  <Show when={chart()}>{r=><Histogram label="events in sealed hours" counts={r().counts} errors={r().errors} first={r().from} last={r().to} count={r().count} onWindow={(a,z)=>props.onWindow(Math.max(a,result()!.from),Math.min(z,result()!.to))}/>}</Show>
 </section>;
}
