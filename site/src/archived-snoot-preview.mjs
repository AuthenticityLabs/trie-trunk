import {icon} from './diagrams.mjs';
const spark = (values, label, area = false) => {
 const points=values.map((v,i)=>`${i*300/(values.length-1)},${94-v}`).join(' ');
 return `<svg class="preview-spark" viewBox="0 0 300 100" role="img" aria-label="${label}"><path class="preview-grid" d="M0 25H300M0 60H300M0 95H300"/>${area?`<polygon points="0,100 ${points} 300,100" fill="currentColor" opacity=".08"/>`:''}<polyline points="${points}" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/></svg>`;
};

const stats = [['Events','1,048,392','↑ 8.4%'],['Error Rate','0.24%','↓ 0.06 pts'],['Average Duration','42 ms','↓ 7 ms']];
const dash = `<div class="mock-heading"><div><span class="preview-eyebrow">Dashboards / Fast</span><h3>Service Overview</h3></div><span class="mock-chip">Last 7 Days</span></div>
 <div class="mock-stat-grid">${stats.map(([name,value,trend])=>`<div><span>${name}</span><strong>${value}</strong><small>${trend}</small></div>`).join('')}</div>
 <div class="mock-chart-card"><div><strong>Event Volume</strong><span>Precomputed Summaries</span></div>${spark([28,26,40,32,36,49,45,48,35,39,56,51,58,47,65,61,56,67,58,71,63,74,69,81], 'Sample event volume over seven days',true)}<div class="mock-axis"><span>Oct 21</span><span>Oct 24</span><span>Oct 28</span></div></div>
 <div class="mock-resource-row"><span><i class="mock-status"></i>payment / transactions</span><span>682,104 Events</span><b>38 ms</b></div><div class="mock-resource-row"><span><i class="mock-status"></i>identity / sessions</span><span>366,288 Events</span><b>49 ms</b></div>`;

const searchRows = [
 ['10:42:08.402','info','payment','Order completed · premiere / new_service'],
 ['10:42:08.318','info','identity','Session renewed · user 8c3f'],
 ['10:42:07.891','error','payment','Gateway timeout · retry scheduled'],
 ['10:42:07.620','info','payment','Payment captured · 49.00 USD'],
 ['10:42:07.414','info','identity','Permission checked · orders.read']
];
const search = `<div class="mock-heading"><div><span class="preview-eyebrow">Research / Events</span><h3>Search</h3></div><span class="mock-chip">Last Hour</span></div>
 <div class="mock-query">${icon('search')}<code>events | where service == "payment"</code><span>Search ${icon('chevron')}</span></div>
 <div class="mock-results-meta"><span>2,481 Matching Events</span><span>Collected Locally · 18 ms</span></div>
 <div class="mock-events"><div class="mock-event-head"><span>Time</span><span>Level</span><span>Event</span></div>${searchRows.filter(row=>row[2]==='payment').map(([time,level,service,copy])=>`<div class="mock-event"><time>${time}</time><span class="mock-level ${level}">${level}</span><div><small>${service} / transactions</small><span>${copy}</span></div></div>`).join('')}</div>
 <div class="mock-inspector"><span>Selected Event</span><code><b>product</b> "premiere"<br><b>type</b> "new_service"<br><b>completed_at</b> "2026-10-28T10:42:08Z"</code></div>`;

const alerts = `<div class="mock-heading"><div><span class="preview-eyebrow">Alerts / Overview</span><h3>Alerts</h3></div><span class="mock-chip">Example View</span></div>
 <div class="mock-alert-summary"><span class="mock-alert-icon">${icon('bell')}</span><div><strong>2 Signals to Review</strong><span>payment / transactions · Last Hour</span></div></div>
 <div class="mock-alert"><div><span class="mock-level error">Needs Review</span><time>10:38 UTC</time></div><h4>Payment Errors Above Baseline</h4><p>Error rate reached 1.8% across 342 events.</p>${spark([15,14,17,15,18,16,17,19,23,30,45,60,67,72], 'Sample payment error rate rising above baseline')}</div>
 <div class="mock-alert compact"><div><span class="mock-level warning">Lost Events</span><time>10:31 UTC</time></div><h4>12 Events Rejected at Ingress</h4><p>Original payloads retained for inspection.</p></div>`;

export const previousSnootPreview = `<div class="hero-visual app-preview" aria-label="Interactive Snoot Preview With Sample Data">
 <div class="app-preview-chrome"><span><i></i><i></i><i></i></span><span>snoot / local workspace</span><span class="sample-badge">Sample Data</span></div>
 <div class="app-preview-body"><aside class="mock-sidebar"><img src="/assets/brand/snoot-forward.png" width="64" height="64" alt="Front-Facing Snoot Anteater"/><span class="mock-workspace"><i class="mock-status"></i>Local Workspace</span><div role="tablist" aria-label="Snoot Preview" aria-orientation="vertical">${[['Dash','dashboard'],['Search','search'],['Alerts','bell']].map(([name,ico],i)=>`<button type="button" role="tab" id="hero-snoot-${i}" aria-controls="hero-snoot-panel-${i}" data-hero-group="snoot" data-hero-index="${i}" aria-selected="${i===0}" tabindex="${i===0?0:-1}">${icon(ico)}<span>${name}</span>${name==='Alerts'?'<small>2</small>':''}</button>`).join('')}</div><span class="mock-sidebar-foot">Snoot through<br>Telemetry data.</span></aside>
 <div class="mock-main">${[dash,search,alerts].map((content,i)=>`<div role="tabpanel" id="hero-snoot-panel-${i}" aria-labelledby="hero-snoot-${i}" tabindex="0" ${i?'hidden':''}>${content}</div>`).join('')}</div></div>
 <p class="preview-disclosure">Static product illustration · Sample data${' · Alerts preview is conceptual.'}</p>
</div>`;
