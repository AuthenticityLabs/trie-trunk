import {chartView,activityPlot,durationPlot,levelPlot,number,milliseconds,dateLabel} from './archived-homepage-charts-model.mjs';

export function initHomepageCharts(root){
 if(!root)return;
 const service=root.querySelector('[data-home-service]'),days=root.querySelector('[data-home-days]');
 const plots=[...root.querySelectorAll('[data-home-plot]')],tooltip=root.querySelector('[data-home-tooltip]');
 let view=chartView(),index=view.rows.length-1,pinned=false,active=null;
 function paint(plot){
  const bounds=plot.getBoundingClientRect(),width=Math.round(bounds.width),height=Math.round(bounds.height);
  plot.setAttribute('viewBox',`0 0 ${width} ${height}`);
  plot.innerHTML=plot.dataset.homePlot==='activity'?activityPlot(view,width,height):durationPlot(view,width,height);
 }
 function clear(){
  pinned=false;active=null;tooltip.hidden=true;
  plots.forEach(p=>p.querySelector('[data-chart-cursor]').setAttribute('hidden',''));
  root.querySelectorAll('[data-home-level]').forEach(b=>b.setAttribute('aria-pressed','false'));
 }
 function position(anchor,event){
  const bounds=root.getBoundingClientRect(),rect=anchor.getBoundingClientRect();
  const x=event?event.clientX-bounds.left:rect.left-bounds.left+rect.width*.4;
  const y=event?event.clientY-bounds.top:rect.top-bounds.top+20;
  tooltip.style.left=`${Math.max(8,Math.min(x+14,bounds.width-tooltip.offsetWidth-8))}px`;
  tooltip.style.top=`${Math.max(8,Math.min(y+14,bounds.height-tooltip.offsetHeight-8))}px`;
 }
 function show(plot,event){
  active=plot;
  const row=view.rows[index];
  plots.forEach(p=>{
   const activity=p.dataset.homePlot==='activity',width=p.viewBox.baseVal.width,left=activity?40:38,height=p.viewBox.baseVal.height-(activity?28:25);
   const x=left+(index+.5)*(width-left-12)/view.rows.length,cursor=p.querySelector('[data-chart-cursor]');
   cursor.setAttribute('d',`M${x} 14V${height}`);cursor.removeAttribute('hidden');
   p.setAttribute('aria-valuenow',index);
   p.setAttribute('aria-valuetext',`${dateLabel(row.stamp)}: ${number(row.count)} events, average ${milliseconds(row.average)}, P95 ${milliseconds(row.p95)}`);
  });
  tooltip.innerHTML=`<strong>${dateLabel(row.stamp)} · UTC</strong>${plot.dataset.homePlot==='activity'
   ?row.values.map(s=>`<div><span>${s.key}</span><b>${number(s.count)}</b></div>`).join('')+`<div class="home-tooltip-total"><span>Events</span><b>${number(row.count)}</b></div>`
   :`<div><span>Average</span><b>${milliseconds(row.average)}</b></div><div><span>P95</span><b>${milliseconds(row.p95)}</b></div>`}`;
  tooltip.hidden=false;position(plot,event);
 }
 function pick(plot,event){
  const rect=plot.getBoundingClientRect(),width=plot.viewBox.baseVal.width,left=plot.dataset.homePlot==='activity'?40:38;
  const x=(event.clientX-rect.left)/rect.width*width;
  index=Math.max(0,Math.min(view.rows.length-1,Math.floor((x-left)/(width-left-12)*view.rows.length)));
 }
 plots.forEach(plot=>{
  plot.addEventListener('pointermove',event=>{if(pinned)return;pick(plot,event);show(plot,event)});
  plot.addEventListener('pointerleave',()=>{if(!pinned)clear()});
  plot.addEventListener('click',event=>{
   if(pinned&&active===plot){clear();return}
   pick(plot,event);pinned=true;show(plot,event);
  });
  plot.addEventListener('focus',()=>{if(!pinned)show(plot)});
  plot.addEventListener('blur',()=>{if(!pinned)clear()});
  plot.addEventListener('keydown',event=>{
   if(event.key==='Escape'){clear();return}
   if(event.key==='Enter'||event.key===' '){event.preventDefault();pinned=!pinned;show(plot);return}
   if(!['ArrowLeft','ArrowRight','Home','End'].includes(event.key))return;
   event.preventDefault();
   index=event.key==='Home'?0:event.key==='End'?view.rows.length-1:Math.max(0,Math.min(view.rows.length-1,index+(event.key==='ArrowRight'?1:-1)));
   show(plot);
  });
 });
 function showLevel(button,event){
  active=button;
  const error=button.dataset.homeLevel==='error',count=error?view.errors:view.count-view.errors;
  tooltip.innerHTML=`<strong>${error?'ERROR':'INFO'} · ${view.range}</strong><div><span>Events</span><b>${number(count)}</b></div><div><span>Share</span><b>${(count/view.count*100).toFixed(2)}%</b></div>`;
  tooltip.hidden=false;position(button,event);
 }
 const levels=root.querySelector('[data-home-levels]');
 levels.addEventListener('pointermove',event=>{const button=event.target.closest('[data-home-level]');if(button&&!pinned)showLevel(button,event)});
 levels.addEventListener('pointerleave',()=>{if(!pinned)clear()});
 levels.addEventListener('focusin',event=>{if(!pinned)showLevel(event.target)});
 levels.addEventListener('focusout',()=>{if(!pinned)clear()});
 levels.addEventListener('keydown',event=>{if(event.key==='Escape')clear()});
 levels.addEventListener('click',event=>{
  const button=event.target.closest('[data-home-level]');if(!button)return;
  if(pinned&&active===button){clear();return}
  clear();pinned=true;button.setAttribute('aria-pressed','true');showLevel(button,event.detail?event:undefined);
 });
 function update(){
  clear();view=chartView(Number(days.value),service.value);index=view.rows.length-1;
  plots.forEach(paint);
  plots.forEach(p=>{p.setAttribute('aria-valuemax',index);p.setAttribute('aria-valuenow',index);p.setAttribute('aria-valuetext',dateLabel(view.rows[index].stamp))});
  root.querySelector('[data-home-range]').textContent=view.range;
  root.querySelector('[data-home-total]').textContent=`${number(view.count)} Events`;
  root.querySelector('[data-home-services]').innerHTML=view.services.map(s=>`<span><i class="home-mark-${s.color}"></i>${s.key}</span>`).join('');
  levels.innerHTML=levelPlot(view);
 }
 service.addEventListener('change',update);days.addEventListener('change',update);
 const sizes=new WeakMap();
 const resizePlots=new ResizeObserver(entries=>{
  for(const {target} of entries){
   const rect=target.getBoundingClientRect(),size=`${Math.round(rect.width)},${Math.round(rect.height)}`;
   if(sizes.get(target)===size)continue;
   sizes.set(target,size);paint(target);
  }
  if(plots.includes(active))show(active);
 });
 plots.forEach(plot=>resizePlots.observe(plot));
 new MutationObserver(()=>{if(!pinned)clear()}).observe(document.documentElement,{attributes:true,attributeFilter:['data-theme']});
}
