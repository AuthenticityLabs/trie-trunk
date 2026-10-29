import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import {dirname,resolve} from 'node:path';
import {build} from '../../client/node_modules/vite/dist/node/index.js';
const here=dirname(fileURLToPath(import.meta.url)),client=resolve(here,'../../client');
const require=createRequire(resolve(client,'package.json'));
const solid=require('vite-plugin-solid');
export async function buildSnootPreview(outDir){
 const virtual=resolve(here,'__actual-alerts.tsx');
 await build({configFile:false,root:here,base:'/snoot-preview/',publicDir:false,logLevel:'warn',
  resolve:{alias:[
   {find:/^echarts\/core$/,replacement:require.resolve('echarts/core')},
   {find:/^echarts\/charts$/,replacement:require.resolve('echarts/charts')},
   {find:/^echarts\/components$/,replacement:require.resolve('echarts/components')},
   {find:/^echarts\/renderers$/,replacement:require.resolve('echarts/renderers')},
   {find:/^solid-js$/,replacement:require.resolve('solid-js/dist/solid.js')},
   {find:/^solid-js\/web$/,replacement:require.resolve('solid-js/web/dist/web.js')},
   {find:/^solid-js\/store$/,replacement:require.resolve('solid-js/store/dist/store.js')},
  ],dedupe:['solid-js']},
  plugins:[{name:'snoot-static-data',enforce:'pre',
   transform(code,id){
    if(id.split('?')[0]!==resolve(client,'components/fast-dashboard.tsx'))return;
    const article='<article class="card fast-panel" style=';
    if(!code.includes(article))throw Error('Unable to identify the actual Snoot chart panels');
    const panels='each={fastDashboard.panels}';
    if(!code.includes(panels))throw Error('Unable to identify the actual Snoot dashboard definition');
    const panelView='panel=><FastPanel panel={panel} data={data()}/>';
    if(!code.includes(panelView))throw Error('Unable to identify the actual Snoot dashboard panel view');
    return `import {previewPanels} from ${JSON.stringify(resolve(here,'panels.ts'))};\nimport {TelemetryScatter} from ${JSON.stringify(resolve(here,'telemetry-scatter.tsx'))};\nimport {TelemetryGauge} from ${JSON.stringify(resolve(here,'telemetry-gauge.tsx'))};\n`+code
     .replace(article,'<article class="card fast-panel" data-preview-panel={p.id} style=')
     .replace(panels,'each={previewPanels(fastDashboard.panels,document.documentElement.dataset.preview)}')
     .replace(panelView,"panel=>document.documentElement.dataset.preview==='marketing'&&panel.id==='duration-trend'?<TelemetryGauge data={data()}/>:document.documentElement.dataset.preview==='marketing'&&panel.id==='bottom'?<TelemetryScatter data={data()}/>:<FastPanel panel={panel} data={data()}/>");
   },
   resolveId(source,importer){
    if(source==='virtual:snoot-alerts')return virtual;
    if(!importer||importer.startsWith(here))return;
    const target=source.startsWith('.')?resolve(dirname(importer.split('?')[0]),source).replace(/\.tsx?$/,''):source;
    const adapters={'services/pil/client':'client.ts','services/dashboard-subscriptions':'dashboard.ts','services/rejections':'rejections.ts','services/notebooks':'notebooks.ts'};
    for(const [module,file] of Object.entries(adapters))if(target===resolve(client,module))return resolve(here,file);
   },
   async load(id){
    if(id!==virtual)return;
    const app=await readFile(resolve(client,'app.tsx'),'utf8'),start=app.indexOf('<Show when={page() === "Alerts"}>');
    const section=app.slice(start),from=section.indexOf('<div class="page-intro">'),to=section.indexOf('</AlertCenter>');
    if(start<0||from<0||to<0)throw Error('Unable to locate the actual Snoot Alerts content');
    return `import {Icon} from ${JSON.stringify(resolve(client,'components/icon.tsx'))};export function ActualAlerts(props){const navigate=props.onNavigate;return <>${section.slice(from,to)}</>}`;
   }
  },solid()],
  build:{outDir,emptyOutDir:true,target:'es2022',sourcemap:false,assetsInlineLimit:0},
 });
}
