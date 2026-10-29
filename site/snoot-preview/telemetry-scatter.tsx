import {createSignal,onCleanup} from 'solid-js';
import {use,type EChartsCoreOption} from 'echarts/core';
import {EffectScatterChart} from 'echarts/charts';
import {FastChart} from '../../client/components/fast-dashboard';
import type {FastResult} from '../../client/services/fast-dashboard';

use([EffectScatterChart]);

// A marketing example built from the same per-resource summary metrics as Snoot.
export function TelemetryScatter(props:{data:FastResult}){
 const motion=matchMedia('(prefers-reduced-motion: reduce)');
 const [reduced,setReduced]=createSignal(motion.matches);
 const update=()=>setReduced(motion.matches);
 motion.addEventListener('change',update);onCleanup(()=>motion.removeEventListener('change',update));
 const title='Error Rate vs. P95 Duration';
 const option=():EChartsCoreOption=>({
  grid:{left:2,right:14,top:10,bottom:2,containLabel:true},
  tooltip:{trigger:'item',renderMode:'richText',confine:true,formatter:(params:any)=>{
   const [rate,p95,events]=params.value;
   return `${params.name}\nError Rate: ${rate.toFixed(2)}%\nP95 Duration: ${p95.toFixed(2)} ms\nEvents: ${events.toLocaleString()}`;
  }},
  xAxis:{type:'value',min:0,max:5,axisLabel:{formatter:'{value}%',fontSize:10}},
  yAxis:{type:'value',min:0,max:500,splitNumber:2,axisLabel:{formatter:'{value} ms',fontSize:10}},
  series:[{type:'effectScatter',showEffectOn:reduced()?'emphasis':'render',
   rippleEffect:{period:5,scale:2.2,number:2,brushType:'stroke'},
   symbolSize:(value:number[])=>7+15*Math.sqrt(value[2]/Math.max(1,props.data.totals.events)),
   itemStyle:{opacity:.8},
   data:props.data.groups.resource.filter(r=>r.errorRate!==null&&r.p95!==null).map(r=>({name:r.key,value:[r.errorRate!,r.p95!/1000,r.events]})),
  }],
 });
 return <article class="card fast-panel marketing-scatter" data-preview-panel="bottom">
  <div class="fast-panel-heading"><h4>{title}</h4><p>One point per resource. Size shows event volume.</p></div>
  <FastChart option={option()} title={`${title}. X: error rate in percent. Y: P95 duration in milliseconds. One point per resource; size shows event volume.`}/>
 </article>;
}
