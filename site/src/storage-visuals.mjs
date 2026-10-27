import {quotaRings} from '../../shared/quota-rings.mjs';
const periods = ['Yearly','Monthly','Daily','Hourly','15-Minute','5-Minute'];
const bars = Array.from({length:12},(_,i)=>{
 const height=25+((i*31+17)%68);
 return `<rect x="${i*34+9}" y="${125-height}" width="23" height="${height}" rx="2"/>`;
}).join('');
export const summaries = `
<section class="wrap summaries" id="analytics">
 <div class="section-heading"><h2>Analytics</h2><p>Precomputed columnar aggregates for counts, errors, and numeric statistics. Collect summaries independently of event detail and combine them across time views.</p></div>
 <div class="aggregate-explorer">
  <div class="aggregate-periods" role="group" aria-label="Aggregate Granularity">${periods.map((period,i)=>`<button type="button" data-aggregate-period="${i}" aria-pressed="${i===1}">${period}</button>`).join('')}</div>
  <div class="aggregate-chart-heading"><strong id="aggregate-title">Monthly View</strong><span>Illustrative Aggregate Views</span></div>
  <svg class="aggregate-chart" viewBox="0 0 420 150" role="img" aria-label="Columnar aggregates shown at different time resolutions"><path class="aggregate-grid" d="M0 25H420M0 75H420M0 125H420"/><g id="aggregate-bars">${bars}</g></svg>
  <div class="aggregate-axis"><span id="aggregate-from">Jan</span><span id="aggregate-to">Dec</span></div>
 </div>
 <div class="aggregate-foot"><span class="mono-label">5-Minute Base Granularity</span><p>Combine stored aggregates for broader time views. Collect individual events when the investigation needs their detail.</p></div>
</section>`;
const rings=quotaRings(192,0,256).map(p=>`<path class="quota-${p.role}" d="${p.d}"/>`).join('');
export const retention = `
<section class="wrap retention-section" id="retention">
 <div class="section-heading"><h2>Retention</h2><p>Give each branch its own budget. Hollow out your trunk to store more data, with different storage allocations for services, resources, and log levels.</p></div>
 <div class="retention-explorer">
  <div class="retention-rings"><svg viewBox="0 0 320 320" role="img" aria-label="Fixed tree rings. A larger quota creates a larger hollow."><g id="quota-rings">${rings}</g></svg><div class="retention-ring-key"><span>Hollow = Quota</span><span>Larger Quota → Larger Hollow</span></div></div>
  <div class="retention-controls">
   <span class="mono-label">Selected Scope</span><p class="retention-scope">prod / payment / transactions</p>
   <label for="retention-budget">Storage Quota</label><output id="retention-value" for="retention-budget">192 GB</output>
   <input id="retention-budget" type="range" min="32" max="256" step="any" value="192" aria-valuetext="192 GB"/>
   <div class="retention-scale"><span>32 GB</span><span>256 GB</span></div>
   <div class="retention-history">
    <span class="retention-history-label">Estimated History at Capacity</span><output id="retention-days" for="retention-budget">48 Days</output>
   </div>
   <p id="retention-caption">Illustration: 4 GB/day. Snoot estimates from your data and saves the size.</p>
   <a class="text-link" href="https://snoot.trietrunk.com">Configure Quotas in Snoot ↗</a>
  </div>
 </div>
</section>`;
