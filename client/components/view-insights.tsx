import { Show } from 'solid-js';
import type { ViewSummary } from '../services/pil/view';
import { duration } from '../services/events';
import { Breakdown } from './insights';

/** Statistics describe the pinned full result, independently of loaded rows. */
export function ViewInsights(props:{summary:ViewSummary;loaded:number;dashboard?:boolean}) {
 return <aside class={props.dashboard?'dashboard-sidebar':'insights-column'} aria-label="Search Insights">
  <section class="card overview-card">
   <h2>Matched Overview</h2>
   <div class="large-stat"><strong data-dashboard-matched={props.dashboard?'':undefined}>{props.summary.count.toLocaleString()}</strong><span>events matched</span></div>
   <dl>
    <div><dt>Errors</dt><dd>{props.summary.errors.reduce((sum,n)=>sum+n,0).toLocaleString()}</dd></div>
    <Show when={props.summary.statistics}>{stats=><>
     <div><dt>Services</dt><dd>{stats().services.length}</dd></div>
     <div><dt>Average Duration</dt><dd>{duration(stats().duration.count?stats().duration.average:undefined)}</dd></div>
     <div><dt>Minimum Duration</dt><dd>{duration(stats().duration.count?stats().duration.min:undefined)}</dd></div>
     <div><dt>Maximum Duration</dt><dd>{duration(stats().duration.count?stats().duration.max:undefined)}</dd></div>
     <div><dt>Duration Samples</dt><dd>{stats().duration.count.toLocaleString()}</dd></div>
    </>}</Show>
    <div><dt>Loaded Events</dt><dd>{props.loaded.toLocaleString()}</dd></div>
   </dl>
   <div class="insight-note"><span>{props.summary.statistics?.complete===false?`Statistics cover ${props.summary.statistics.evaluated?.toLocaleString()} of ${props.summary.count.toLocaleString()} matches; cold partial segments are not scanned.`:'Full matching range. Duration statistics exclude debug and trace events.'}</span></div>
  </section>
  <Show when={props.summary.statistics}>{stats=><>
   <Breakdown title="Services" values={stats().services.map(row=>[row.name,row.count])} total={props.summary.count} />
   <Breakdown title="Levels" values={stats().levels.map(row=>[row.name,row.count])} total={props.summary.count} levels />
  </>}</Show>
 </aside>
}
