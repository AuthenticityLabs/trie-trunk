import type {Archive} from "../store/protocol.ts";
import {unsignedNumber as compact,unsignedBig as compactBig,shuffleColumns,type ColumnSpan,type UnsignedEncoding} from 'pill-packer/columns';
import {expandPAC,pacHeader} from './pac.ts';
import { readSummaryPacks, summaryBatchLimit } from './summary-packs.ts';
import {fetchCredentialScope} from '../connections.ts';
import { queueSummaryPacket, storeSummaryPacket } from './summary-storage.ts';
import type { ViewSummary, ViewStatistics } from './view.ts';
import {encodeObject} from 'pill-packer/object';
export type SummaryDayReference = {
  key: string;
  source: string;
  definition: string;
  generation: string;
  day: number;
  sealed: boolean;
};
// UTC coarse buckets cover sealed daily totals only. Strings preserve exact U64
// values; filtered charts and local-calendar edges still use five-minute data.
export type CalendarRollup = {
  resolution: 'week' | 'month' | 'year';
  from: number; to: number; coveredFrom: number; coveredTo: number;
  statisticsComplete: boolean;
  count: string; durationCount: string; sum: string; min: string; max: string; errors: string;
};
export type DailyAnalytics = {
  rollups?: {source: string; definition: string; generation: string; buckets: CalendarRollup[]}[];
  from: number;
  to: number;
  days: SummaryDayReference[];
  border?: {
    labels: string[];
    count: number;
    errors: number;
    durations: [number, number][] | null;
  }[];
};
const hour = 3600000,
  bucket = 300000,
  day = 24 * hour,
  maximum = 128 * 1024 * 1024;
const decoder = new TextDecoder('utf-8', {
  fatal: true
});
const summaryDefinition={pil:0,definition:'trie-trunk/daily-summary',version:0,fields:[
  {name:'day',type:'U64'},{name:'from',type:'U64'},{name:'to',type:'U64'},
  {name:'hour',type:'U64'},{name:'sealed',type:'BOOL'},{name:'encoding',type:'BYTES'},{name:'columns',type:'BYTES'}
]};
let summaryDigest:Promise<string>|undefined;
function expectedSummaryDigest() {
  return summaryDigest??=crypto.subtle.digest('SHA-256',encodeObject(summaryDefinition).slice().buffer as ArrayBuffer).then(raw=>
    [...new Uint8Array(raw)].map(b=>b.toString(16).padStart(2,'0')).join(''));
}
function u64(v: DataView, at: number, exact = true) {
  const n = Number(v.getBigUint64(at, true));
  if (exact && !Number.isSafeInteger(n)) throw Error('Summary integer exceeds safe range');
  return n;
}
function safeAdd(a: number, b: number) {
  const n = a + b;
  if (!Number.isSafeInteger(n) || n < 0) throw Error('Summary count overflow');
  return n;
}
function magic(b: Uint8Array, s: string) {
  return b.length >= s.length && s.split('').every((c, i) => b[i] === c.charCodeAt(0));
}
type NumericEncoding=UnsignedEncoding;
const encodingSize = 81;
function readEncoding(v: DataView, at: number): NumericEncoding[] {
  return Array.from({length: 9}, (_, i) => {
    const width = v.getUint8(at + i * 9), base = v.getBigUint64(at + i * 9 + 1, true);
    if (width < 1 || width > 8 || ![3, 4, 7].includes(i) && base !== 0n || i === 2 && width !== 8)
      throw Error('Invalid summary column encoding');
    return {width, base};
  });
}
function unshuffleSummary(bytes: Uint8Array, enc: NumericEncoding[]) {
  if (bytes.length < 116 || !magic(bytes, 'SUMCOL00') || bytes[48] !== 1)
    throw Error('Invalid shuffled summary');
  const v = new DataView(bytes.buffer, bytes.byteOffset, bytes.length);
  const n = v.getUint32(32, true), m = v.getUint32(36, true), p = v.getUint32(40, true),
    d = v.getUint32(44, true), w = enc.map(c => c.width);
  const expected = 116 + n * (2 + w[0] + w[1] + w[2] + w[3] + w[4])
    + m * (13 + w[5] + w[6]) + p * (w[7] + w[8]) + d;
  if (expected !== bytes.length) throw Error('Invalid shuffled column lengths');
  let at=116+n*2;const spans:ColumnSpan[]=[];
  const column=(count:number,width:number)=>{spans.push({offset:at,count,width});at+=count*width;};
  for (let k = 0; k < 5; k++) column(n, w[k]);
  at += m * 5;
  column(m, w[5]); column(m, w[6]);
  at += m * 8;
  column(p, w[7]); column(p, w[8]);
  shuffleColumns(bytes,spans,true);
}
export type SummaryHour = {
  encoding: NumericEncoding[];
  bytes: Uint8Array;
  view: DataView;
  time: number;
  rows: number;
  stats: number;
  pairs: number;
  boundaries: Uint32Array;
  labels: string[][];
  ref: number;
  count: number;
  durationCount: number;
  sum: number;
  min: number;
  max: number;
  statBucket: number;
  statLabel: number;
  statCount: number;
  statErrors: number;
  histStart: number;
  histEnd: number;
  histValue: number;
  histCount: number;
};
export function decodeSummaryHour(bytes: Uint8Array, shared?:NumericEncoding[]): SummaryHour {
  if (bytes.length < 116 || !magic(bytes, 'SUMCOL00') || bytes.subarray(50,64).some(v=>v!==0)||bytes[48]>1||(bytes[48]===1)!==!!shared||![0,1,2,4,8].includes(bytes[49])) throw Error('Invalid summary columns');
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.length),
    rows = view.getUint32(32, true),
    stats = view.getUint32(36, true),
    pairs = view.getUint32(40, true),
    dict = view.getUint32(44, true),
    time = u64(view, 24);
  const encoding=shared??readEncoding(view,116), w=encoding.map(e=>e.width), start=116+(shared?0:encodingSize);
  if (!rows || time % hour || start+(2+w[0]+w[1]+w[2]+w[3]+w[4])*rows+(13+w[5]+w[6])*stats+(w[7]+w[8])*pairs+dict!==bytes.length) throw Error('Invalid column lengths');
  const boundaries = Uint32Array.from({
    length: 13
  }, (_, i) => view.getUint32(64 + i * 4, true));
  if (boundaries[0] !== 0 || boundaries[12] !== rows || boundaries.some((v, i) => v > rows || i > 0 && v < boundaries[i - 1])) throw Error('Invalid bucket boundaries');
  const labels = JSON.parse(decoder.decode(bytes.subarray(bytes.length - dict)));
  if (!Array.isArray(labels) || !labels.every(a => Array.isArray(a) && a.length === 4 && a.every(v => typeof v === 'string'))) throw Error('Invalid summary labels');
  const ref = start,
    count = ref + rows * 2,
    durationCount = count + rows * w[0],
    sum = durationCount + rows * w[1],
    min = sum + rows * w[2],
    max = min + rows * w[3],
    statBucket = max + rows * w[4],
    statLabel = statBucket + stats,
    statCount = statLabel + stats * 4,
    statErrors = statCount + stats * w[5],
    histStart = statErrors + stats * w[6],
    histEnd = histStart + stats * 4,
    histValue = histEnd + stats * 4,
    histCount = histValue + pairs * w[7];
  const definitionWidth=bytes[49], limit=definitionWidth?((1n<<BigInt(definitionWidth*8))-1n):0xffffffffffffffffn;
  let total = 0,
    durations = 0;
  for (let i = 0; i < rows; i++) {
    const n = compact(view,count+i*w[0],encoding[0]),
      d = compact(view,durationCount+i*w[1],encoding[1]);
    if (!n || d > n) throw Error('Invalid aggregate');
    const sumValue=compactBig(view,sum+i*w[2],encoding[2]);
    const minValue=compactBig(view,min+i*w[3],encoding[3]),maxValue=compactBig(view,max+i*w[4],encoding[4]);
    if(d===0) {if(sumValue!==0n||minValue!==encoding[3].base||maxValue!==encoding[4].base)throw Error('Invalid absent duration');}
    else if(minValue>maxValue||maxValue>limit||sumValue<BigInt(d)*minValue||sumValue>BigInt(d)*maxValue)throw Error('Invalid duration aggregate');
    total = safeAdd(total, n);
    durations = safeAdd(durations, d);
  }
  if (total !== u64(view, 16) - u64(view, 8)) throw Error('Invalid summary watermark');
  let previous = 0,
    statTotal = 0,
    samples = 0;
  const seen = new Set<string>();
  for (let i = 0; i < stats; i++) {
    const b = bytes[statBucket + i],
      label = view.getUint32(statLabel + i * 4, true),
      n = compact(view,statCount+i*w[5],encoding[5]),
      errors = compact(view,statErrors+i*w[6],encoding[6]),
      a = view.getUint32(histStart + i * 4, true),
      z = view.getUint32(histEnd + i * 4, true),
      key = JSON.stringify([b, labels[label]]);
    if (b >= 12 || label >= labels.length || !n || errors > n || a !== previous || z < a || z > pairs || seen.has(key)) throw Error('Invalid statistics columns');
    seen.add(key);
    previous = z;
    statTotal = safeAdd(statTotal, n);
    let part = 0;
    let prior = -1n;
    for (let j = a; j < z; j++) {
      const value = compactBig(view,histValue+j*w[7],encoding[7]),
        cnt = compact(view,histCount+j*w[8],encoding[8]);
      if (value <= prior || value>limit || !cnt) throw Error('Invalid duration histogram');
      prior = value;
      part = safeAdd(part, cnt);
    }
    ;
    if (part > n) throw Error('Invalid duration count');
    samples = safeAdd(samples, part);
  }
  if (previous !== pairs || stats > 0 && (statTotal !== total || samples !== durations)) throw Error('Statistics coverage mismatch');
  return {
    encoding,
    bytes,
    view,
    time,
    rows,
    stats,
    pairs,
    boundaries,
    labels,
    ref,
    count,
    durationCount,
    sum,
    min,
    max,
    statBucket,
    statLabel,
    statCount,
    statErrors,
    histStart,
    histEnd,
    histValue,
    histCount
  };
}
export async function decodeSummaryDay(raw: Uint8Array) {
  if (raw.length > maximum + 16) throw Error('Summary PAC exceeds limit');
  const expanded=await expandPAC(raw,0,maximum);
  if(!expanded||expanded.end!==raw.length)throw Error('Invalid summary PAC');
  const bytes=expanded.bytes,header=pacHeader(bytes,0,maximum,new Set());
  if(!header||header.end!==bytes.length||header.common.length||header.extensions.length||header.count<1||header.count>24)throw Error('Invalid summary PAC header');
  if(header.digest!==await expectedSummaryDigest())throw Error('Summary PAC definition mismatch');
  const hours: SummaryHour[] = [];
  let dayStart=-1,from=-1,to=-1,sealed:boolean|undefined,cursor=header.start;
  const view=new DataView(bytes.buffer,bytes.byteOffset,bytes.byteLength);
  for(let i=0;i<header.count;i++){
    if(cursor+37>header.end||bytes[cursor]!==0xaa||bytes[cursor+1]!==0xbc)throw Error('Invalid summary PAC member');
    const direct=cursor+2;
    const recordDay=u64(view,direct),recordFrom=u64(view,direct+8),recordTo=u64(view,direct+16),recordHour=u64(view,direct+24),recordSealed=view.getUint8(direct+32);
    if(recordSealed>1)throw Error('Invalid summary seal state');
    let at=direct+33;
    const extended=()=>{const short=view.getUint16(at,true);at+=2;if(short!==65535)return short;const medium=view.getUint32(at,true);at+=4;if(medium!==0xffffffff){if(medium<65535)throw Error('Nonminimal summary offset');return medium;}const large=view.getBigUint64(at,true);at+=8;if(large<BigInt(0xffffffff)||large>BigInt(maximum))throw Error('Invalid summary offset');return Number(large);};
    const encodingEnd=extended(),memberEnd=extended();
    if(encodingEnd!==encodingSize||memberEnd<=encodingEnd||memberEnd>header.end-at)throw Error('Invalid summary member bounds');
    if(i===0){dayStart=recordDay;from=recordFrom;to=recordTo;sealed=recordSealed===1;}
    else if(dayStart!==recordDay||from!==recordFrom||to!==recordTo||sealed!==(recordSealed===1))throw Error('Summary PAC member metadata differs');
    if(dayStart%day!==0||from<dayStart||to>dayStart+day||from>to||from%hour||to%hour||recordHour<from||recordHour>=to||Math.floor(recordHour/day)*day!==dayStart)throw Error('Invalid summary PAC coverage');
    const encodingBytes=bytes.subarray(at,at+encodingEnd),encoding=readEncoding(new DataView(encodingBytes.buffer,encodingBytes.byteOffset,encodingBytes.byteLength),0);
    const chunk=bytes.subarray(at+encodingEnd,at+memberEnd);
    if(recordSealed)unshuffleSummary(chunk,encoding);
    const col=decodeSummaryHour(chunk,encoding);
    if(col.time!==recordHour||hours.length&&hours.at(-1)!.time>=recordHour)throw Error('Misplaced summary hour');
    hours.push(col);cursor=at+memberEnd;
  }
  if(cursor!==header.end)throw Error('Trailing summary PAC data');
  const totals=[0n,0n,0n,0n,0n,0n];
  for(const c of hours){
   for(let i=0;i<c.rows;i++){
    const count=compactBig(c.view,c.count+i*c.encoding[0].width,c.encoding[0]);
    const n=compactBig(c.view,c.durationCount+i*c.encoding[1].width,c.encoding[1]);
    totals[0]+=count;
    totals[2]+=compactBig(c.view,c.sum+i*c.encoding[2].width,c.encoding[2]);
    if(n){
     const min=compactBig(c.view,c.min+i*c.encoding[3].width,c.encoding[3]),max=compactBig(c.view,c.max+i*c.encoding[4].width,c.encoding[4]);
     if(!totals[1]||min<totals[3])totals[3]=min;
     if(max>totals[4])totals[4]=max;
    }
    totals[1]+=n;
   }
   for(let i=0;i<c.stats;i++)totals[5]+=compactBig(c.view,c.statErrors+i*c.encoding[6].width,c.encoding[6]);
  }
  const rollup=totals;
  return {
    rollup,
    statisticsComplete: hours.every(c => c.stats > 0),
    day:dayStart,
    from,
    to,
    hours,
    sealed,
    bytes: bytes.length
  };
}
export type DecodedDay = Awaited<ReturnType<typeof decodeSummaryDay>>;

/** Iterate aggregate cells, never individual events. Histogram counts are weights. */
export function* summaryCells(day: DecodedDay, from: number, to: number) {
  for (const c of day.hours) {
    for (let b = 0; b < 12; b++) {
      const time = c.time + b * bucket;
      if (time < from || time + bucket > to) continue;
      let expected = 0, represented = 0;
      for (let i = c.boundaries[b]; i < c.boundaries[b + 1]; i++) expected = safeAdd(expected, compact(c.view,c.count+i*c.encoding[0].width,c.encoding[0]));
      for (let i = 0; i < c.stats; i++) {
        if (c.bytes[c.statBucket + i] !== b) continue;
        const count = compact(c.view,c.statCount+i*c.encoding[5].width,c.encoding[5]);
        represented = safeAdd(represented, count);
        const histogram: [number, number][] = [];
        const start = c.view.getUint32(c.histStart + i * 4, true), end = c.view.getUint32(c.histEnd + i * 4, true);
        for (let j = start; j < end; j++) histogram.push([
          compact(c.view,c.histValue+j*c.encoding[7].width,c.encoding[7],false),
          compact(c.view,c.histCount+j*c.encoding[8].width,c.encoding[8]),
        ]);
        yield { time, count, errors: compact(c.view,c.statErrors+i*c.encoding[6].width,c.encoding[6]),
          labels: c.labels[c.view.getUint32(c.statLabel + i * 4, true)], histogram };
      }
      if (represented !== expected) throw Error('These summaries do not contain complete chart dimensions.');
    }
  }
}
async function digest(bytes: Uint8Array) {
  return [...new Uint8Array(await crypto.subtle.digest('SHA-256', bytes.slice().buffer))].map(b => b.toString(16).padStart(2, '0')).join('');
}

// Daily completeness is independent of event/PAC completeness. Stored cache keys
// include an authorization partition and a content digest from a fresh manifest.
export class SummaryDayCache {
  private held = new Map<string, DecodedDay>();
  private loading = new Map<string, Promise<DecodedDay>>();
  private bytes = 0;
  private partition?: {token: string; digest: Promise<string>};
  async load(ref: SummaryDayReference, token: string, fetcher: typeof fetch, signal: AbortSignal, requireStorage = false): Promise<DecodedDay> {
    return (await this.loadMany([ref], token, fetcher, signal, requireStorage))[0];
  }
  async loadMany(refs: SummaryDayReference[], token: string, fetcher: typeof fetch, signal: AbortSignal, requireStorage = false, archive?:Archive): Promise<DecodedDay[]> {
    signal.throwIfAborted();
    for (const ref of refs) {
      if (!/^[a-f0-9]{64}$/.test(ref.key) || !/^[a-f0-9]{64}$/.test(ref.definition) || !/^[a-f0-9]{32}$/.test(ref.generation) || typeof ref.source !== 'string' || !Number.isSafeInteger(ref.day) || ref.day < 0 || ref.day % day || typeof ref.sealed !== 'boolean') throw Error('Invalid summary manifest');
    }
    const scope=fetchCredentialScope(fetcher,token);
    if(this.partition?.token !== scope)this.partition={token:scope,digest:digest(new TextEncoder().encode(scope))};
    const partition = await this.partition.digest;
    type Pending = {ref:SummaryDayReference; key:string; flight:string; url:string; cache?:Cache; missing?:boolean; resolve:(day:DecodedDay)=>void; reject:(error:unknown)=>void};
    const owned:Pending[]=[];
    const results=refs.map(ref=>{
      const key=partition+'/'+ref.key, held=this.held.get(key);
      if(held&&!requireStorage)return Promise.resolve(held);
      const flight=key+(requireStorage?'/persistent':'/search');
      let pending=this.loading.get(flight);
      if(!pending){
        let resolve!:Pending['resolve'],reject!:Pending['reject'];
        pending=new Promise<DecodedDay>((yes,no)=>{resolve=yes;reject=no});
        this.loading.set(flight,pending);
        void pending.finally(()=>{if(this.loading.get(flight)===pending)this.loading.delete(flight)}).catch(()=>{});
        owned.push({ref,key,flight,url:new URL('/__snoot_summary_cache/'+key,globalThis.location?.origin??'http://localhost').href,resolve,reject});
      }
      return pending;
    });
    const complete=async(item:Pending,raw:Uint8Array,cacheHit:boolean)=>{
      if(await digest(raw)!==item.ref.key)throw Error('Summary content identity mismatch');
      const decoded=await decodeSummaryDay(raw);
      if(decoded.day!==item.ref.day||decoded.sealed!==item.ref.sealed)throw Error('Summary manifest mismatch');
      if(item.cache&&!cacheHit){
        try {
          if(requireStorage)await storeSummaryPacket(item.cache,item.url,raw);
          else queueSummaryPacket(item.cache,item.url,raw);
        }catch(error){if(requireStorage)throw Error(`Unable to save summaries: ${error instanceof Error?error.message:String(error)}`)}
      }
      this.remember(item.key,decoded);item.resolve(decoded);
    };
    const run=(async()=>{
      try {
        // Bound cache I/O separately from HTTP. The network receives only misses,
        // including holes in an otherwise cached date range.
        let next=0;
        const prepares=await Promise.allSettled(Array.from({length:Math.min(16,owned.length)},async()=>{
          for(;;){
            signal.throwIfAborted();const item=owned[next++];if(!item)return;
            if(archive&&item.ref.sealed){
              const {ref}=item;
              const bytes=(await archive(token,{kind:'summary-read',query:{tap:ref.source,digest:ref.definition,generation:ref.generation,key:ref.key}})).summaryBytes;
              if(bytes){await complete(item,bytes,true);continue;}
            }
            try {if(item.ref.sealed&&typeof caches!=='undefined')item.cache=await caches.open('snoot-summary-days')}catch{}
            if(requireStorage&&item.ref.sealed&&!item.cache)throw Error('Local summary storage is unavailable.');
            let raw:Uint8Array|undefined;
            try {const stored=await item.cache?.match(item.url);if(stored)raw=new Uint8Array(await stored.arrayBuffer())}catch{}
            if(raw&&await digest(raw)!==item.ref.key){await item.cache?.delete(item.url);raw=undefined}
            if(raw)await complete(item,raw,true);else item.missing=true;
          }
        }));
        for(const result of prepares)if(result.status==='rejected')throw result.reason;
        const missing=owned.filter(item=>item.missing);
        for(let start=0;start<missing.length;start+=summaryBatchLimit){
          signal.throwIfAborted();
          const batch=missing.slice(start,start+summaryBatchLimit);
          const response=await fetcher('/summaries',{
            method:'POST',signal,cache:'no-store',
            headers:{'Content-Type':'application/json',Accept:'application/pil',...(token?{Authorization:`Bearer ${token}`}:{})},
            body:JSON.stringify({packs:batch.map(({ref:{source,definition,key}})=>({source,definition,key}))})
          });
          await readSummaryPacks(response,batch.length,maximum+32,signal,(index,raw)=>complete(batch[index],raw,false));
        }
      }catch(error){for(const item of owned)item.reject(error);throw error}
    })();
    // Observe every promise immediately, including when a late pack is corrupt.
    const settled=await Promise.allSettled([run,Promise.all(results)]);
    for(const result of settled)if(result.status==='rejected')throw result.reason;
    signal.throwIfAborted();
    const days=(settled[1] as PromiseFulfilledResult<DecodedDay[]>).value;
    days.forEach((decoded,i)=>{if(decoded.day!==refs[i].day||decoded.sealed!==refs[i].sealed)throw Error('Summary manifest mismatch')});
    return days;
  }
  private remember(key:string,decoded:DecodedDay) {
    if(decoded.bytes<=32*1024*1024){
      this.bytes-=this.held.get(key)?.bytes??0;this.held.delete(key);this.held.set(key,decoded);this.bytes+=decoded.bytes;
    }
    while(this.bytes>32*1024*1024&&this.held.size>1){
      const first=this.held.keys().next().value!;this.bytes-=this.held.get(first)!.bytes;this.held.delete(first);
    }
  }
}

export function mergeDailyAnalytics(base: ViewSummary, manifest: DailyAnalytics, days: DecodedDay[]): ViewSummary {
  if (!Number.isSafeInteger(manifest.from) || !Number.isSafeInteger(manifest.to) || manifest.from % bucket || manifest.to % bucket || manifest.from < base.from || manifest.to > base.to || manifest.from >= manifest.to || days.length !== manifest.days.length) throw Error('Invalid analytics window');
  const result: ViewSummary = {
    ...base,
    counts: [...base.counts],
    errors: [...base.errors]
  };
  const stats: ViewStatistics = base.statistics ? structuredClone(base.statistics) : {
    complete: true,
    matched: 0,
    evaluated: 0,
    services: [],
    levels: [],
    duration: {
      count: 0,
      average: 0,
      min: 0,
      max: 0
    },
    operations: {
      errors: [],
      latency: []
    }
  };
  const services = new Map(stats.services.map(v => [v.name, v.count])),
    levels = new Map(stats.levels.map(v => [v.name, v.count]));
  const operations = new Map<string, {
    labels: string[];
    errors: number;
    hist: Map<number, number>;
  }>();
  // Border distributions seed the operation histograms. Their scalar totals
  // already exist in base.statistics, so they must not be counted twice.
  for (const g of manifest.border ?? []) {
    if (!Array.isArray(g.labels) || g.labels.length !== 4 || !g.labels.every(s => typeof s === 'string') || !Number.isSafeInteger(g.count) || g.count < 0 || !Number.isSafeInteger(g.errors) || g.errors < 0 || g.errors > g.count) throw Error('Invalid border statistics');
    const k = JSON.stringify(g.labels.slice(0, 3));
    let op = operations.get(k);
    if (!op) {
      op = {
        labels: g.labels,
        errors: 0,
        hist: new Map()
      };
      operations.set(k, op);
    }
    op.errors = safeAdd(op.errors, g.errors);
    for (const [value, count] of g.durations ?? []) {
      if (!Number.isFinite(value) || value < 0 || !Number.isSafeInteger(count) || count <= 0) throw Error('Invalid border histogram');
      op.hist.set(value, safeAdd(op.hist.get(value) ?? 0, count));
    }
  }
  let added = 0,
    evaluated = 0;
  const unique = new Set<string>();
  days.forEach((d, index) => {
    const ref = manifest.days[index],
      identity = JSON.stringify([ref.source, ref.definition, ref.generation, ref.day]);
    if (unique.has(identity)) throw Error('Duplicate summary day');
    unique.add(identity);
    for (const c of d.hours) {
      const v = c.view;
      for (let b = 0; b < 12; b++) {
        const time = c.time + b * bucket;
        if (time < manifest.from || time + bucket > manifest.to) continue;
        const bin = Math.floor((time - result.from) / result.step);
        if (bin < 0 || bin >= result.counts.length) throw Error('Summary bin outside view');
        let count = 0;
        for (let i = c.boundaries[b]; i < c.boundaries[b + 1]; i++) count = safeAdd(count, compact(v,c.count+i*c.encoding[0].width,c.encoding[0]));
        result.counts[bin] = safeAdd(result.counts[bin], count);
        added = safeAdd(added, count);
        if (ref.source.endsWith('/error')) result.errors[bin] = safeAdd(result.errors[bin], count);
      }
      for (let i = 0; i < c.stats; i++) {
        const time = c.time + c.bytes[c.statBucket + i] * bucket;
        if (time < manifest.from || time + bucket > manifest.to) continue;
        const labels = c.labels[v.getUint32(c.statLabel + i * 4, true)],
          n = compact(v,c.statCount+i*c.encoding[5].width,c.encoding[5]),
          errors = compact(v,c.statErrors+i*c.encoding[6].width,c.encoding[6]);
        evaluated = safeAdd(evaluated, n);
        services.set(labels[0], safeAdd(services.get(labels[0]) ?? 0, n));
        levels.set(labels[3], safeAdd(levels.get(labels[3]) ?? 0, n));
        const k = JSON.stringify(labels.slice(0, 3));
        let op = operations.get(k);
        if (!op) {
          op = {
            labels,
            errors: 0,
            hist: new Map()
          };
          operations.set(k, op);
        }
        ;
        op.errors = safeAdd(op.errors, errors);
        const a = v.getUint32(c.histStart + i * 4, true),
          z = v.getUint32(c.histEnd + i * 4, true);
        for (let j = a; j < z; j++) {
          const value = compact(v,c.histValue+j*c.encoding[7].width,c.encoding[7], false),
            n = compact(v,c.histCount+j*c.encoding[8].width,c.encoding[8]);
          op.hist.set(value, safeAdd(op.hist.get(value) ?? 0, n));
          const dur = stats.duration;
          if (!dur.count || value < dur.min) dur.min = value;
          if (value > dur.max) dur.max = value;
          dur.count = safeAdd(dur.count, n);
          dur.average += (value - dur.average) * n / dur.count;
        }
      }
    }
  });
  result.count = safeAdd(result.count, added);
  stats.matched = result.count;
  stats.evaluated = safeAdd(stats.evaluated ?? base.count, evaluated);
  stats.complete = stats.evaluated === stats.matched;
  const counts = (m: Map<string, number>) => [...m].map(([name, count]) => ({
    name,
    count
  })).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  stats.services = counts(services);
  stats.levels = counts(levels);
  const errors: ViewStatistics['operations']['errors'] = [],
    latency: ViewStatistics['operations']['latency'] = [];
  for (const op of operations.values()) {
    const [service, resource, operation] = op.labels;
    if (op.errors) errors.push({
      service,
      resource,
      operation,
      value: op.errors,
      samples: op.errors
    });
    let n = 0;
    for (const count of op.hist.values()) n = safeAdd(n, count);
    if (n) {
      const rank = n - Math.floor(n / 20);
      let seen = 0;
      for (const [value, count] of [...op.hist].sort((a, b) => a[0] - b[0])) {
        seen += count;
        if (seen >= rank) {
          latency.push({
            service,
            resource,
            operation,
            value,
            samples: n
          });
          break;
        }
      }
    }
  }
  const top = (a: typeof errors) => a.sort((a, b) => b.value - a.value || a.service.localeCompare(b.service) || a.resource.localeCompare(b.resource) || a.operation.localeCompare(b.operation)).slice(0, 10);
  stats.operations = {
    errors: top(errors),
    latency: top(latency)
  };
  if (base.statistics) result.statistics = stats;
  return result;
}
