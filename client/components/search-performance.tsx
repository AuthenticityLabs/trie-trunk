import { For, Show, createMemo, createSignal, createUniqueId, onCleanup, onMount } from 'solid-js';
import type { ViewTimings } from '../services/pil/view';
import {searchTimingRows} from '../services/search/timing';
export type SearchTiming = { kind: 'Search' | 'Restore' | 'Page' | 'Charts'; ms: number; phases?: ViewTimings; failed?: boolean };
const duration=(ms:number)=>ms<1000?`${Math.round(ms)} ms`:`${(ms/1000).toFixed(2)} s`;

/** Phase totals use durations across worker boundaries, never mixed clock origins. */
export function SearchPerformance(props: { timing?: SearchTiming; matched?: number; loaded?: number }) {
 const [open,setOpen]=createSignal(false);
 const [position,setPosition]=createSignal({left:'8px',top:'8px'});
 const tooltipId=createUniqueId();
 let root:HTMLSpanElement|undefined;
 let details:HTMLSpanElement|undefined;
 const close=()=>setOpen(false);
 const reveal=()=>{
  setOpen(true);
  if(!root||!details)return;
  const anchor=root.getBoundingClientRect(),box=details.getBoundingClientRect();
  const viewport=window.visualViewport;
  const left=viewport?.offsetLeft??0,top=viewport?.offsetTop??0;
  const width=viewport?.width??window.innerWidth,height=viewport?.height??window.innerHeight;
  setPosition({
   left:`${Math.max(left+8,Math.min(anchor.right-box.width,left+width-box.width-8))}px`,
   top:`${Math.max(top+8,Math.min(anchor.bottom+6,top+height-box.height-8))}px`,
  });
 };
 onMount(()=>{
  const outside=(event:PointerEvent)=>{if(!root?.contains(event.target as Node))close();};
  const escape=(event:KeyboardEvent)=>{if(event.key==='Escape')close();};
  document.addEventListener('pointerdown',outside);
  document.addEventListener('keydown',escape);
  window.addEventListener('resize',close);
  window.addEventListener('scroll',close,true);
  window.visualViewport?.addEventListener('resize',close);
  onCleanup(()=>{
   document.removeEventListener('pointerdown',outside);
   document.removeEventListener('keydown',escape);
   window.removeEventListener('resize',close);
   window.removeEventListener('scroll',close,true);
   window.visualViewport?.removeEventListener('resize',close);
  });
 });
 const rows=createMemo(()=>{
  const t=props.timing;if(!t)return [];
  return searchTimingRows(t.ms,t.phases);
 });
 return <Show when={props.timing}>{timing=><span class="search-performance" ref={root} data-open={open()}
  onPointerEnter={event=>{if(event.pointerType==='mouse')reveal();}}
  onPointerLeave={event=>{if(event.pointerType==='mouse')close();}}
  onFocusOut={event=>{if(!event.currentTarget.contains(event.relatedTarget as Node|null))close();}}
  data-search-time-ms={timing().ms} data-search-time-kind={timing().kind}>
  <button type="button" class="performance-trigger" aria-expanded={open()} aria-describedby={open()?tooltipId:undefined}
   onFocus={event=>{if(event.currentTarget.matches(':focus-visible'))reveal();}}
   onClick={event=>{if(event.detail===0||event.currentTarget.matches(':hover')&&window.matchMedia('(hover: hover)').matches)reveal();else if(open())close();else reveal();}}
   aria-label={`${timing().kind}${timing().failed?' Failed':''} Performance: ${duration(timing().ms)}`}>
   <span>{timing().kind}{timing().failed?' Failed':''}: {duration(timing().ms)}</span>
   <Show when={timing().phases}><span class="performance-bar" aria-hidden="true"><For each={rows()}>{row=><span class={`performance-${row.key}`} style={{width:`${timing().ms?row.ms/timing().ms*100:0}%`}} />}</For></span></Show>
  </button>
  <span class="performance-details" ref={details} id={tooltipId} role="tooltip" style={position()}>
   <strong>{timing().kind}{timing().failed?' Failed':''} Performance</strong>
   <Show when={props.matched!==undefined}><span class="performance-count">Matched Events <b data-matched-count>{props.matched?.toLocaleString()}</b></span></Show>
   <Show when={props.loaded!==undefined}><span class="performance-count">Loaded Events <b data-collected-count>{props.loaded?.toLocaleString()}</b></span></Show>
   <For each={rows()}>{row=><span class="performance-row" title={row.hint}><i class={`performance-${row.key}`} /><span>{row.label}</span><b>{duration(row.ms)}</b></span>}</For>
   <span class="performance-note">{timing().failed?'Elapsed before failure.':timing().kind==='Charts'?'Time from search start until chart data is ready.':'Time until event data is ready; charts load separately.'} {timing().phases?'Includes network and local reads. Excludes painting and background cache writes.':'Phase timings were not returned.'}</span>
  </span>
 </span>}</Show>;
}
