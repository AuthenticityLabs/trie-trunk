import {chartView,activityPlot,durationPlot,levelPlot,chartServices} from './archived-homepage-charts-model.mjs';
const view=chartView();
const plot=(kind,title,content,width,height)=>`<svg data-home-plot="${kind}" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" tabindex="0" role="slider" aria-label="${title}: Explore Days" aria-describedby="home-chart-help" aria-valuemin="0" aria-valuemax="29" aria-valuenow="29" aria-valuetext="Oct 27" aria-orientation="horizontal">${content}</svg>`;
export const homepageCharts=`<div class="home-dashboard" data-home-dashboard>
 <div class="home-dashboard-toolbar"><span class="home-example-label">Example Data</span><div>
  <label>Service <select data-home-service><option value="all">All Services</option>${chartServices.map(s=>`<option value="${s.key}">${s.key}</option>`).join('')}</select></label>
  <label>Time Window <select data-home-days><option value="30">Last 30 Days</option><option value="7">Last 7 Days</option></select></label>
 </div></div>
 <div class="home-chart-layout">
  <figure class="home-chart-card home-activity-card"><figcaption><h2>Event Activity</h2><span data-home-range>${view.range}</span></figcaption>
   ${plot('activity','Event Activity',activityPlot(view),620,258)}
   <div class="home-chart-legend" data-home-services>${chartServices.map(s=>`<span><i class="home-mark-${s.color}"></i>${s.key}</span>`).join('')}</div>
  </figure>
  <figure class="home-chart-card home-duration-card"><figcaption><h2>Duration Trend</h2><span>ms</span></figcaption>
   ${plot('duration','Duration Trend',durationPlot(view),340,134)}
   <div class="home-chart-legend"><span><i class="home-mark-teal"></i>Average</span><span><i class="home-mark-violet"></i>P95</span></div>
  </figure>
  <figure class="home-chart-card home-level-card"><figcaption><h2>Event Levels</h2><span data-home-total>27,008 Events</span></figcaption><div data-home-levels>${levelPlot(view)}</div></figure>
 </div>
 <p class="home-chart-help" id="home-chart-help">Hover to inspect. Click to pin. Use arrow keys to explore a chart.</p>
 <div class="home-chart-tooltip" data-home-tooltip role="status" aria-live="polite" hidden></div>
</div>`;
