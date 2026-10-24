import type { ViewSummary, ViewStatistics } from './view.ts';
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
const crcTable = Uint32Array.from({
  length: 256
}, (_, i) => {
  let c = i;
  for (let j = 0; j < 8; j++) c = c & 1 ? 0x82f63b78 ^ c >>> 1 : c >>> 1;
  return c >>> 0;
});
function crc32c(bytes: Uint8Array) {
  let c = 0xffffffff;
  for (const b of bytes) c = crcTable[(c ^ b) & 255] ^ c >>> 8;
  return (c ^ 0xffffffff) >>> 0;
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
type NumericEncoding = { width: number; base: bigint };
const encodingSize = 81, dayDirectory = 433;
function readEncoding(v: DataView, at: number): NumericEncoding[] {
  return Array.from({length: 9}, (_, i) => {
    const width = v.getUint8(at + i * 9), base = v.getBigUint64(at + i * 9 + 1, true);
    if (width < 1 || width > 8 || ![3, 4, 7].includes(i) && base !== 0n || i === 2 && width !== 8)
      throw Error('Invalid summary column encoding');
    return {width, base};
  });
}
function compactSmall(v: DataView, at: number, width: number) {
  switch (width) {
    case 1: return v.getUint8(at);
    case 2: return v.getUint16(at, true);
    case 3: return v.getUint16(at, true) + v.getUint8(at + 2) * 65536;
    case 4: return v.getUint32(at, true);
  }
  let n = 0;
  for (let i = width - 1; i >= 0; i--) n = n * 256 + v.getUint8(at + i);
  return n;
}
function compactBig(v: DataView, at: number, e: NumericEncoding) {
  let n: bigint;
  if (e.width <= 6) n = BigInt(compactSmall(v, at, e.width));
  else if (e.width === 8) n = v.getBigUint64(at, true);
  else n = BigInt(v.getUint32(at, true)) + BigInt(v.getUint16(at + 4, true)) * 4294967296n
    + BigInt(v.getUint8(at + 6)) * 281474976710656n;
  n += e.base;
  if (n > 0xffffffffffffffffn) throw Error('Summary base overflow');
  return n;
}
function compact(v: DataView, at: number, e: NumericEncoding, exact = true) {
  // Common columns avoid BigInt arithmetic on each row.
  const n = e.width <= 6 && e.base <= BigInt(Number.MAX_SAFE_INTEGER)
    ? compactSmall(v, at, e.width) + Number(e.base)
    : Number(compactBig(v, at, e));
  if (exact && !Number.isSafeInteger(n)) throw Error('Summary integer exceeds safe range');
  return n;
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
  let at = 116 + n * 2, scratch = new Uint8Array(0);
  const column = (count: number, width: number) => {
    const length = count * width;
    if (count > 1 && width > 1) {
      if (scratch.length < length) scratch = new Uint8Array(length);
      scratch.set(bytes.subarray(at, at + length));
      for (let b = 0; b < width; b++)
        for (let i = 0; i < count; i++) bytes[at + i * width + b] = scratch[b * count + i];
    }
    at += length;
  };
  for (let k = 0; k < 5; k++) column(n, w[k]);
  at += m * 5;
  column(m, w[5]); column(m, w[6]);
  at += m * 8;
  column(p, w[7]); column(p, w[8]);
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
  if (raw.length < 32 || raw.length > maximum + 32 || !magic(raw, 'SUMDAY00')) throw Error('Invalid daily summary');
  const v = new DataView(raw.buffer, raw.byteOffset, raw.length),
    length = v.getUint32(16, true),
    time = u64(v, 24);
  if (v.getUint32(12, true) !== raw.length || length < dayDirectory || length > maximum || raw[8] > 1 || raw[9] > 1 || raw[10]>1 || raw[11] || time % day) throw Error('Invalid daily summary header');
  let bytes = raw.subarray(32);
  if (raw[8] === 1) {
    const reader = new Blob([bytes.slice().buffer]).stream().pipeThrough(new DecompressionStream('gzip')).getReader();
    bytes = new Uint8Array(length);
    let at = 0;
    try {
      for (;;) {
        const {
          done,
          value
        } = await reader.read();
        if (done) break;
        if (at + value.length > length) throw Error('Expanded summary exceeds limit');
        bytes.set(value, at);
        at += value.length;
      }
      if (at !== length) throw Error('Short expanded summary');
    } catch (e) {
      await reader.cancel().catch(() => {});
      throw e;
    } finally {
      reader.releaseLock();
    }
  }
  if (bytes.length !== length || crc32c(bytes) !== v.getUint32(20, true)) throw Error('Summary checksum mismatch');
  if(raw[10]&&raw[8]===0)bytes=bytes.slice();
  const d = new DataView(bytes.buffer, bytes.byteOffset, bytes.length),
    from = u64(d, 0),
    to = u64(d, 8);
  if (from < time || to > time + day || from > to || from % hour || to % hour) throw Error('Invalid day coverage');
  const hours: SummaryHour[] = [];
  const encoding=readEncoding(d,352);
  const rollup=Array.from({length:6},(_,i)=>d.getBigUint64(304+i*8,true));
  let cursor = dayDirectory;
  for (let h = 0; h < 24; h++) {
    const offset = d.getUint32(16 + h * 12, true),
      n = d.getUint32(20 + h * 12, true),
      state = d.getUint32(24 + h * 12, true),
      known = time + h * hour >= from && time + h * hour < to;
    if (state !== (known ? 1 : 0) || !known && n || !n && offset) throw Error('Invalid hour state');
    if (!n) continue;
    if (offset !== cursor || n > bytes.length - offset) throw Error('Invalid hour range');
    const chunk=bytes.subarray(offset,offset+n);
    if(raw[10])unshuffleSummary(chunk,encoding);
    const col = decodeSummaryHour(chunk,encoding);
    if (col.time !== time + h * hour) throw Error('Misplaced summary hour');
    hours.push(col);
    cursor += n;
  }
  if (cursor !== bytes.length) throw Error('Trailing daily data');
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
  if(totals.some((v,i)=>v!==rollup[i]))throw Error('Invalid daily rollup');
  return {
    rollup,
    statisticsComplete: hours.every(c => c.stats > 0),
    day: time,
    from,
    to,
    hours,
    sealed: raw[9] === 1,
    bytes: bytes.length
  };
}
export type DecodedDay = Awaited<ReturnType<typeof decodeSummaryDay>>;
async function digest(bytes: Uint8Array) {
  return [...new Uint8Array(await crypto.subtle.digest('SHA-256', bytes.slice().buffer))].map(b => b.toString(16).padStart(2, '0')).join('');
}

// Daily completeness is independent of event/PAC completeness. Stored cache keys
// include an authorization partition and a content digest from a fresh manifest.
export class SummaryDayCache {
  private held = new Map<string, DecodedDay>();
  private loading = new Map<string, Promise<DecodedDay>>();
  private bytes = 0;
  async load(ref: SummaryDayReference, token: string, fetcher: typeof fetch, signal: AbortSignal): Promise<DecodedDay> {
    if (!/^[a-f0-9]{64}$/.test(ref.key) || !/^[a-f0-9]{64}$/.test(ref.definition) || !/^[a-f0-9]{32}$/.test(ref.generation) || typeof ref.source !== 'string' || !Number.isSafeInteger(ref.day) || ref.day < 0 || ref.day % day || typeof ref.sealed !== 'boolean') throw Error('Invalid summary manifest');
    const partition = await digest(new TextEncoder().encode(token)),
      key = partition + '/' + ref.key,
      existing = this.held.get(key);
    if (existing) {
      if (existing.day !== ref.day || existing.sealed !== ref.sealed) throw Error('Summary manifest mismatch');
      return existing;
    }
    let pending = this.loading.get(key);
    if (!pending) {
      pending = this.fetchDay(ref, key, fetcher, signal, token);
      this.loading.set(key, pending);
      void pending.finally(() => this.loading.delete(key)).catch(() => {});
    }
    const decoded = await pending;
    signal.throwIfAborted();
    if (decoded.day !== ref.day || decoded.sealed !== ref.sealed) throw Error('Summary manifest mismatch');
    return decoded;
  }
  private async fetchDay(ref: SummaryDayReference, key: string, fetcher: typeof fetch, signal: AbortSignal, token: string): Promise<DecodedDay> {
    const cacheURL = new URL('/__snoot_summary_cache/' + key, globalThis.location?.origin ?? 'http://localhost').href;
    let cache: Cache | undefined;
    try {
      if (ref.sealed && typeof caches !== 'undefined') cache = await caches.open('snoot-summary-days');
    } catch {}
    let raw: Uint8Array | undefined;
    try {
      const stored = await cache?.match(cacheURL);
      if (stored) raw = new Uint8Array(await stored.arrayBuffer());
    } catch {}
    if (raw && (await digest(raw)) !== ref.key) {
      await cache?.delete(cacheURL);
      raw = undefined;
    }
    if (!raw) {
      const q = new URLSearchParams({
        source: ref.source,
        definition: ref.definition,
        key: ref.key
      });
      const response = await fetcher('/summary-day?' + q, {
        signal,
        cache: 'no-store',
        headers: token ? {
          Authorization: `Bearer ${token}`
        } : {}
      });
      if (!response.ok) throw Error(response.status === 409 ? 'Summary snapshot expired; repeat search' : `Summary retrieval failed (${response.status})`);
      const parts: Uint8Array[] = [];
      let length = 0;
      const reader = response.body?.getReader();
      if (!reader) throw Error('Missing summary body');
      try {
        for (;;) {
          const {
            done,
            value
          } = await reader.read();
          if (done) break;
          length += value.length;
          if (length > maximum + 32) throw Error('Summary exceeds limit');
          parts.push(value);
        }
      } catch (e) {
        await reader.cancel().catch(() => {});
        throw e;
      } finally {
        reader.releaseLock();
      }
      ;
      raw = new Uint8Array(length);
      let at = 0;
      for (const part of parts) {
        raw.set(part, at);
        at += part.length;
      }
      if ((await digest(raw)) !== ref.key) throw Error('Summary content identity mismatch');
    }
    const decoded = await decodeSummaryDay(raw);
    if (decoded.day !== ref.day || decoded.sealed !== ref.sealed) throw Error('Summary manifest mismatch');
    if (cache) {
      try {
        await cache.put(cacheURL, new Response(raw.slice().buffer));
        const keys = await cache.keys();
        for (const request of keys.slice(0, Math.max(0, keys.length - 128))) await cache.delete(request);
      } catch {}
    }
    if (decoded.bytes <= 32 * 1024 * 1024) {
      this.held.set(key, decoded);
      this.bytes += decoded.bytes;
    }
    while (this.bytes > 32 * 1024 * 1024 && this.held.size > 1) {
      const first = this.held.keys().next().value!;
      this.bytes -= this.held.get(first)!.bytes;
      this.held.delete(first);
    }
    ;
    return decoded;
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
