import {compileFields, type Dictionary, type LogEvent} from '../../client/services/dictionary';
import type {FastRequest, FastResult, Metrics} from '../../client/services/fast-dashboard';
import type {QueryField} from '../../client/services/query';

export const fixedNow = Date.UTC(2026, 9, 28);
const services = [
 {key:'prod/payment',resource:'transactions.v1',events:17400,errors:623,p95:349270},
 {key:'prod/identity',resource:'sessions.v1',events:6208,errors:93,p95:62400},
 {key:'prod/checkout',resource:'orders.v1',events:3400,errors:56,p95:213800},
];
export const sources = services.map(s=>`${s.key}/${s.resource}/read`);
const day = 86_400_000;
const metrics=(events:number,errors:number):Metrics=>({events,errors,errorRate:events?errors/events*100:0,samples:events,average:106670,p95:349270});
export function dashboardData(query:FastRequest):FastResult {
 const days=Math.min(300,Math.max(1,Math.round((query.to-query.from)/day))),from=fixedNow-days*day;
 const selected=services.filter(s=>query.scopes.some(scope=>s.key===scope||scope===`${s.key}/${s.resource}`));
 const count=selected.reduce((n,s)=>n+s.events,0),errors=selected.reduce((n,s)=>n+s.errors,0);
 const bins=days<=7?days*24:days,step=days*day/bins;
 const weights=Array.from({length:bins},(_,i)=>i===bins-1?.3:1+Math.sin(i*.8)*.03);
 const weight=weights.reduce((a,b)=>a+b,0);
 const amounts=weights.map(v=>Math.floor(count*v/weight));amounts[0]+=count-amounts.reduce((a,b)=>a+b,0);
 const errorsByTime=weights.map(v=>Math.floor(errors*v/weight));errorsByTime[0]+=errors-errorsByTime.reduce((a,b)=>a+b,0);
 const time=amounts.map((n,i)=>({time:from+i*step,...metrics(n,errorsByTime[i]),p95:i%7===0?1500000:349270}));
 return {from,to:fixedNow,step,packets:days*selected.length,matched:Math.round(count*27934/27008),totals:metrics(count,errors),time,
  comparison:{earlier:metrics(Math.round(count*.52),Math.round(errors*.52)),later:metrics(Math.round(count*.48),Math.round(errors*.48))},
  groups:{service:selected.map(s=>({key:s.key,label:s.key.replace('/',' / '),trend:amounts.map(n=>Math.round(n*s.events/Math.max(1,count))),...metrics(s.events,s.errors)})),
   resource:selected.map(s=>({key:`${s.key}/${s.resource}`,label:`${s.key.replace('/',' / ')} / ${s.resource}`,trend:amounts,...metrics(s.events,s.errors),p95:s.p95})),
   level:[{key:'info',label:'info',trend:amounts,...metrics(count-errors,0)},{key:'error',label:'error',trend:errorsByTime,...metrics(errors,errors)}]}};
}

const messages=[
 ['payment','transactions','authorize','Payment authorization completed','authorized',48200],
 ['identity','sessions','refresh','Session refreshed','active',9400],
 ['checkout','orders','submit','Order submitted','pending',127800],
 ['payment','transactions','capture','Payment captured','captured',86700],
 ['payment','transactions','authorize','Upstream authorization timed out','failed',620400],
 ['identity','sessions','validate','Session validated','active',7200],
] as const;
const names=['level','service','resource','operation','message','duration','state','request_id'];
const fields=compileFields({0:{name:'id',encoding:'uuidv7'},...Object.fromEntries(names.map((name,index)=>[index+1,{name}]))},{});
const dictionaries:Dictionary[]=sources.map((tap,i)=>({tap,version:0,digest:String(i+1).repeat(64),fields,id:fields.names.get('id')!,path:new Set<number>(),headers:[]}));
const layout={keys:names.map((_,i)=>i+1),fields:names.map(name=>fields.names.get(name)),positions:new Map(names.map((_,i)=>[i+1,i]))};
export const events:LogEvent[]=Array.from({length:48},(_,i)=>{
 const entry=messages[i%messages.length],service=services.findIndex(s=>s.key===`prod/${entry[0]}`);
 const time=Date.UTC(2026,9,28,10,42,Math.floor(i/3),i*13%1000),id=new Uint8Array(16);
 let value=time;for(let n=5;n>=0;n--){id[n]=value%256;value=Math.floor(value/256)}id[6]=0x70;id[8]=0x80;id[15]=i;
 const uuid=btoa(String.fromCharCode(...id));
 const values=[i%6===4?'ERROR':'INFO',entry[0],entry[1],entry[2],entry[3],entry[5],entry[4],`req_${String(i+1).padStart(4,'0')}`];
 return {dictionary:dictionaries[service],layout,values,uuid,millis:time} as LogEvent;
});
export const queryFields:QueryField[]=[
 ...names.map(name=>({name,type:name==='duration'?'U64':'STRING',values:name==='level'?['INFO','ERROR']:name==='service'?services.map(s=>s.key.split('/')[1]):name==='state'?['authorized','captured','pending','failed','active']:[],...(name==='duration'?{unit:'us' as const}:{})})),
];
export const rejected=[{received_at:'2026-10-28T10:42:07Z',source:sources[0],event_id:'0'.repeat(31)+'1',definition_version:'1'.repeat(64),finalized_before:'2026-10-28T10:00:00Z',reason:'Event arrived after its hourly seal.',event_json:'{"service":"payment","resource":"transactions","level":"ERROR","message":"Late event received","duration":620400}'}];
