import dashboardDefinition from './fast-dashboard.json' with { type: 'json' };
import { summaryCells, type DailyAnalytics, type DecodedDay } from './pil/summaries.ts';

export const dayMs = 86_400_000;
export type FastRequest = { scopes: string[]; from: number; to: number };
export type Metrics = { events: number; errors: number; errorRate: number | null; samples: number; average: number | null; p95: number | null };
export type Metric = 'events' | 'errors' | 'errorRate' | 'average' | 'p95';
export type Group = Metrics & { key: string; label: string; trend: number[] };
export type FastResult = {
  from: number; to: number; step: number; packets: number; matched: number; totals: Metrics;
  time: (Metrics & { time: number })[];
  comparison: {earlier: Metrics; later: Metrics};
  groups: Record<'service' | 'resource' | 'level', Group[]>;
};
type Accumulator = { events: number; errors: number; histogram: Map<number, number> };
const accumulator = (): Accumulator => ({ events: 0, errors: 0, histogram: new Map() });
function add(a: Accumulator, c: { count: number; errors: number; histogram: [number, number][] }, budget:{entries:number}) {
  a.events += c.count; a.errors += c.errors;
  if (!Number.isSafeInteger(a.events) || !Number.isSafeInteger(a.errors)) throw Error('Dashboard count exceeds the exact numeric range.');
  for (const [value, weight] of c.histogram) {
    if(!a.histogram.has(value)&&++budget.entries>1_000_000)throw Error('This summary selection exceeds the chart memory budget. Select fewer services or a shorter time window.');
    a.histogram.set(value, (a.histogram.get(value) ?? 0) + weight);
  }
}
function metrics(a: Accumulator): Metrics {
  let samples = 0, average = 0, p95: number | null = null;
  for (const [value, weight] of a.histogram) { samples += weight; average += (value - average) * weight / samples; }
  const rank = samples - Math.floor(samples / 20);
  let seen = 0;
  for (const [value, weight] of [...a.histogram].sort((a,b) => a[0]-b[0])) { seen += weight; if (seen >= rank) { p95 = value; break; } }
  return { events:a.events, errors:a.errors, errorRate:a.events ? a.errors/a.events*100 : null, samples, average:samples ? average : null, p95 };
}
export function validateFastRequest(request: FastRequest) {
  const { scopes, from, to } = request;
  if (!Array.isArray(scopes) || !scopes.length || scopes.length > 256 || scopes.some(s => typeof s !== 'string' || !/^[a-z][a-z0-9._-]{0,127}\/[a-z][a-z0-9._-]{0,127}(\/[a-z][a-z0-9._-]{0,127})?$/.test(s)) ||
      !Number.isSafeInteger(from) || !Number.isSafeInteger(to) || from < 0 || from % dayMs || to % dayMs || to <= from || to - from > 366*dayMs)
    throw Error('Select up to one year of complete UTC days and valid subscribed services.');
  // Parent/child overlap would count the same physical records twice.
  if (scopes.some((s,i) => scopes.some((t,j) => i !== j && (s === t || s.startsWith(t+'/'))))) throw Error('Dashboard scopes overlap.');
}
export class FastProjection {
  private total = accumulator();
  private halves = [accumulator(),accumulator()];
  private budget = {entries:0};
  private times: Accumulator[];
  private groups = { service:new Map<string, {label:string; data:Accumulator; trend:number[]}>(), resource:new Map<string, {label:string; data:Accumulator; trend:number[]}>(), level:new Map<string, {label:string; data:Accumulator; trend:number[]}>() };
  private identities = new Set<string>();
  private packets = 0;
  private matched = 0;
  readonly from: number;
  readonly to: number;
  readonly step: number;
  constructor(request: FastRequest) {
    validateFastRequest(request); this.from=request.from; this.to=request.to;
    this.step = this.to-this.from <= 7*dayMs ? 3_600_000 : dayMs;
    this.times=Array.from({length:Math.ceil((this.to-this.from)/this.step)},accumulator);
  }
  get count() { return this.total.events; }
  cover(count:number,represented:number) {
    if(!Number.isSafeInteger(count)||count<represented||represented<0||!Number.isSafeInteger(this.matched+count))throw Error('Invalid dashboard summary coverage.');
    this.matched+=count;
  }
  consume(manifest: DailyAnalytics, days: DecodedDay[]) {
    if (days.length !== manifest.days.length) throw Error('Missing dashboard summary packets.');
    days.forEach((day,index) => {
      const ref=manifest.days[index], identity=JSON.stringify([ref.source,ref.definition,ref.generation,ref.day]);
      if(this.identities.has(identity))throw Error('Duplicate dashboard summary source.');
      this.identities.add(identity); this.packets++;
      const [cache,service,resource]=ref.source.split('/');
      for(const cell of summaryCells(day,Math.max(this.from,manifest.from),Math.min(this.to,manifest.to))) {
        const bin=Math.floor((cell.time-this.from)/this.step);
        add(this.total,cell,this.budget); add(this.times[bin],cell,this.budget);
        add(this.halves[cell.time < (this.from+this.to)/2 ? 0 : 1],cell,this.budget);
        const dimensions = {service:[`${cache}/${service}`,`${cache} / ${service}`], resource:[`${cache}/${service}/${resource}`,`${cache} / ${service} / ${resource}`], level:[cell.labels[3],cell.labels[3]]};
        for(const dim of ['service','resource','level'] as const) {
          const [key,label]=dimensions[dim], map=this.groups[dim];
          let group=map.get(key);
          if(!group){if(map.size>=10000)throw Error('Select a smaller scope to chart these summaries.');group={label,data:accumulator(),trend:Array(this.times.length).fill(0)};map.set(key,group);}
          add(group.data,cell,this.budget);group.trend[bin]+=cell.count;
        }
      }
    });
  }
  finish(): FastResult {
    const groups = Object.fromEntries(Object.entries(this.groups).map(([dimension,map]) => [dimension,[...map].map(([key,g])=>({key,label:g.label,trend:g.trend,...metrics(g.data)})).sort((a,b)=>a.key.localeCompare(b.key))])) as FastResult['groups'];
    return { from:this.from,to:this.to,step:this.step,packets:this.packets,matched:this.matched,totals:metrics(this.total),comparison:{earlier:metrics(this.halves[0]),later:metrics(this.halves[1])},time:this.times.map((a,i)=>({time:this.from+i*this.step,...metrics(a)})),groups };
  }
}

/** A visual editor will write this contract; neither executable JS nor chart-library options. */
export type PanelDefinition = {
  id: string; title: string; span: 3 | 4 | 6 | 12;
  data: { shape:'number'|'boolean'|'set'|'table'|'series'; metric:Metric; groupBy?:'service'|'resource'|'level'; order?:'asc'|'desc'; limit?:number; test?:{op:'gt';value:number} };
  view: { kind:'value'|'status'|'tags'|'table'|'timechart'|'bars'|'list-trends'; unit:'count'|'percent'|'ms'; columns?:Metric[]; indicator?:'trend'|'sparkline'|'none' };
};
export type DashboardDefinition = { id:string; title:string; mode:'fast'; source:'summaries'; scope:'subscriptions'; time:{preset:'30d';timezone:'UTC';completeDays:true}; layout:{columns:12;mobileColumns:1}; panels:PanelDefinition[] };
export const fastDashboard = dashboardDefinition as DashboardDefinition;

// Symmetric relative change: doubling and halving tilt equally (45 degrees).
// Under 1% stays horizontal; zero-to-positive reaches vertical without infinity.
export function trendAngle(earlier:number|null,later:number|null):number|null {
  if(earlier===null||later===null||!Number.isFinite(earlier)||!Number.isFinite(later)||earlier<0||later<0)return null;
  const scale=Math.max(earlier,later);
  const change=scale?(later-earlier)/scale:0;
  return Math.abs(change)<.01?0:change*90;
}

// Uniform time buckets retain their horizontal position across missing data.
// Use a zero baseline so small fluctuations are not magnified into large swings.
export function sparklinePath(values:(number|null)[]):string {
  let maximum=0;
  for(const value of values) if(value!==null&&Number.isFinite(value)) maximum=Math.max(maximum,value);
  let connected=false,path='';
  values.forEach((value,index)=>{
    if(value===null||!Number.isFinite(value)){connected=false;return;}
    const x=1+index*98/Math.max(1,values.length-1),y=26-(maximum?value/maximum*24:0);
    path+=`${connected?'L':'M'}${x},${y}`;
    // A round-capped zero-length segment makes isolated observations visible.
    if(!connected)path+=`L${x},${y}`;
    connected=true;
  });
  return path;
}

export function panelGroups(panel: PanelDefinition, data: FastResult): Group[] {
  const rows=panel.data.groupBy ? [...data.groups[panel.data.groupBy]] : [];
  const {metric,order,limit}=panel.data;
  rows.sort((a,b)=>((order==='asc'?1:-1)*((a[metric]??-Infinity)-(b[metric]??-Infinity)))||a.key.localeCompare(b.key));
  return limit ? rows.slice(0,limit) : rows;
}
export function chartValue(value:number|null,unit:PanelDefinition['view']['unit']) {
  // Trie-Trunk's duration role is explicitly microseconds, independent of its field name.
  return value===null?null:unit==='ms'?value/1000:value;
}
export function chartCSV(rows: Group[], columns: Metric[]) {
  const cell=(v:unknown)=>{const s=String(v??'');return '"'+(/^[=+@\-\t\r]/.test(s)?"'":'')+s.replaceAll('"','""')+'"'};
  return [['Resource',...columns.map(c=>c==='average'||c==='p95'?`${c}_us`:c==='errorRate'?'errorRate_percent':c)],...rows.map(r=>[r.key,...columns.map(c=>r[c])])].map(row=>row.map(cell).join(',')).join('\r\n');
}
