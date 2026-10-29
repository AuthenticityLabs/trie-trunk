import {createEffect,createSignal,onCleanup,onMount} from 'solid-js';
import {init,use} from 'echarts/core';
import {GaugeChart} from 'echarts/charts';
import {AriaComponent,TooltipComponent} from 'echarts/components';
import {SVGRenderer} from 'echarts/renderers';
import type {FastResult} from '../../client/services/fast-dashboard';

use([GaugeChart,AriaComponent,TooltipComponent,SVGRenderer]);

// Marketing duration gauge; the scale expresses duration, not an alert policy.
export function TelemetryGauge(props:{data:FastResult}){
 let element!:HTMLDivElement;
 const value=()=>props.data.totals.p95===null?null:props.data.totals.p95/1000;
 const label=()=>`P95 Duration: ${value()===null?'No Samples':value()!.toFixed(2)+' ms'}. 95th percentile event duration.`;
 onMount(()=>{
  const chart=init(element,undefined,{renderer:'svg'}),[revision,setRevision]=createSignal(0);
  const motion=matchMedia('(prefers-reduced-motion: reduce)');
  const redraw=()=>setRevision(n=>n+1);
  const theme=new MutationObserver(redraw);theme.observe(document.documentElement,{attributes:true,attributeFilter:['data-theme']});
  const resize=new ResizeObserver(()=>{chart.resize();redraw()});resize.observe(element);
  motion.addEventListener('change',redraw);
  createEffect(()=>{
   revision();
   const css=getComputedStyle(element),color=css.getPropertyValue('--chart-blue').trim()||'#a092b5';
   const text=css.getPropertyValue('--text-secondary').trim()||'#747474',track=css.getPropertyValue('--border').trim()||'#353535';
   const radius=Math.min(element.clientWidth*.37,element.clientHeight*.64);
   const center=Math.min(element.clientHeight-40,radius+24),duration=value();
   const maximum=Math.max(1000,Math.ceil((duration??0)/500)*500);
   chart.setOption({
    animation:!motion.matches,animationDuration:650,animationDurationUpdate:350,
    textStyle:{fontFamily:css.fontFamily},aria:{enabled:true,label:{description:label()}},
    tooltip:{trigger:'item',renderMode:'richText',confine:true,formatter:()=>`${label()}\n${props.data.totals.samples.toLocaleString()} Measured Events\nDisplay Scale: 0–${maximum.toLocaleString()} ms`},
    series:[{
     type:'gauge',startAngle:180,endAngle:0,min:0,max:maximum,splitNumber:4,
     center:['50%',center],radius,
     itemStyle:{color},progress:{show:true,roundCap:true,width:12},
     pointer:{show:duration!==null,length:'74%',width:5,itemStyle:{color}},
     anchor:{show:true,size:9,itemStyle:{color}},
     axisLine:{roundCap:true,lineStyle:{width:12,color:[[1,track]]}},
     axisTick:{distance:-20,splitNumber:2,length:4,lineStyle:{color:text,width:1}},
     splitLine:{distance:-23,length:8,lineStyle:{color:text,width:1}},
     axisLabel:{distance:30,fontSize:10,color:text,formatter:(v:number)=>v.toLocaleString()},
     title:{show:false},
     detail:{offsetCenter:[0,'18%'],valueAnimation:!motion.matches,formatter:()=>duration===null?'—':`{value|${duration.toFixed(2)}} {unit|ms}`,rich:{value:{fontSize:30,fontWeight:500,color:css.color},unit:{fontSize:13,color:text}}},
     data:duration===null?[]:[{value:duration,name:'P95 Duration'}],
    }],
   },{notMerge:true});
  });
  onCleanup(()=>{theme.disconnect();resize.disconnect();motion.removeEventListener('change',redraw);chart.dispose()});
 });
 return <article class="card fast-panel marketing-gauge" data-preview-panel="duration-trend">
  <div class="fast-panel-heading"><h4>P95 Duration</h4><p>95th percentile event duration.</p></div>
  <div ref={element} class="fast-chart" role="img" aria-label={label()}/>
  <span class="gauge-scale-label">Display Scale: 0–{Math.max(1000,Math.ceil((value()??0)/500)*500).toLocaleString()} ms</span>
 </article>;
}
