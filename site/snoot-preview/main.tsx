import {For,Show,createEffect,createMemo,createSignal,onCleanup,onMount} from 'solid-js';
import {render} from 'solid-js/web';
import {Navigation,type Page} from '../../client/components/navigation';
import DashboardWorkspace from '../../client/components/dashboard-workspace';
import FastDashboard from '../../client/components/fast-dashboard';
import {serviceDirectory} from '../../client/services/dashboard-subscriptions';
import {SummarySubscriptions} from '../../client/components/summary-subscriptions';
import {Events} from '../../client/components/events';
import {QueryEditor} from '../../client/components/query-editor';
import {Sources} from '../../client/components/sources';
import {Icon} from '../../client/components/icon';
import {IndicatorDialog} from '../../client/components/indicator';
import {Settings,type Panel} from '../../client/components/settings';
import {AlertCenter} from '../../client/components/alerts';
import Research from '../../client/components/research';
import {ActualAlerts} from 'virtual:snoot-alerts';
import {Results} from '../../client/services/results';
import {parseQuery} from '../../client/services/query';
import {valueAt,expandEvent,jsonText,type LogEvent} from '../../client/services/dictionary';
import {compileIndicator,type EventField,type Indicator} from '../../client/services/indicators';
import {nextTheme,resolveTheme,type ThemePreference} from '../../client/services/preferences';
import wordmark from '../../client/assets/snoot-wordmark.png';
import {events,sources,queryFields} from './data';
import '../../client/style.css';
import '../../shared/select-controls.css';
import './preview.css';
import './marketing.css';

function Preview(){
 const [page,setPage]=createSignal<Page>('Dashboards');
 const [collapsed,setCollapsed]=createSignal(parent.innerWidth<=760);
 const [theme,setTheme]=createSignal<ThemePreference>(parent.document.documentElement.dataset.theme==='light'?'light':'dark');
 const [modal,setModal]=createSignal<Panel>();
 const [source,setSource]=createSignal('');
 const [query,setQuery]=createSignal('events');
 const [matched,setMatched]=createSignal(events);
 const [selected,setSelected]=createSignal<LogEvent>();
 const [error,setError]=createSignal('');
 const [field,setField]=createSignal<EventField>();
 const [indicators,setIndicators]=createSignal<Indicator[]>([]);
 const [following,setFollowing]=createSignal(false);
 const [revision,setRevision]=createSignal(0);
 const [range,setRange]=createSignal('today');
 const [loggedOut,setLoggedOut]=createSignal(false);
 createEffect(()=>{document.documentElement.dataset.theme=resolveTheme(theme(),matchMedia('(prefers-color-scheme: dark)').matches)});
 const sync=(event:MessageEvent)=>{
  if(event.source!==parent||event.origin!==location.origin||event.data?.type!=='snoot-preview-appearance')return;
  setTheme(event.data.theme==='light'?'light':'dark');setCollapsed(Boolean(event.data.compact));
 };
 addEventListener('message',sync);onCleanup(()=>removeEventListener('message',sync));
 const annotations=createMemo(()=>{const result=new Results();result.setMatchers(indicators().map(compileIndicator));result.append(matched());return result.annotations});
 function search(){
  try{
   const plan=parseQuery(query(),queryFields).plan;
   const rows=events.filter(event=>{
    if(source()&&!event.dictionary.tap.startsWith(source().replace(/\/(read|write)$/,'')))return false;
    for(const f of plan.filters){
     const actual=valueAt(event,f.field);
     const duration=f.value.kind==='duration'?f.value.value.match(/^([\d.]+)(ns|us|µs|μs|ms|s)$/):undefined;
     const operand=duration?Number(duration[1])*({ns:.001,us:1,'µs':1,'μs':1,ms:1000,s:1_000_000}[duration[2]]??1):f.value.kind==='number'?Number(f.value.value):f.value.value;
     if(f.op==='=='&&actual!==operand||f.op==='!='&&actual===operand||f.op==='contains'&&!String(actual).includes(String(operand))||f.op==='>'&&!(Number(actual)>Number(operand))||f.op==='>='&&!(Number(actual)>=Number(operand))||f.op==='<'&&!(Number(actual)<Number(operand))||f.op==='<='&&!(Number(actual)<=Number(operand)))return false;
    }
    return !plan.search||jsonText(expandEvent(event)).toLowerCase().includes(plan.search.text.toLowerCase());
   });
   if(plan.order==='desc')rows.reverse();setMatched(rows);setSelected(undefined);setError('');setRevision(n=>n+1);
  }catch(cause){setError(cause instanceof Error?cause.message:String(cause))}
 }
 const navigate=(next:Page)=>{setPage(next)};
 return <Show when={!loggedOut()} fallback={<main class="signed-out"><img class="content-wordmark" src={wordmark} alt="snoot"/><h1>Logged Out of This Tab</h1><button class="button" onClick={()=>setLoggedOut(false)}>Open Workspace</button></main>}>
 <div class="workspace" classList={{'sidebar-collapsed':collapsed()}}>
  <Navigation theme={theme()} themeBusy={false} themeError="" onToggleTheme={()=>setTheme(nextTheme(theme()))} page={page()} count={matched().length} hasToken={true} onNavigate={navigate} onHelp={()=>setModal('help')} onConnection={()=>setModal('connection')} onProfile={()=>setModal('profile')} onLogout={()=>setLoggedOut(true)}/>
  <main class="main"><header class="topbar">
   <button class="icon-button desktop-menu" aria-label={collapsed()?'Expand navigation':'Collapse navigation'} aria-expanded={!collapsed()} onClick={()=>setCollapsed(!collapsed())}><Icon name="panel"/></button>
   <h1 class="content-brand"><img class="content-wordmark" src={wordmark} alt="snoot" width="2172" height="724"/><span class="sr-only"> — {page()}</span></h1>
   <div class="topbar-right"><span class="preview-page-badge">Sample Data</span><button class="guide-button" aria-label="Quick Guide" onClick={()=>setModal('help')}><Icon name="help" size={16}/><span>Quick Guide</span></button></div>
  </header>
  <SummarySubscriptions token="static-preview" sources={sources} loading={false}/>
  <div class="page-scroll"><div class="page-content">
   <Show when={page()==='Dashboards'}><DashboardWorkspace token="static-preview" sources={sources} loading={false} catalogError=""/></Show>
   <Show when={page()==='Search'}>
    <form class="card query-card preview-query-card" aria-label="Search Events" onSubmit={e=>{e.preventDefault();search()}}>
     <div class="query-heading"><div><span class="query-symbol"><Icon name="search" size={17}/></span><strong>Search</strong></div></div>
     <Sources value={source()} catalog={sources} loading={false} onChange={setSource}/>
     <QueryEditor value={query()} onInput={setQuery} fields={queryFields} loading={false}/>
     <div class="query-controls"><label class="control"><Icon name="clock" size={15}/><span class="sr-only">Time Window</span><select aria-label="Time Window" value={range()} onChange={e=>setRange(e.currentTarget.value)}><option value="today">Today (UTC)</option><option value="15">Last 15 Minutes</option><option value="60">Last Hour</option><option value="1440">Last 24 Hours</option><option value="10080">Last 7 Days</option><option value="43200">Last 30 Days</option></select></label><span class="query-controls-spacer"/>
      <button type="button" class={`follow-button ${following()?'is-following':''}`} onClick={()=>setFollowing(!following())}><Icon name={following()?'stop':'activity'} size={16}/>{following()?'Stop Following':'Follow Live'}</button>
      <Show when={query()!=='events'}><button class="button secondary" type="button" onClick={()=>{setQuery('events');search()}}><Icon name="close" size={14}/>Clear</button></Show><button class="button primary" type="submit"><Icon name="search" size={16}/>Search</button>
     </div><Show when={error()}><div class="tap-error" role="alert">{error()} The current view is unchanged.</div></Show>
    </form>
    <section class="card results-card"><div class="results-toolbar"><div class="view-tabs" aria-label="Results"><span class="active">Events <span>{matched().length}</span></span></div><div class="results-actions"><span class="stream-status" classList={{live:following()}}><i/>{following()?'Following':'Collected'}</span></div></div>
     <Events revision={revision()} events={matched()} selected={selected()} select={event=>setSelected(selected()===event?undefined:event)} annotations={annotations()} onIndicator={setField} indicatorsLoading={false} reveal={undefined} pageSize={100} active={true}/>
     <div class="results-footer"><span>{matched().length} matched / {events.length} collected</span><span>Oldest → newest <span class="footer-dot">·</span> UTC</span></div>
    </section>
   </Show>
   <Show when={page()==='Alerts'}><AlertCenter token="static-preview" lostEvents={()=><Research mode="Lost Events" token="static-preview" sources={sources} initialSource={sources[0]} pageSize={100} onIndicator={setField} indicatorsLoading={false}/>}><ActualAlerts onNavigate={navigate}/></AlertCenter></Show>
  </div></div></main>
  <Settings panel={modal()} token="" onToken={()=>setModal(undefined)} onClose={()=>setModal(undefined)} sources={sources} onRebase={async()=>{}}/>
  <Show when={field()}>{value=><IndicatorDialog field={value()} onClose={()=>setField(undefined)} onSave={async indicator=>{setIndicators(list=>[...list,indicator]);setField(undefined)}}/>}</Show>
 </div></Show>;
}
function useEmbeddedTheme(mode:'dashboard'|'marketing'){
 document.documentElement.dataset.preview=mode;
 const [theme,setTheme]=createSignal(parent.document.documentElement.dataset.theme==='light'?'light':'trunk');
 createEffect(()=>{document.documentElement.dataset.theme=theme()});
 const sync=(event:MessageEvent)=>{
  if(event.source!==parent||event.origin!==location.origin||event.data?.type!=='snoot-preview-appearance')return;
  setTheme(event.data.theme==='light'?'light':'trunk');
 };
 addEventListener('message',sync);onCleanup(()=>removeEventListener('message',sync));
}
function HomepageDashboard(){
 useEmbeddedTheme('dashboard');
 onMount(()=>{
  const root=document.getElementById('root')!;
  const report=()=>parent.postMessage({type:'snoot-dashboard-size',height:root.getBoundingClientRect().height},location.origin);
  const resize=new ResizeObserver(report);resize.observe(root);report();onCleanup(()=>resize.disconnect());
 });
 const catalog=serviceDirectory(sources);
 return <><span class="preview-page-badge">Sample Data</span><FastDashboard token="static-homepage" services={catalog.map(s=>s.id)} revision="static-homepage"/></>;
}
function MarketingCharts(){
 useEmbeddedTheme('marketing');
 const catalog=serviceDirectory(sources);
 let stage!:HTMLDivElement;
 onMount(()=>{
  const decorative=()=>stage.querySelectorAll<HTMLElement>('[data-preview-panel="top"]').forEach(panel=>{panel.inert=true;panel.setAttribute('aria-hidden','true')});
  const observer=new MutationObserver(decorative);observer.observe(stage,{childList:true,subtree:true});decorative();onCleanup(()=>observer.disconnect());
 });
 return <div ref={stage} class="marketing-chart-stage"><FastDashboard token="static-homepage" services={catalog.map(s=>s.id)} revision="static-homepage"/><span class="marketing-sample-label">Sample Data</span></div>;
}
render(()=>{
 const view=new URLSearchParams(location.search).get('view');
 return view==='marketing'?<MarketingCharts/>:view==='dashboard'?<HomepageDashboard/>:<Preview/>;
},document.getElementById('root')!);
