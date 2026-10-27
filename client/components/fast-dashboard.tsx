import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { init, use, type EChartsCoreOption } from 'echarts/core';
import { LineChart, BarChart } from 'echarts/charts';
import { GridComponent, TooltipComponent, AriaComponent } from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';
import { dayMs, fastDashboard, panelGroups, chartCSV, chartValue, sparklinePath, trendAngle, type FastResult, type Metric, type PanelDefinition } from '../services/fast-dashboard';
import { loadFastDashboard } from '../services/pil/client';
import type { SeedResource } from '../services/dashboard-subscriptions';

use([LineChart, BarChart, GridComponent, TooltipComponent, AriaComponent, SVGRenderer]);
const number = (v: number | null, unit:PanelDefinition['view']['unit'] = 'count') => v === null ? '—' : chartValue(v,unit)!.toLocaleString(undefined,{maximumFractionDigits:unit==='count'?0:2})+(unit==='percent'?'%':unit==='ms'?' ms':'');
const names: Record<Metric,string> = { events:'Events',errors:'Errors',errorRate:'Error Rate',average:'Average Duration',p95:'P95 Duration' };
const units = (metric: Metric) => metric==='average'||metric==='p95'?'ms':metric==='errorRate'?'percent':'count';
const date = (time: number) => new Date(time).toLocaleDateString(undefined,{timeZone:'UTC',month:'short',day:'numeric'});

export default function FastDashboard(props:{token:string;services:string[];resources:SeedResource[];revision:string}) {
  const [period,setPeriod]=createSignal<string>(fastDashboard.time.preset),[resource,setResource]=createSignal(''),[refresh,setRefresh]=createSignal(0);
  const [result,setResult]=createSignal<FastResult>(),[loading,setLoading]=createSignal(false),[error,setError]=createSignal('');
  createEffect(()=>{if(resource()&&!props.resources.some(r=>r.id===resource()))setResource('')});
  const selection=createMemo(()=>JSON.stringify([props.token,[...props.services].sort(),resource(),period(),props.revision,refresh()]));
  createEffect(()=>{
    selection();
    const controller=new AbortController(),to=Math.floor(Date.now()/dayMs)*dayMs;
    const from=period()==='ytd'?Date.UTC(new Date(to).getUTCFullYear(),0,1):to-Number(period().slice(0,-1))*dayMs;
    const scopes=resource()?[resource()]:[...props.services];
    setResult(undefined);setError('');setLoading(true);
    if(!scopes.length||from===to){setLoading(false);return;}
    void loadFastDashboard({scopes,from,to},props.token,controller.signal).then(value=>{if(!controller.signal.aborted)setResult(value)},cause=>{if(!controller.signal.aborted)setError(cause instanceof Error?cause.message:String(cause))}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});
    onCleanup(()=>controller.abort());
  });
  return <section class="fast-dashboard" aria-label="Fast Dashboard">
    <div class="fast-toolbar">
      <div role="tablist" aria-label="Dashboard Mode" class="fast-tabs"><button role="tab" id="fast-dashboard-tab" aria-controls="fast-dashboard-panel" aria-selected="true">Fast</button><button role="tab" aria-selected="false" disabled title="Event-based analysis is coming next">Deep</button></div>
      <label>Resource<select value={resource()} onChange={e=>setResource(e.currentTarget.value)}><option value="">All Resources</option><For each={props.resources}>{r=><option value={r.id}>{r.service} / {r.resource}</option>}</For></select></label>
      <label>Time Window<select value={period()} onChange={e=>setPeriod(e.currentTarget.value)}><option value="7d">Last 7 Days</option><option value="30d">Last 30 Days</option><option value="ytd">Year to Date</option></select></label>
      <button class="button secondary" disabled={loading()} onClick={()=>setRefresh(n=>n+1)}>Refresh</button>
    </div>
    <div role="tabpanel" id="fast-dashboard-panel" aria-labelledby="fast-dashboard-tab" aria-busy={loading()}>
      <Show when={loading()}><p class="fast-load" role="status">Loading Summary Charts…</p></Show>
      <Show when={error()}><div class="notice error-notice" role="alert">{error()}<button class="button secondary" onClick={()=>setRefresh(n=>n+1)}>Retry</button></div></Show>
      <Show when={result()}>{data=><>
        <div class="fast-range"><span>{date(data().from)} – {date(data().to-1)} · Complete UTC Days</span><span>Precomputed Summaries</span></div>
        <Show when={data().matched!==data().totals.events}><div class="notice" role="status">{number(data().totals.events)} of {number(data().matched)} Events Summarized · Charts Cover Summarized Events</div></Show>
        <Show when={data().totals.events>0} fallback={<div class="card fast-empty"><h3>No Summary Data in This Window</h3><p>Choose another time window or resource.</p></div>}>
          <div class="fast-grid"><For each={fastDashboard.panels}>{panel=><FastPanel panel={panel} data={data()}/>}</For></div>
        </Show>
      </>}</Show>
    </div>
    <details class="fast-definition"><summary>Default Chart Definition</summary><pre>{JSON.stringify(fastDashboard,null,2)}</pre></details>
  </section>;
}

function FastPanel(props:{panel:PanelDefinition;data:FastResult}) {
  const p=props.panel,rows=createMemo(()=>panelGroups(p,props.data));
  const metric=()=>props.data.totals[p.data.metric];
  const download=()=>{
    const url=URL.createObjectURL(new Blob([chartCSV(rows(),p.view.columns??[p.data.metric])],{type:'text/csv;charset=utf-8'}));
    const a=document.createElement('a');a.href=url;a.download=`${p.id}.csv`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
  };
  const option=():EChartsCoreOption=>{
    if(p.view.kind==='bars')return {grid:{left:8,right:40,top:8,bottom:8,containLabel:true},tooltip:{trigger:'axis',renderMode:'richText',confine:true},xAxis:{type:'value'},yAxis:{type:'category',inverse:true,data:rows().map(r=>r.label),axisLabel:{width:140,overflow:'truncate'}},series:[{type:'bar',data:rows().map(r=>chartValue(r[p.data.metric],p.view.unit)),barMaxWidth:18,itemStyle:{borderRadius:[0,3,3,0]}}]};
    return {useUTC:true,grid:{left:8,right:12,top:16,bottom:16,containLabel:true},tooltip:{trigger:'axis',renderMode:'richText',confine:true},xAxis:{type:'time',axisLabel:{formatter:(value:number)=>props.data.step<dayMs?`${date(value)} ${new Date(value).getUTCHours()}:00`:date(value)}},yAxis:{type:'value',name:p.view.unit==='ms'?'ms':'Events'},series:[{type:'line',name:p.view.unit==='ms'?'P95 (ms)':'Events',showSymbol:false,connectNulls:false,data:props.data.time.map(t=>[t.time,chartValue(t[p.data.metric],p.view.unit)]),lineStyle:{width:2},areaStyle:p.data.metric==='events'?{opacity:.06}:undefined}]};
  };
  return <article class="card fast-panel" style={{'--panel-span':p.span}}>
    <div class="fast-panel-heading"><h4>{p.title}</h4><Show when={p.view.kind==='table'}><button class="text-button" onClick={download}>Export CSV</button></Show></div>
    <Show when={p.view.kind==='value'}><div class="fast-statistic"><div class="fast-number">{number(metric(),p.view.unit)}</div><Show when={p.view.indicator==='trend'}><TrendArrow panel={p} data={props.data}/></Show><Show when={p.view.indicator==='sparkline'}><Sparkline values={props.data.time.map(t=>t[p.data.metric])} label={`${p.title} Sparkline`} description={`${date(props.data.from)} – ${date(props.data.to-1)} UTC; gaps indicate missing data.`}/></Show></div></Show>
    <Show when={p.view.kind==='status'}><div class="fast-status" classList={{'has-errors':(metric()??0)>(p.data.test?.value??0)}}><span aria-hidden="true"/>{(metric()??0)>(p.data.test?.value??0)?'Yes':'No'}</div><span class="fast-secondary">{number(metric())} Errors</span></Show>
    <Show when={p.view.kind==='timechart'||p.view.kind==='bars'}><FastChart option={option()} title={p.title}/></Show>
    <Show when={p.view.kind==='tags'}><ul class="fast-tags"><For each={rows()}>{r=><li><span>{r.label}</span><strong>{number(r.events)}</strong></li>}</For></ul></Show>
    <Show when={p.view.kind==='list-trends'}><ul class="fast-trends"><For each={rows()}>{r=><li><span>{r.label}</span><Sparkline values={r.trend}/><strong>{number(r.events)}</strong></li>}</For></ul></Show>
    <Show when={p.view.kind==='table'}><div class="fast-table-scroll" tabIndex={0} aria-label="Resource Summary Table"><table><thead><tr><th>Resource</th><For each={p.view.columns}>{c=><th>{names[c]}</th>}</For></tr></thead><tbody><For each={rows()}>{r=><tr><th scope="row">{r.key}</th><For each={p.view.columns}>{c=><td>{number(r[c],units(c))}</td>}</For></tr>}</For></tbody></table></div></Show>
  </article>;
}
function TrendArrow(props:{panel:PanelDefinition;data:FastResult}) {
  const before=()=>props.data.comparison.earlier,after=()=>props.data.comparison.later;
  const angle=()=>before().events&&after().events?trendAngle(before()[props.panel.data.metric],after()[props.panel.data.metric]):null;
  const description=()=>angle()===null?`${props.panel.title}: Not Enough Data to Compare`:`${props.panel.title}: ${angle()===0?'Stable':angle()!>0?'Increasing':'Decreasing'}. Earlier Half: ${number(before()[props.panel.data.metric],props.panel.view.unit)}; Later Half: ${number(after()[props.panel.data.metric],props.panel.view.unit)}.`;
  return <span class="fast-trend-arrow" tabIndex={0} role="img" aria-label={description()} title={description()}><Show when={angle()!==null} fallback={<span aria-hidden="true">—</span>}><svg viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="18" fill="none" stroke="currentColor" opacity=".12"/><g transform={`rotate(${-angle()!} 24 24)`} fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 24H36M29 17L36 24L29 31"/></g></svg></Show></span>;
}
function Sparkline(props:{values:(number|null)[];label?:string;description?:string}) {
  const path=createMemo(()=>sparklinePath(props.values));
  return <svg class="fast-sparkline" viewBox="0 0 100 28" role="img" aria-label={props.label??'Event Volume Trend'}><title>{props.description??props.label??'Event Volume Trend'}</title><path d={path()} fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>;
}
function FastChart(props:{option:EChartsCoreOption;title:string}) {
  let element!:HTMLDivElement;
  onMount(()=>{
    const chart=init(element,undefined,{renderer:'svg'});
    const [theme,setTheme]=createSignal(0);
    const observer=new MutationObserver(()=>setTheme(n=>n+1));observer.observe(document.documentElement,{attributes:true,attributeFilter:['data-theme']});
    createEffect(()=>{theme();const css=getComputedStyle(element),text=css.getPropertyValue('--text-secondary').trim()||'#626860',color=css.getPropertyValue('--chart-blue').trim()||'#52769c',line=css.getPropertyValue('--border').trim()||'#ddd';
      const option=props.option as any;
      chart.setOption({...option,animation:false,color:[color],textStyle:{color:text,fontFamily:'inherit'},aria:{enabled:true,label:{description:props.title}},xAxis:{...option.xAxis,axisLabel:{color:text,...option.xAxis?.axisLabel},axisLine:{lineStyle:{color:line}},splitLine:{lineStyle:{color:line}}},yAxis:{...option.yAxis,axisLabel:{color:text,...option.yAxis?.axisLabel},splitLine:{lineStyle:{color:line}}}},{notMerge:true});
    });
    const resize=new ResizeObserver(()=>chart.resize());resize.observe(element);
    onCleanup(()=>{observer.disconnect();resize.disconnect();chart.dispose()});
  });
  return <div ref={element} class="fast-chart" role="img" aria-label={props.title}/>;
}
