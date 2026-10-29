// Fixed illustrative data, independent of the live datastore and benchmark.
export const chartServices=[
 {key:'payment',events:17400,errors:623,color:'blue',center:72},
 {key:'identity',events:6208,errors:93,color:'teal',center:14},
 {key:'checkout',events:3400,errors:56,color:'ochre',center:110},
];
const durations=[4,8,16,24,32,48,64,96,128,192,256,384,512,768,1024];
const distribute=(total,weights)=>{
 const sum=weights.reduce((a,b)=>a+b,0),values=weights.map(v=>Math.floor(total*v/sum));
 const remainder=total-values.reduce((a,b)=>a+b,0);
 const order=weights.map((v,i)=>({i,remainder:total*v/sum-values[i]})).sort((a,b)=>b.remainder-a.remainder);
 for(let n=0;n<remainder;n++)values[order[n].i]++;
 return values;
};
const serviceDays=chartServices.map((s,k)=>{
 const weights=Array.from({length:30},(_,i)=>1+.19*Math.sin(i*.49+k)+.08*Math.cos(i*1.17+k)-([5,6].includes(new Date(Date.UTC(2026,8,28+i)).getUTCDay())?.18:0));
 const counts=distribute(s.events,weights);
 const errors=distribute(s.errors,weights.map((v,i)=>v*(i===11||i===24?2.8:.8)));
 return counts.map((count,i)=>{
  const center=Math.log2(s.center)+.24*Math.sin(i*.48+k)+(i===11||i===24?.52:0);
  const histogram=distribute(count,durations.map(v=>Math.exp(-.5*((Math.log2(v)-center)/1.06)**2)));
  return {key:s.key,count,errors:errors[i],histogram};
 });
});
export const dateLabel=stamp=>new Intl.DateTimeFormat('en-US',{month:'short',day:'numeric',timeZone:'UTC'}).format(stamp);
export const number=value=>new Intl.NumberFormat('en-US').format(value);
export const milliseconds=value=>`${new Intl.NumberFormat('en-US',{maximumFractionDigits:1}).format(value)} ms`;
const compact=value=>new Intl.NumberFormat('en-US',{notation:'compact',maximumFractionDigits:1}).format(value);
export function chartView(days=30,service='all'){
 const services=chartServices.filter(s=>service==='all'||s.key===service);
 const rows=Array.from({length:days},(_,n)=>{
  const i=30-days+n,values=services.map(s=>serviceDays[chartServices.indexOf(s)][i]);
  const count=values.reduce((n,s)=>n+s.count,0),errors=values.reduce((n,s)=>n+s.errors,0);
  const histogram=durations.map((_,j)=>values.reduce((n,s)=>n+s.histogram[j],0));
  let cumulative=0;
  const percentile=durations[histogram.findIndex(n=>(cumulative+=n)>=count*.95)];
  return {stamp:Date.UTC(2026,8,28+i),values,count,errors,average:histogram.reduce((n,count,j)=>n+count*durations[j],0)/count,p95:percentile};
 });
 const count=rows.reduce((n,r)=>n+r.count,0),errors=rows.reduce((n,r)=>n+r.errors,0);
 return {rows,services,count,errors,range:`${dateLabel(rows[0].stamp)} – ${dateLabel(rows.at(-1).stamp)} · UTC`};
}
const text=(x,y,value,anchor='end')=>`<text x="${x}" y="${y}" text-anchor="${anchor}" class="home-chart-axis">${value}</text>`;
const axis=(height,max,width,left,bottom)=>[0,.5,1].map(f=>{
 const y=height-bottom-f*(height-bottom-14);
 return `<path class="home-chart-grid" d="M${left} ${y}H${width-12}"/>${text(left-9,y+3,compact(max*f))}`;
}).join('');
const dates=(rows,width,height,left)=>[0,Math.floor((rows.length-1)/2),rows.length-1].map(i=>text(left+(i+.5)*(width-left-12)/rows.length,height-6,dateLabel(rows[i].stamp),'middle')).join('');
export function activityPlot(view,width=620,height=258){
 const left=40,bottom=28,plot=width-left-12;
 const max=Math.ceil(Math.max(...view.rows.map(r=>r.count))/250)*250;
 const scale=(height-bottom-14)/max,step=plot/view.rows.length;
 return `${axis(height,max,width,left,bottom)}${view.rows.map((r,i)=>{
  let y=height-bottom;
  return r.values.map(s=>{const h=s.count*scale;y-=h;return `<rect x="${left+i*step+step*.18}" y="${y.toFixed(2)}" width="${(step*.64).toFixed(2)}" height="${h.toFixed(2)}" class="home-mark-${chartServices.find(v=>v.key===s.key).color}"/>`}).join('');
 }).join('')}${dates(view.rows,width,height,left)}<path class="home-chart-cursor" data-chart-cursor hidden d=""/>`;
}
export function durationPlot(view,width=340,height=134){
 const left=38,bottom=25,plot=width-left-12;
 const max=Math.ceil(Math.max(...view.rows.map(r=>r.p95))/100)*100;
 const x=i=>left+(i+.5)*plot/view.rows.length,y=value=>height-bottom-value/max*(height-bottom-14);
 const points=field=>view.rows.map((r,i)=>`${i?'L':'M'}${x(i).toFixed(2)} ${y(r[field]).toFixed(2)}`).join('');
 const average=points('average'),p95=points('p95');
 return `${axis(height,max,width,left,bottom)}<path class="home-duration-wash" d="${average}L${x(view.rows.length-1)} ${height-bottom}L${x(0)} ${height-bottom}Z"/><path class="home-chart-line home-line-teal" d="${average}"/><path class="home-chart-line home-line-violet" d="${p95}"/>${dates(view.rows,width,height,left)}<path class="home-chart-cursor" data-chart-cursor hidden d=""/>`;
}
export function levelPlot(view){
 return [['info',view.count-view.errors,'blue'],['error',view.errors,'coral']].map(([key,count,color])=>`<button type="button" class="home-level-row" data-home-level="${key}" aria-pressed="false"><span>${key.toUpperCase()}</span><span class="home-level-track"><i class="home-mark-${color}" style="width:${count/view.count*100}%"></i></span><span>${number(count)}</span></button>`).join('');
}
