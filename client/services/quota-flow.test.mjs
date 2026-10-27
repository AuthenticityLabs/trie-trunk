import test from 'node:test';
import assert from 'node:assert/strict';
import {init, use} from 'echarts/core';
import {SankeyChart} from 'echarts/charts';
import {TooltipComponent} from 'echarts/components';
import {SVGRenderer} from 'echarts/renderers';
import {quotaFlow, quotaNodes, scopeKey} from './quota-policy.ts';
use([SankeyChart, SVGRenderer, TooltipComponent]);
const paths=[[],['cache'],['cache','svc'],['cache','svc','res'],['cache','svc','res','read'],['cache','svc','res','write'],['cache','svc','res','debug']];
const nodes=quotaNodes(paths.map((path,i)=>({path,kind:['trunk','cache','service','resource','branch'][path.length],allocated:i===4?10:i===5?90:0})));
test('fixed-height Sankey conserves bytes without minimum leaf inflation',()=>{
 for(const policy of [{poolBytes:null,scopes:[]},{poolBytes:1000,scopes:[{path:paths[4],limitBytes:200},{path:paths[5],limitBytes:300}]}]) {
  const flow=quotaFlow(nodes,policy),total=flow.get('[]');
  for(let depth=1;depth<=4;depth++)assert.equal(nodes.filter(n=>n.path.length===depth).reduce((sum,n)=>sum+(flow.get(n.id)??0),0),total);
  assert.equal(flow.get(scopeKey(paths[6])),0);
  const chart=init(null,undefined,{renderer:'svg',ssr:true,width:1000,height:640});
  try {
   chart.setOption({animation:false,series:[{type:'sankey',top:10,bottom:10,left:12,right:100,nodeGap:0,layoutIterations:0,nodeAlign:'left',data:nodes.map(n=>({name:n.id,depth:n.path.length,value:flow.get(n.id)??0})),links:nodes.filter(n=>n.path.length).map(n=>({source:scopeKey(n.path.slice(0,-1)),target:n.id,value:flow.get(n.id)??0}))}]});
   const graph=chart.getModel().getSeriesByIndex(0).getGraph();
   const trunk=graph.nodes.find(n=>n.id==='[]').getLayout();
   const leaves=graph.nodes.filter(n=>JSON.parse(n.id).length===4);
   assert.ok(Math.abs(trunk.dy-620)<1e-6);
   assert.ok(Math.abs(leaves.reduce((sum,n)=>sum+n.getLayout().dy,0)-trunk.dy)<1e-6);
   assert.ok(!chart.renderToSVGString().includes('NaN'));
  } finally {chart.dispose()}
 }
});

test('branch labels remain complete at negligible or zero allocation, without changing flow thickness', async()=>{
 const {LabelLayout}=await import('echarts/features');use([LabelLayout]);
 const {quotaTreeOption}=await import('./quota-tree.ts');
 const {storageScopeLabel}=await import('./storage-labels.ts');
 const inventory=[{path:[],kind:'trunk',allocated:0},{path:['cache'],kind:'cache',allocated:0}];
 for(let i=0;i<5;i++) {
  const root=['cache',`svc${i}`,'res'];
  inventory.push({path:root.slice(0,2),kind:'service',allocated:0},{path:root,kind:'resource',allocated:0});
  for(const branch of ['trace','debug','write','read','error']) inventory.push({path:[...root,branch],kind:'branch',allocated:branch==='write'?1_000_000:branch==='read'?900_000:branch==='trace'?0:1});
 }
 const catalog=quotaNodes(inventory), policy={poolBytes:null,scopes:[]};
 assert.deepEqual(catalog.filter(n=>n.kind==='branch').slice(0,5).map(n=>n.name),['Info - Read','Info - Write','Error','Debug','Trace']);
 assert.equal(storageScopeLabel(['cache','svc','res','read']),'cache / svc / res / Info - Read');
 assert.equal(catalog.find(n=>n.name==='Info - Read').path.at(-1),'read');
 for(const width of [340,1000]) {
  const chart=init(null,undefined,{renderer:'svg',ssr:true,width,height:640});
  try {
   chart.setOption({...quotaTreeOption(catalog,policy,[],width,{text:'#ddd',muted:'#aaa',surface:'#212121',line:'#444'}),animation:false});
   const svg=chart.renderToSVGString();assert.ok(!svg.includes('NaN'));
   for(const label of ['Info - Read','Info - Write','Error','Debug','Trace']) assert.equal(svg.split(`>${label}</text>`).length-1,5,`${label} missing at ${width}px`);
   const series=chart.getModel().getSeriesByIndex(0), data=series.getData();
   const bounds=[];
   catalog.forEach((n,index)=>{
    if(n.kind!=='branch')return;
    const text=data.getItemGraphicEl(index).getTextContent();
    const rect=text.getBoundingRect().clone();rect.applyTransform(text.getComputedTransform());bounds.push(rect);
   });
   bounds.sort((a,b)=>a.y-b.y);
   assert.ok(bounds[0].y>=0);
   for(let i=1;i<bounds.length;i++)assert.ok(bounds[i].y>=bounds[i-1].y+bounds[i-1].height-.01,`overlap at ${width}px`);
   assert.ok(bounds.at(-1).y+bounds.at(-1).height<=640);
   assert.ok(bounds.every(r=>r.x+r.width<=width),`branch name cropped at ${width}px`);
   const graph=series.getGraph(),trunk=graph.nodes.find(n=>n.id==='[]').getLayout();
   assert.ok(Math.abs(graph.nodes.filter(n=>JSON.parse(n.id).length===4).reduce((sum,n)=>sum+n.getLayout().dy,0)-trunk.dy)<1e-6);
  } finally {chart.dispose()}
 }
});

test('consumption fallback keeps the tree visible without changing configured management', async()=>{
 const {quotaTreeOption}=await import('./quota-tree.ts');
 const policy={poolBytes:1000,scopes:[]};
 const option=quotaTreeOption(nodes,policy,[],1000,{text:'#ddd',muted:'#aaa',surface:'#212121',line:'#444'},true);
 assert.equal(option.series[0].data[0].value,100);
 assert.equal(option.series[0].data[0].itemStyle.opacity,1);
 assert.equal(policy.poolBytes,1000);
 assert.equal(policy.managed,undefined);
});
