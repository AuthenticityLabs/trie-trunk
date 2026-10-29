import {decodeSummaryDay} from "../pil/summaries.ts";
import { decodeObject } from "../pil/control.ts";
import { NativeCacheError } from "./errors.ts";
import { mergeTimeWindow, missingHours, timeLeaf, retireTime, HOUR, TIME_INDEX, TIME_STATE, TIME_SODS, TIME_SUM, markTimeFiles, type ClientTimeTrie } from "./time-trie.ts";
import { indexMillis, INDEX_ROW_BYTES } from '../pil/pac-native.ts';
import { pacHeader } from "../pil/pac.ts";
import { pacPlacement, pacLocalRows, nativePAC, type PACPlacement } from "../pil/pac-native.ts";
import { resourceBinding, type PILDefinition } from "../pil/layout.ts";
import { matchesSource,validSource } from "../source.ts";
import { objectParameters, objectIdentityBytes, type ObjectQuery } from "../replay.ts";
import type { Metadata, Binding } from "../pil/metadata.ts";
import type { NativeRecord } from "../pil/stream.ts";
import type { FileIO } from "./wasm.ts";
import { readFile, write, recover } from "./files.ts";

export type Span = { Start: number; End: number };
type Extent = Span & { local: number };
type Blob = { start: number; count: number; local: number; length: number; digest: string };
type StatePage = Blob & { first?: string; last?: string };
type StateSnapshot = { epoch:string; sequence:number; collection:number; pages:StatePage[] };
type File = { retired?:number; free?:number[]; pages?: Record<string,number>; end: number; extents: Extent[]; blobs: Blob[] };
type Kind = "index" | "sods" | "trie" | "map" | "state" | "sum";
export type NativeControl = { serial:number; scopes:Record<string,number> };
export type NativeManifest = {
  IndexOnly?:boolean;
  TimeTrie?:Uint8Array;TimeRetired?:boolean;TimeRetainedFrom?:number;
  RetiredRows?:number;RetiredMembers?:number;RetiredPods?:number;
  Definition: Uint8Array; Generation: Uint8Array; Branch: number;
  From: number; To: number; Tail: number; Checkpoint: number; RowWidth: number;
  TrieNodes: number; Index: Span[]; MemberRanges: Span[];
  Provisional: boolean; IndexPeriod: string; Timezone: string;
  StateEpoch?: Uint8Array; StateSequence?: number;
  GenerationSequence?: number;
};
export type NativeSource = {
  timeTrie?:ClientTimeTrie;
  key: string; tap: string; digest: string; generation: string;
  files: Record<Exclude<Kind,"sum">, File> & {sum?:File};
  trieNodes: number;
  retiredRows?:number;retiredMembers?:number;
  contexts?: {placement:PACPlacement;common:Uint8Array}[];
  state?: StateSnapshot;
  stateFileRevision?: number;
  generationSequence?: number;
  retiredBy?: string;
  reclaimed?: boolean;
};
// Data files are flushed before publishing branch leaves through the file-store commit root.
export interface NativeBackend {
  get(key: string): Promise<NativeSource | undefined>;
  list(): Promise<NativeSource[]>;
  put(sources: NativeSource[], control?:NativeControl): Promise<void>;
  control():Promise<NativeControl>;
  file(key: string, kind: Kind, revision?:number): Promise<FileIO>;
  removeFiles(key: string): Promise<void>;
  removeOldStateFiles(key:string,keepRevision:number):Promise<void>;
}
export const hex = (b: Uint8Array) => [...b].map(v => v.toString(16).padStart(2,"0")).join("");
const hash = async (b: Uint8Array) => hex(new Uint8Array(await crypto.subtle.digest("SHA-256", b.slice().buffer)));
const integer = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
export function normalize(spans: Span[]): Span[] {
  const out: Span[] = [];
  for (const r of [...spans].sort((a,b) => a.Start-b.Start)) {
    if (!integer(r.Start) || !integer(r.End) || r.End <= r.Start) throw Error("Invalid native span");
    const last = out.at(-1);
    if (last && r.Start <= last.End) last.End = Math.max(last.End,r.End);
    else out.push({...r});
  }
  return out;
}
export function missing(want: Span[], have: Span[]): Span[] {
  const known = normalize(have), result: Span[] = [];
  let i = 0;
  for (const w of normalize(want)) {
    let at = w.Start;
    while (i < known.length && known[i].End <= at) i++;
    for (let j=i;j<known.length && known[j].Start < w.End;j++) {
      const h=known[j];
      if (h.Start>at) result.push({Start:at,End:h.Start});
      at=Math.max(at,h.End);
      if(at>=w.End)break;
    }
    if(at<w.End)result.push({Start:at,End:w.End});
  }
  return result;
}
export function resolve(extents: Extent[], wanted: Span): Extent[] {
  normalize([wanted]);
  const sorted=[...extents].sort((a,b)=>a.Start-b.Start), out:Extent[]=[];
  let at=wanted.Start, previous=0;
  for(const e of sorted){
    if(!integer(e.local)||e.Start<previous||e.End<=e.Start)throw Error("Invalid native extents");
    previous=e.End;
    if(e.End<=at||e.Start>=wanted.End)continue;
    if(e.Start>at)throw Error("Native source gap");
    const end=Math.min(e.End,wanted.End);
    out.push({Start:at,End:end,local:e.local+at-e.Start});at=end;
  }
  if(at!==wanted.End)throw Error("Native source gap");
  return out;
}
function manifest(raw: Record<string, unknown>, binding: Binding): NativeManifest {
  const stamp=(v:unknown)=>typeof v==='string'?Date.parse(v)/1000:v;
  const m={...raw,From:stamp(raw.From),To:stamp(raw.To)} as unknown as NativeManifest;
  if(!(m.Definition instanceof Uint8Array)||hex(m.Definition)!==binding.digest||
    !(m.Generation instanceof Uint8Array)||m.Generation.length!==16||m.RowWidth!==INDEX_ROW_BYTES||
    !integer(m.Branch)||m.Branch>4||!integer(m.Tail)||m.Tail>Number.MAX_SAFE_INTEGER/INDEX_ROW_BYTES||
    !integer(m.Checkpoint)||!integer(m.TrieNodes)||m.TrieNodes<1||m.TrieNodes>65536||
    !integer(m.From)||!integer(m.To)||m.From>=m.To||
    typeof m.Provisional!=="boolean"||typeof m.IndexPeriod!=="string"||typeof m.Timezone!=="string")
    throw Error("Invalid native manifest");
  if(m.IndexOnly!==undefined&&typeof m.IndexOnly!=="boolean")throw Error("Invalid index collection mode");
  if(m.IndexOnly&&((m.MemberRanges??[]).length||!m.Provisional&&!m.TimeTrie))throw Error("Invalid index-only manifest");
  if(m.GenerationSequence!==undefined&&(!integer(m.GenerationSequence)||m.GenerationSequence===0))throw Error("Invalid native generation sequence");
  m.Index=normalize(m.Index??[]);m.MemberRanges=normalize(m.MemberRanges??[]);
  if(m.Index.some(r=>r.End>m.Tail))
    throw Error("Invalid native index boundary");
  const rows=m.RetiredRows??0,members=m.RetiredMembers??0;
  if(!integer(rows)||rows>m.Tail||!integer(members)||!integer(m.RetiredPods??0)||
    m.Index.some(r=>r.Start<rows)||m.MemberRanges.some(r=>r.Start<members))throw Error("Invalid PAC retirement boundary");
  // PIL byte strings are views into the received buffer. Structured cloning a
  // view copies its entire backing buffer, not just its visible bytes. Persist
  // owned digest/identity bytes so each hourly manifest cannot retain a whole
  // network response (hundreds of MB across a month of small packages).
  return structuredClone({...m,Definition:m.Definition.slice(),Generation:m.Generation.slice(),
    ...(m.StateEpoch instanceof Uint8Array?{StateEpoch:m.StateEpoch.slice()}:{}),
    ...(m.TimeTrie instanceof Uint8Array?{TimeTrie:m.TimeTrie.slice()}:{})});
}
function empty(key:string,b:Pick<Binding,'tap'|'digest'>,m:Pick<NativeManifest,'Generation'>):NativeSource {
  const file=():File=>({end:0,extents:[],blobs:[]});
  return {key,tap:b.tap,digest:b.digest,generation:hex(m.Generation),
    files:{index:file(),sods:file(),trie:file(),map:file(),state:file()},
    trieNodes:1};
}
type Package = { indexHours?:Map<number,{time:number;count:number}>; rows?:{start:number;bytes:Uint8Array}[]; key:string; binding:Binding; manifest:NativeManifest; state?:StateSnapshot; stateFileRevision?:number; stateComplete:boolean; stateCount:number; stateLast?:string; closed:boolean };
export type NativeSession = { packages: Package[]; active: Map<number,Package>; finished:boolean; serial:number };
export type NativePage = { ordinals?:Float64Array; bases: Float64Array; common?:(Uint8Array|undefined)[]; rows: Uint8Array; payload: Uint8Array; offsets: Uint32Array; after?: number; done: boolean };

export type SummaryQuery={tap:string;digest:string;generation:string;key?:string;day?:number};
export type SummaryImport=SummaryQuery & {bytes:Uint8Array;serial:number};
export type TimeQuery={tap:string;digest:string;from:number;to:number};
export type TimeReceipt={serial:number;generation?:string;sequence?:number;rows:{time:number;count:number;flags:number}[];missing:{from:number;to:number}[]};
export type TimeImport={tap:string;definition:Metadata;generation:string;sequence:number;serial:number;bytes:Uint8Array};

export class NativeStore {
  private backend: NativeBackend;
  private retiredHeaders=new WeakMap<FileIO,number>();
  constructor(backend: NativeBackend) { this.backend=backend; }
  async resolveHour(q:{tap:string;digest:string;generation:string;from:number;to:number}){
    const control=await this.backend.control(),salt=control.scopes[q.tap.split('/').slice(0,3).join('/')]??0;
    const key=await hash(new TextEncoder().encode(JSON.stringify([q.tap,q.digest,q.generation,...(salt?[salt]:[])])));
    const source=await this.backend.get(key);
    if(!source||source.retiredBy||missingHours(source.timeTrie,q.from,q.to).length)return;
    return this.dependencies(key);
  }
  async timeWindow(q:TimeQuery):Promise<TimeReceipt>{
    const serial=await this.serial();
    const sources=(await this.backend.list()).filter(s=>!s.retiredBy&&s.tap===q.tap&&s.digest===q.digest&&s.timeTrie);
    sources.sort((a,b)=>(b.generationSequence??0)-(a.generationSequence??0));
    const source=sources[0],tree=source?.timeTrie;
    if(source&&sources.some(s=>s.key!==source.key&&s.generationSequence===source.generationSequence))throw Error('Conflicting time trie generations');
    let from=q.from;
    if(tree)from=Math.max(from,Number(new DataView(tree.bytes.buffer,tree.bytes.byteOffset).getBigUint64(8,true)));
    const gaps=from<q.to?missingHours(tree,from,q.to,0):[];
    const rows:TimeReceipt['rows']=[];
    for(let at=Math.floor(q.from/HOUR)*HOUR;at<q.to;at+=HOUR){const item=timeLeaf(tree,at);if(item)rows.push({time:at,count:item.count,flags:item.flags})}
    return {serial,generation:source?.generation,sequence:source?.generationSequence,rows,missing:gaps};
  }
  private async summarySource(q:SummaryQuery){
    return (await this.backend.list()).find(s=>!s.retiredBy&&s.tap===q.tap&&s.digest===q.digest&&s.generation===q.generation);
  }
  async summaryBytes(q:SummaryQuery):Promise<Uint8Array|undefined>{
    const s=await this.summarySource(q);if(!s)return;
    const blob=s.files.sum?.blobs.find(b=>q.key?b.digest===q.key:b.start===q.day);
    if(!blob)return;
    const file=await this.backend.file(s.key,'sum');
    try{const bytes=readFile(file,blob.local,blob.length);if(await hash(bytes)!==blob.digest)throw Error('Stored summary checksum mismatch');return bytes;}finally{file.close()}
  }
  async importSummary(input:SummaryImport){
    if(await this.serial()!==input.serial)throw new NativeCacheError('Native cache rebased; retry summary collection',{code:'native-cache-race',reason:'Native cache rebased'});
    const s=await this.summarySource(input);if(!s?.timeTrie)throw Error('Summary requires a matching time trie');
    const decoded=await decodeSummaryDay(input.bytes),digest=await hash(input.bytes);
    if(!decoded.sealed||input.key&&digest!==input.key||input.day!==undefined&&decoded.day!==input.day)throw Error('Invalid sealed summary binding');
    const counts=new Map(decoded.hours.map(h=>[h.time,Number(h.view.getBigUint64(16,true)-h.view.getBigUint64(8,true))]));
    const ranges:{from:number;to:number}[]=[];
    for(let at=decoded.from;at<decoded.to;at+=HOUR){
      const leaf=timeLeaf(s.timeTrie,at);if(!leaf)continue;
      if(leaf.count!==(counts.get(at)??0))throw Error('Summary and time trie counts disagree');
      const last=ranges.at(-1);if(last?.to===at)last.to+=HOUR;else ranges.push({from:at,to:at+HOUR});
    }
    const existing=s.files.sum?.blobs.find(b=>b.start===decoded.day);
    if(existing&&existing.digest!==digest)throw Error('Immutable summary changed in the same generation');
    const f=s.files.sum??={end:0,extents:[],blobs:[]},handle=await this.backend.file(s.key,'sum');
    try{
      if(!existing){recover(handle,f.end);write(handle,input.bytes,f.end);handle.flush();f.blobs.push({start:decoded.day,count:1,digest,local:f.end,length:input.bytes.length});f.end+=input.bytes.length;}
      // The transaction flushes bytes before publishing these receipt bits.
      for(const r of ranges)s.timeTrie=markTimeFiles(s.timeTrie!,Uint8Array.from(unhex(s.generation)),r.from,r.to,TIME_SUM);
      await this.backend.put([s]);
    }finally{handle.close()}
  }
  async importTime(input:TimeImport){
    const {tap,definition,generation,sequence,serial,bytes}=input;
    if(!/^[a-f0-9]{32}$/.test(generation)||!integer(sequence)||!sequence||!validSource(tap))throw Error('Invalid time trie identity');
    const control=await this.backend.control();
    if(control.serial!==serial)throw new NativeCacheError('Native cache rebased; retry time collection',{code:'native-cache-race',reason:'Native cache rebased'});
    const salt=control.scopes[tap.split('/').slice(0,3).join('/')]??0;
    const key=await hash(new TextEncoder().encode(JSON.stringify([tap,definition.digest,generation,...(salt?[salt]:[])])));
    const known=await this.backend.list();
    for(const other of known.filter(s=>!s.retiredBy&&s.tap===tap&&s.digest===definition.digest)){
      if((other.generationSequence??0)>sequence||other.generationSequence===sequence&&other.generation!==generation)throw new NativeCacheError('Stale or conflicting time trie generation',{code:'native-cache-conflict',source:tap,reason:'generation-fork'});
    }
    const s=await this.backend.get(key)??empty(key,{tap,digest:definition.digest},{Generation:Uint8Array.from(unhex(generation))});
    if(s.retiredBy||s.generationSequence!==undefined&&s.generationSequence!==sequence)throw Error('Stale time trie generation');
    const tree=mergeTimeWindow(s.timeTrie,bytes,Uint8Array.from(unhex(generation)),0,true);
    if(!s.files.map.end){
      if(definition.digest!==await hash(definition.store)||definition.pil.length!==definition.store.length||definition.pil.some((b,i)=>b!==definition.store[i]))throw Error('Invalid time trie definition');
      await this.blob(s,'map',0,1,definition.digest,definition.store);
    }
    s.timeTrie=tree;s.generationSequence=sequence;
    await this.backend.put([s]);
  }
  async serial():Promise<number>{return (await this.backend.control()).serial;}
  begin(serial=0):NativeSession {return {packages:[],active:new Map(),finished:false,serial};}
  async record(session:NativeSession,record:NativeRecord,definition?:Metadata){
    if(session.finished)throw Error("Native response already finished");
    const control=await this.backend.control();
    if(control.serial!==session.serial)throw new NativeCacheError("Native cache rebased; retry collection",{code:"native-cache-race",reason:"Native cache rebased"});
    const b=record.binding;
    if(record.kind==="manifest"){
      if(session.active.has(b.stream))throw Error("Overlapping native package");
      const m=manifest(record.manifest,b);
      if(m.Provisional&&(m.Index.length||m.MemberRanges.length))throw Error("Partial hour cannot be persisted");
      const salt=control.scopes[b.tap.split("/").slice(0,3).join("/")]??0;
      const key=await hash(new TextEncoder().encode(JSON.stringify([b.tap,b.digest,hex(m.Generation),...(salt?[salt]:[])])));
      const s=await this.backend.get(key)??empty(key,b,m);
      if(!integer(m.RetiredRows??0)||!integer(m.RetiredMembers??0)||(m.RetiredRows??0)<(s.retiredRows??0)||(m.RetiredMembers??0)<(s.retiredMembers??0))throw Error("Stale PAC retirement boundary");
      if(s.retiredBy)throw new NativeCacheError("Native generation retired; retry collection",{code:"native-cache-race",reason:"Native generation retired"});
      if(!s.files.map.end){
        if(!definition||definition.digest!==b.digest||await hash(definition.store)!==b.digest||
          definition.store.length!==definition.pil.length||definition.store.some((v,i)=>v!==definition.pil[i]))
          throw Error("Native map is missing or inconsistent");
        await this.blob(s,"map",0,1,b.digest,definition.store);
      }
      const p:Package={key,binding:b,manifest:m,stateComplete:false,stateCount:0,closed:false};
      if(m.IndexOnly&&m.TimeTrie){
        const tree=mergeTimeWindow(undefined,m.TimeTrie,m.Generation);
        p.indexHours=new Map();
        for(const span of tree.ranges)for(let at=span.from;at<span.to;at+=HOUR){const leaf=timeLeaf(tree,at)!;if(leaf.count)p.indexHours.set(leaf.start+1,{time:at,count:leaf.count});}
      }
      session.packages.push(p);session.active.set(b.stream,p);return;
    }
    const p=session.active.get(b.stream);
    if(!p||b.tap!==p.binding.tap||b.digest!==p.binding.digest)throw Error("Native control outside package");
    if(record.kind==="package-end"){
      if(p.rows?.length)throw Error("Incomplete payload record");
      if(!p.stateComplete)throw Error("Missing native invalidation state");
      p.closed=true;session.active.delete(b.stream);return;
    }
    const s=await this.backend.get(p.key);
    if(!s)throw Error("Native source disappeared");
    if((p.manifest.RetiredRows??0)<(s.retiredRows??0)||(p.manifest.RetiredMembers??0)<(s.retiredMembers??0))throw Error("Stale PAC retirement boundary");
    if(s.retiredBy)throw new NativeCacheError("Native generation retired; retry collection",{code:"native-cache-race",reason:"Native generation retired"});
    if(record.kind==="pac"){
      if(p.manifest.IndexOnly)throw Error("Payload in index-only collection");
      await this.pac(s,p,record.bytes,record.bounds,record.integrityVerified===true);return;
    }
    const {file,start,bytes,digest}=record;
    if(!integer(start)||!bytes.length||(!record.integrityVerified&&await hash(bytes)!==digest))throw Error("Invalid native chunk");
    if(file==="index"){
      const unit=INDEX_ROW_BYTES;
      if(bytes.length%unit)throw Error("Partial native row");
      const end=start+bytes.length/unit;
      if(!integer(end)||missing([{Start:start,End:end}],p.manifest.Index).length)
        throw Error("Native chunk outside requested ranges");
      if(p.manifest.IndexOnly){
        const leaf=p.indexHours?.get(start),hour=leaf?.time;
        if(hour===undefined||leaf!.count!==bytes.length/INDEX_ROW_BYTES)throw Error("Index chunk must contain a complete hour");
        const segment=new Uint8Array(INDEX_ROW_BYTES+bytes.length),v=new DataView(segment.buffer);
        v.setUint32(0,Math.floor(hour/2**32),true);
        segment.set(bytes,INDEX_ROW_BYTES);await this.range(s,"index",(start-1)*INDEX_ROW_BYTES,segment);
      }else (p.rows??=[]).push({start,bytes:bytes.slice()});
    }else if(file==="trie"){
      const nodes=decodeObject(bytes);
      if(!Array.isArray(nodes)||start<1||start+nodes.length>p.manifest.TrieNodes)throw Error("Invalid native trie prefix");
      for(let i=0;i<nodes.length;i++){
        const n=nodes[i] as Record<string,unknown>;
        if(!n||!integer(n["0"])||n["0"]>=start+i||!integer(n["1"])||!(n["2"] instanceof Uint8Array))
          throw Error("Invalid native trie node");
      }
      await this.trie(s,start,nodes,bytes,digest);
    }else if(file==="map"){
      if(start!==0||digest!==s.digest)throw Error("Native map mismatch");
      await this.blob(s,file,0,1,digest,bytes);
    }else{
      await this.statePage(session,p,s,start,digest,bytes);
    }
  }
  private async pac(s:NativeSource,p:Package,bytes:Uint8Array,bounds:Uint32Array,integrityVerified=false){
    const header=pacHeader(bytes,0,0xffffffff,new Set([1,nativePAC]));
    if(!header||header.addressBytes!==4||header.end!==bytes.length||header.digest!==s.digest||header.source!==s.tap)throw Error("PAC source mismatch");
    const placement=pacPlacement(header),first=Number(placement.firstRow),rows=Number(placement.rows);
    if(placement.pageSize!==(1<<20)||placement.firstRow<BigInt(p.manifest.RetiredRows??0)||placement.memberStart<BigInt(p.manifest.RetiredMembers??0)||placement.source!==s.generation||Number(placement.from)<p.manifest.From*1000||Number(placement.to)>p.manifest.To*1000||p.manifest.Provisional)throw Error("PAC outside sealed hour");
    if(bounds.length!==rows*2)throw Error("PAC bounds mismatch");
    const supplied=p.rows?.find(r=>r.start===first&&r.bytes.length===rows*INDEX_ROW_BYTES);
    if(!supplied)throw Error("Missing PAC index rows");
    // Only the internal transport worker sets integrityVerified, after checking
    // the sealed CRC and index checksum. Do not scan those same events again.
    let local=supplied.bytes;
    if(!integrityVerified){
      const lengths=new Uint32Array(rows);let end=header.start;
      for(let i=0;i<rows;i++){
        if(bounds[i*2]!==end||bounds[i*2+1]<=end||bounds[i*2+1]>header.end)throw Error("Invalid PAC member bounds");
        lengths[i]=bounds[i*2+1]-end;end=bounds[i*2+1];
      }
      if(end!==header.end)throw Error("Trailing PAC data");
      local=pacLocalRows(placement,supplied.bytes,lengths);
    }
    const start=Number(placement.memberStart);
    if(!Number.isSafeInteger(start+Number(placement.memberBytes)))throw Error("PAC address exceeds browser precision");
    const context={placement,common:header.common.slice()};
    const prior=s.contexts?.find(c=>c.placement.block===placement.block);
    if(prior){
      const old=prior.placement;
      if(Object.entries(placement).some(([key,value])=>key!=="pages"&&old[key as keyof PACPlacement]!==value)||
        old.pages.length!==placement.pages.length||old.pages.some((v,i)=>v.row!==placement.pages[i].row||v.page!==placement.pages[i].page)||
        hex(prior.common)!==hex(header.common))throw Error("PAC context changed");
    }else for(const c of s.contexts??[]){
      const v=c.placement;
      if(placement.firstRow<v.firstRow+v.rows&&placement.firstRow+placement.rows>v.firstRow||
        placement.memberStart<v.memberStart+v.memberBytes&&placement.memberStart+placement.memberBytes>v.memberStart)
        throw Error("Overlapping PAC placement");
    }
    // Diagnostic/unverified inputs retain the full temporal consistency check.
    if(!integrityVerified){
      for(let i=0;i<rows;i++){
        const row=supplied.bytes.subarray(i*INDEX_ROW_BYTES,(i+1)*INDEX_ROW_BYTES),ms=indexMillis(row,Number(placement.from));
        if(BigInt(ms)<placement.from||BigInt(ms)>=placement.to)throw Error("Invalid PAC event row");
        if(i>0&&indexMillis(supplied.bytes.subarray((i-1)*INDEX_ROW_BYTES,i*INDEX_ROW_BYTES),Number(placement.from))>ms)throw Error("Unordered PAC event rows");
      }
    }
    const directoryBytes=rows*4;
    const hour=new Uint8Array(4+directoryBytes+header.end-header.start);
    new DataView(hour.buffer).setUint32(0,hour.length,true);
    if(start<4+directoryBytes)throw Error("Invalid SODS segment start");
    hour.set(header.directory,4);hour.set(bytes.subarray(header.start,header.end),4+directoryBytes);
    await this.range(s,"sods",start-4-directoryBytes,hour);
    const segmentHeader=new Uint8Array(INDEX_ROW_BYTES),hv=new DataView(segmentHeader.buffer);
    hv.setUint32(0,Math.floor(Number(placement.from)/2**32),true);
    const hourIndex=new Uint8Array(INDEX_ROW_BYTES+local.length);
    hourIndex.set(segmentHeader);hourIndex.set(local,INDEX_ROW_BYTES);
    await this.range(s,"index",(first-1)*INDEX_ROW_BYTES,hourIndex);
    if(!prior){(s.contexts??=[]).push(context);s.contexts.sort((a,b)=>a.placement.firstRow<b.placement.firstRow?-1:1);}
    await this.backend.put([s]);
    p.rows=p.rows!.filter(r=>r!==supplied);
  }
  private async statePage(session:NativeSession,p:Package,s:NativeSource,start:number,digest:string,bytes:Uint8Array){
    const revision=s.stateFileRevision??0;
    if(p.stateFileRevision!==undefined&&p.stateFileRevision!==revision)throw new NativeCacheError("Native state compacted; retry collection",{code:"native-cache-race",reason:"Native state compacted"});
    p.stateFileRevision=revision;
    const v=decodeObject(bytes) as Record<string,unknown>;
    if(p.stateComplete||!v||!(v.Epoch instanceof Uint8Array)||v.Epoch.length!==16||
      !integer(v.Sequence)||!integer(v.Collection)||v.Collection>v.Sequence||
      (v.Objects!==null&&!Array.isArray(v.Objects))||
      (v.Paged!==undefined&&v.Paged!==true)||
      (v.Final!==undefined&&typeof v.Final!=="boolean")||
      (v.Unchanged!==undefined&&typeof v.Unchanged!=="boolean"))throw Error("Invalid native state");
    const epoch=hex(v.Epoch),sequence=v.Sequence,collection=v.Collection;
    if(p.manifest.StateEpoch!==undefined&&(!(p.manifest.StateEpoch instanceof Uint8Array)||hex(p.manifest.StateEpoch)!==epoch||p.manifest.StateSequence!==sequence))
      throw Error("State does not match native manifest");
    const entries=(v.Objects??[]) as {ID:Uint8Array;Token:number}[];
    if(v.Unchanged){
      if(!v.Paged||v.Final!==true||start!==0||entries.length||p.state)throw Error("Invalid unchanged state");
      const known=[...session.packages.filter(other=>other!==p&&other.key===p.key&&other.stateComplete&&other.stateFileRevision===revision).map(other=>other.state),s.state]
        .find(state=>state?.epoch===epoch&&state.sequence===sequence&&state.collection===collection);
      if(!known)throw Error("Missing unchanged native state");
      p.state=structuredClone(known);p.stateComplete=true;return;
    }
    if(start!==p.stateCount||(!v.Paged&&start!==0)||(v.Paged&&entries.length>1024))throw Error("Noncontiguous native state");
    if(p.state&&(p.state.epoch!==epoch||p.state.sequence!==sequence||p.state.collection!==collection))throw Error("Mixed native state snapshots");
    let first:string|undefined,last=p.stateLast;
    for(const entry of entries){
      if(!entry||!(entry.ID instanceof Uint8Array)||!integer(entry.Token)||entry.Token===0||entry.Token>sequence)throw Error("Invalid object state token");
      const id=hex(entry.ID);
      if(last!==undefined&&id<=last)throw Error("Unordered object state identities");
      first??=id;last=id;
    }
    if(v.Paged&&!v.Final&&!entries.length)throw Error("Empty intermediate state page");
    const blob=await this.blob(s,"state",start,entries.length,digest,bytes);
    p.state??={epoch,sequence,collection,pages:[]};
    p.state.pages.push({...blob,first,last:entries.length?last:undefined});
    p.stateCount+=entries.length;p.stateLast=last;p.stateComplete=v.Paged?v.Final===true:true;
  }
  private retiredHeader(h:FileIO,value:number){
    if(this.retiredHeaders.get(h)===value)return;
    const header=new Uint8Array(8);new DataView(header.buffer).setBigUint64(0,BigInt(value),true);
    write(h,header,0);this.retiredHeaders.set(h,value);
  }
  private async range(s:NativeSource,kind:"index"|"sods",start:number,bytes:Uint8Array){
    const f=s.files[kind], wanted={Start:start,End:start+bytes.length};
    if(!integer(wanted.End))throw Error("Native byte address overflow");
    const h=await this.backend.file(s.key,kind);
    try{
      // The file commit root selects durable allocation state after interruption.
      // Reconcile the minimal retired_bytes header on opening.
      recover(h,f.end);if(f.end===0){h.truncate(8);f.end=8;}
      this.retiredHeader(h,f.retired??0);
      // Repeated/interlaced pulls may overlap. Immutable bytes must agree, and
      // only missing byte runs are appended. Existing local addresses never move.
      for(const e of f.extents){
        const lo=Math.max(start,e.Start),hi=Math.min(wanted.End,e.End);
        if(lo>=hi)continue;
        const old=readFile(h,e.local+lo-e.Start,hi-lo);
        if(old.some((v,i)=>v!==bytes[lo-start+i]))throw Error("Native source changed without a generation change");
      }
      const pageSize=1<<20;f.pages??={};
      for(const gap of missing([wanted],f.extents)){
        for(let at=gap.Start;at<gap.End;){
          const page=Math.floor(at/pageSize),offset=at%pageSize,end=Math.min(gap.End,(page+1)*pageSize);
          let local=f.pages[page];
          if(local===undefined){local=f.free?.pop()??f.end;f.pages[page]=local;f.end=Math.max(f.end,local+pageSize);h.truncate(f.end);}
          write(h,bytes.subarray(at-start,end-start),local+offset);
          f.extents.push({Start:at,End:end,local:local+offset});at=end;
        }
      }
      f.extents.sort((a,b)=>a.Start-b.Start);
      h.flush();await this.backend.put([s]);
    }finally{h.close();}
  }
  private async blob(s:NativeSource,kind:"map"|"trie"|"state",start:number,count:number,digest:string,bytes:Uint8Array){
    const f=s.files[kind];
    const existing=f.blobs.find(b=>b.start===start&&b.count===count&&b.digest===digest);
    if(existing)return existing;
    const h=await this.backend.file(s.key,kind,kind==="state"?s.stateFileRevision:undefined);
    try{
      recover(h,f.end);write(h,bytes,f.end);h.flush();
      const blob={start,count,digest,local:f.end,length:bytes.length};
      f.blobs.push(blob);f.end+=bytes.length;
      await this.backend.put([s]);
      return blob;
    }finally{h.close();}
  }
  private async trie(s:NativeSource,start:number,nodes:unknown[],bytes:Uint8Array,digest:string){
    const f=s.files.trie, h=await this.backend.file(s.key,"trie");
    try{
      recover(h,f.end);
      for(const b of f.blobs){
        const lo=Math.max(start,b.start),hi=Math.min(start+nodes.length,b.start+b.count);
        if(lo>=hi)continue;
        const prior=decodeObject(readFile(h,b.local,b.length)) as Record<string,unknown>[];
        for(let id=lo;id<hi;id++){
          const a=prior[id-b.start],n=nodes[id-start] as Record<string,unknown>;
          if(a["0"]!==n["0"]||a["1"]!==n["1"]||hex(a["2"] as Uint8Array)!==hex(n["2"] as Uint8Array))
            throw Error("Immutable trie prefix changed");
        }
      }
      if(!missing([{Start:start,End:start+nodes.length}],f.blobs.map(b=>({Start:b.start,End:b.start+b.count}))).length)return;
    }finally{h.close();}
    // Keep the original source encoding; never rebuild the trie. A rare partially
    // overlapping prefix can retain redundant dependency bytes, not event bytes.
    await this.blob(s,"trie",start,nodes.length,digest,bytes);
  }
  // Only call after PILStream.finish() validates the response success marker.
  // Chunks are durable earlier, but no new package/range is advertised before it.
  async finish(session:NativeSession){
    if(session.finished||session.active.size||session.packages.some(p=>!p.closed))throw Error("Incomplete native response");
    if(await this.serial()!==session.serial)throw new NativeCacheError("Native cache rebased; retry collection",{code:"native-cache-race",reason:"Native cache rebased"});
    const changed=new Map<string,NativeSource>();
    for(const p of session.packages){
      const s=changed.get(p.key)??await this.backend.get(p.key);
      if(!s)throw Error("Missing native source");
      if(s.retiredBy)throw new NativeCacheError("Native generation retired; retry collection",{code:"native-cache-race",reason:"Native generation retired"});
      const m=p.manifest;
      if((m.RetiredRows??0)<(s.retiredRows??0)||(m.RetiredMembers??0)<(s.retiredMembers??0))throw Error("Stale PAC retirement boundary");
      this.retire(s,m.RetiredRows??0,m.RetiredMembers??0,m.TimeRetainedFrom);
      if(s.generationSequence!==undefined&&m.GenerationSequence!==s.generationSequence)throw new NativeCacheError("Native generation sequence changed",{code:"native-cache-conflict",source:s.tap,reason:"sequence-changed",detail:{generation:s.generation,cachedSequence:s.generationSequence,incomingSequence:m.GenerationSequence??null}});
      s.generationSequence=m.GenerationSequence;
      if(!p.state||!p.stateComplete)throw Error("Incomplete native state snapshot");
      if(p.stateFileRevision!==(s.stateFileRevision??0))throw new NativeCacheError("Native state compacted; retry collection",{code:"native-cache-race",reason:"Native state compacted"});
      if(s.state&&s.state.epoch!==p.state.epoch)throw new NativeCacheError("Native state epoch changed; rebase required",{code:"native-cache-conflict",source:s.tap,reason:"state-epoch-changed",detail:{cachedEpoch:s.state.epoch,incomingEpoch:p.state.epoch}});
      if(!s.state||p.state.sequence>s.state.sequence)s.state=structuredClone(p.state);
      for(const r of m.Index)resolve(s.files.index.extents,{Start:r.Start*INDEX_ROW_BYTES,End:r.End*INDEX_ROW_BYTES});
      if(missing(m.MemberRanges,s.files.sods.extents).length)throw Error("Missing native payload");
      if(m.TrieNodes>1&&missing([{Start:1,End:m.TrieNodes}],s.files.trie.blobs.map(b=>({Start:b.start,End:b.start+b.count}))).length)
        throw Error("Missing native trie dependency");
      if(!m.Provisional&&!m.TimeRetired&&!m.TimeTrie)throw Error("Missing sealed time trie");
      if(m.TimeTrie){
        if(m.Provisional)throw Error("Provisional time trie publication");
        const h=new DataView(m.TimeTrie.buffer,m.TimeTrie.byteOffset,m.TimeTrie.byteLength);
        if(Number(h.getBigUint64(64,true))<m.From*1000||Number(h.getBigUint64(72,true))>m.To*1000)throw Error("Time trie outside collection");
        const received=mergeTimeWindow(undefined,m.TimeTrie,m.Generation);
        const payloads=new Map((s.contexts??[]).map(c=>[Number(c.placement.from),c.placement]));
        for(const span of received.ranges)for(let at=span.from;at<span.to;at+=HOUR){
          const leaf=timeLeaf(received,at)!;if(!leaf.count)continue;
          resolve(s.files.index.extents,{Start:(leaf.start+1)*INDEX_ROW_BYTES,End:leaf.end*INDEX_ROW_BYTES});
          if(!m.IndexOnly){
            const payload=payloads.get(at);
            if(!payload||Number(payload.firstRow)!==leaf.start+1||Number(payload.rows)!==leaf.count)throw Error('Missing complete hourly payload');
            resolve(s.files.sods.extents,{Start:Number(payload.memberStart)-4*Number(payload.rows)-4,End:Number(payload.memberStart+payload.memberBytes)});
          }
        }
        s.timeTrie=mergeTimeWindow(s.timeTrie,m.TimeTrie,m.Generation,TIME_INDEX|TIME_STATE|(m.IndexOnly?0:TIME_SODS),(m.RetiredRows??0)>0,true);
      }
      s.trieNodes=Math.max(s.trieNodes,m.TrieNodes);
      changed.set(s.key,s);
    }
    // Publication orders physical generations, never response arrival times.
    // A failed or truncated replacement cannot retire committed coverage.
    const known=await this.backend.list();
    for(const current of [...changed.values()]){
      const peers=known.filter(s=>s.key!==current.key&&s.tap===current.tap&&s.digest===current.digest&&!s.retiredBy&&s.state);
      if([...changed.values()].some(s=>s.key!==current.key&&s.tap===current.tap&&s.digest===current.digest))
        throw new NativeCacheError("Generation changed during collection; retry collection",{code:"native-cache-race",reason:"Generation changed during collection"});
      for(const prior of peers){
        if(current.generationSequence===undefined){
          if(prior.generationSequence!==undefined)throw new NativeCacheError("Unordered native generation; retry collection",{code:"native-cache-conflict",source:current.tap,reason:"missing-generation-sequence"});
          continue; // Legacy peers remain isolated until a sequenced response.
        }
        if(prior.generationSequence!==undefined&&prior.generationSequence>=current.generationSequence)
          throw new NativeCacheError("Stale or conflicting native generation; retry collection",{
            code:prior.generationSequence===current.generationSequence||prior.state?.epoch!==current.state?.epoch?"native-cache-conflict":"native-cache-race",
            source:current.tap,reason:prior.generationSequence===current.generationSequence?"generation-fork":"older-generation",
            detail:{cachedGeneration:prior.generation,incomingGeneration:current.generation,
              cachedSequence:prior.generationSequence,incomingSequence:current.generationSequence,
              cachedEpoch:prior.state?.epoch??null,incomingEpoch:current.state?.epoch??null}});
        if(prior.state?.epoch!==current.state?.epoch)throw new NativeCacheError("Native state epoch changed; rebase required",{code:"native-cache-conflict",source:current.tap,reason:"state-epoch-changed",detail:{cachedEpoch:prior.state?.epoch??null,incomingEpoch:current.state?.epoch??null}});
        if(prior.state&&current.state&&prior.state.sequence>current.state.sequence)throw new NativeCacheError("Replacement state regressed; retry collection",{code:"native-cache-race",reason:"Replacement state regressed"});
        prior.retiredBy=current.key;delete prior.timeTrie;delete prior.state;
        changed.set(prior.key,prior);
      }
    }
    await this.backend.put([...changed.values()]);session.finished=true;
    for(const s of changed.values())if(!s.retiredBy)for(const kind of ["index","sods"] as const){
      if(s.files[kind].end<8)continue;
      const h=await this.backend.file(s.key,kind);
      try{recover(h,s.files[kind].end);this.retiredHeader(h,s.files[kind].retired??0);h.flush();}finally{h.close();}
    }
  }
  private retire(s:NativeSource,rows:number,members:number,first?:number){
    if(rows>0&&first!==undefined){s.timeTrie=retireTime(s.timeTrie,first);}
    if(rows===(s.retiredRows??0)&&members===(s.retiredMembers??0))return;
    for(const [kind,base] of [["index",rows*INDEX_ROW_BYTES],["sods",members]] as const){
      const f=s.files[kind];f.retired=base;
      f.extents=f.extents.filter(e=>e.End>base).map(e=>({...e,local:e.local+Math.max(0,base-e.Start),Start:Math.max(e.Start,base)}));
      for(const [page,local] of Object.entries(f.pages??{}))if((Number(page)+1)*(1<<20)<=base){
        (f.free??=[]).push(local);delete f.pages![page];
      }
    }
    // Trim free physical tail pages after publication; interior slots remain
    // reusable without relocating any surviving source coordinates.
    for(const kind of ["index","sods"] as const){
      const f=s.files[kind],free=new Set(f.free??[]);
      while(f.end>8&&free.delete(f.end-(1<<20)))f.end-=1<<20;
      f.free=[...free];
    }
    s.retiredRows=rows;s.retiredMembers=members;
    s.contexts=s.contexts?.filter(c=>c.placement.firstRow+c.placement.rows>BigInt(rows));
  }
  async reclaim():Promise<number>{
    let count=0;
    for(const source of await this.backend.list()){
      if(!source.retiredBy||source.reclaimed)continue;
      await this.backend.removeFiles(source.key);
      for(const file of Object.values(source.files)){file.end=0;file.blobs=[];file.extents=[];delete file.pages;delete file.free;}
      source.trieNodes=1;source.reclaimed=true;
      await this.backend.put([source]);count++;
    }
    return count;
  }
  async compactState(key:string,force=false):Promise<boolean>{
    const source=await this.backend.get(key);
    if(!source?.state||source.retiredBy)return false;
    const revision=source.stateFileRevision??0;
    const live=source.state.pages.reduce((size,page)=>size+page.length,0);
    if(!force&&source.files.state.end<=Math.max(1<<20,4*live)){
      if(revision)await this.backend.removeOldStateFiles(key,revision);
      return false;
    }
    if(!integer(revision+1))throw Error("State file revision exhausted");
    const input=await this.backend.file(key,"state",revision);
    try{
      recover(input,source.files.state.end);
      const output=await this.backend.file(key,"state",revision+1);
      try{
        output.truncate(0);
        const pages:StatePage[]=[];
        let end=0;
        for(const page of source.state.pages){
          const bytes=readFile(input,page.local,page.length);
          if(await hash(bytes)!==page.digest)throw Error("Cached state checksum mismatch");
          write(output,bytes,end);pages.push({...page,local:end});end+=bytes.length;
        }
        output.flush();
        source.files.state={end,extents:[],blobs:pages.map(({first,last,...page})=>page)};
        source.state={...source.state,pages};source.stateFileRevision=revision+1;
        await this.backend.put([source]);
      }finally{output.close();}
    }finally{input.close();}
    // Atomic metadata selection precedes deletion. Failed deletion is retried
    // by maintenance; a failed publication leaves the old snapshot authoritative.
    await this.backend.removeOldStateFiles(key,revision+1);
    return true;
  }
  async maintain():Promise<void>{
    await this.reclaim();
    for(const source of await this.backend.list())if(!source.retiredBy&&source.state)await this.compactState(source.key);
  }
  // Explicit local reset only. Never infer this from a mismatched server epoch.
  // A scoped key salt prevents old row references rebinding after server restore;
  // the global serial rejects requests that began before the reset in any tab.
  async rebase(source:string):Promise<number>{return this.rebaseMany([source]);}
  async rebaseMany(sources:readonly string[]):Promise<number>{
    if(!sources.length)throw Error("Missing resources to refresh");
    const scopes=new Set<string>();
    for(const source of sources){objectParameters({source,id:""});scopes.add(source.split("/").slice(0,3).join("/"));}
    const control=await this.backend.control();
    if(!integer(control.serial+1))throw Error("Native rebase sequence exhausted");
    for(const scope of scopes){
      const next=(control.scopes[scope]??0)+1;
      if(!integer(next))throw Error("Native rebase sequence exhausted");
      control.scopes[scope]=next;
    }
    control.serial++;
    const retired=(await this.backend.list()).filter(s=>!s.retiredBy&&scopes.has(s.tap.split("/").slice(0,3).join("/")));
    for(const s of retired){s.retiredBy=`rebase:${control.serial}`;delete s.timeTrie;delete s.state;}
    // One durable transaction selects every resource salt and retires its old
    // pointers. No other tab can observe a partially refreshed resource set.
    await this.backend.put(retired,control);
    return retired.length;
  }
  async activeSelections<T extends {source:string}>(selections:T[]):Promise<T[]>{
    const live=new Set((await this.backend.list()).filter(s=>!s.retiredBy&&s.state).map(s=>s.key));
    return selections.filter(s=>live.has(s.source));
  }
  async missingTime(key:string,from:number,to:number){
    const s=await this.backend.get(key);
    return missingHours(s?.retiredBy?undefined:s?.timeTrie,from,to);
  }
  async coverage(tap?:string){
    return (await this.backend.list()).filter(s=>!s.retiredBy&&matchesSource(tap??"",s.tap)).filter(s=>s.state).map(s=>({
      Definition:unhex(s.digest),Generation:unhex(s.generation),Index:committedRanges(s).index,MemberRanges:committedRanges(s).sods,TrieNodes:s.trieNodes,
      ...(s.state?{StateKnown:true,StateEpoch:unhex(s.state.epoch),StateSequence:s.state.sequence}:{}),
    }));
  }
  async objectState(query:ObjectQuery):Promise<{token:string;sequence:number}>{
    objectParameters(query);
    const collection=query.source.split("/").slice(0,3).join("/");
    const sources=(await this.backend.list()).filter(s=>!s.retiredBy&&s.state&&s.tap.split("/").slice(0,3).join("/")===collection);
    if(!sources.length)throw Error("No committed cached object state");
    if(new Set(sources.map(s=>s.state!.epoch)).size!==1)throw Error("Conflicting cached state epochs; rebase required");
    sources.sort((a,b)=>b.state!.sequence-a.state!.sequence);
    const source=sources[0],state=source.state!;
    // Token lookup needs the identity type, not the replicated trie.
    const mapFile=await this.backend.file(source.key,"map");
    let map:Uint8Array;
    try{
      recover(mapFile,source.files.map.end);
      map=readFile(mapFile,0,source.files.map.end);
      if(await hash(map)!==source.digest)throw Error("Cached native map checksum mismatch");
    }finally{mapFile.close();}
    const definition=decodeObject(map) as PILDefinition;
    const role=resourceBinding(definition)?.roles.id, field=definition.fields.find(f=>f.name===role);
    if(!field)throw Error("Collection has no object identity");
    const id=hex(objectIdentityBytes(field.type,query.id));
    const page=state.pages.find(p=>p.first!==undefined&&p.last!==undefined&&p.first<=id&&id<=p.last);
    let token=0;
    if(page){
      const handle=await this.backend.file(source.key,"state",source.stateFileRevision);
      try{
        recover(handle,source.files.state.end);
        const raw=readFile(handle,page.local,page.length);
        if(await hash(raw)!==page.digest)throw Error("Cached state checksum mismatch");
        const snapshot=decodeObject(raw) as {Objects:{ID:Uint8Array;Token:number}[]};
        token=snapshot.Objects.find(entry=>hex(entry.ID)===id)?.Token??0;
      }finally{handle.close();}
    }
    const suffix=new Uint8Array(16),view=new DataView(suffix.buffer);
    view.setBigUint64(0,BigInt(state.collection),true);view.setBigUint64(8,BigInt(token),true);
    return {token:state.epoch+hex(suffix),sequence:state.sequence};
  }
  async definitions():Promise<Metadata[]>{
    const known=new Set<string>(),out:Metadata[]=[];
    for(const s of await this.backend.list()){
      if(s.retiredBy)continue;
      if(known.has(s.digest)||!s.files.map.end)continue;
      const h=await this.backend.file(s.key,"map");
      try{
        recover(h,s.files.map.end);
        const map=readFile(h,0,s.files.map.end);
        if(await hash(map)!==s.digest)throw Error("Cached native map checksum mismatch");
        out.push({digest:s.digest,store:map,pil:map});known.add(s.digest);
      }finally{h.close();}
    }
    return out;
  }
  async read(key:string,kind:"index"|"sods",wanted:Span){
    const s=await this.backend.get(key);if(!s)throw Error("Unknown native source");
    if(s.retiredBy)throw Error("Native generation retired; recollect this window");
    const extents=resolve(s.files[kind].extents,wanted), h=await this.backend.file(key,kind);
    try{
      recover(h,s.files[kind].end);
      const out=new Uint8Array(wanted.End-wanted.Start);
      for(const e of extents)out.set(readFile(h,e.local,e.End-e.Start),e.Start-wanted.Start);
      return out;
    }finally{h.close();}
  }
  async dependencies(key:string){
    const source=await this.backend.get(key);if(!source)throw Error("Unknown native source");
    if(source.retiredBy)throw Error("Native generation retired; recollect this window");
    const load=async(kind:"map"|"trie")=>{
      const h=await this.backend.file(key,kind);
      try{
        recover(h,source.files[kind].end);
        return source.files[kind].blobs.filter(b=>kind==="map"||b.start<source.trieNodes).map(b=>({start:b.start,bytes:readFile(h,b.local,b.length)}));
      }finally{h.close();}
    };
    const maps=await load("map");if(maps.length!==1)throw Error("Missing native map");
    return {source,definition:{digest:source.digest,store:maps[0].bytes,pil:maps[0].bytes} satisfies Metadata,trie:await load("trie")};
  }
  async records(key:string,from:number,to:number,rows:number[]):Promise<NativePage>{
    if(!Array.isArray(rows)||!rows.length||rows.length>4096||rows.some((row,i)=>!integer(row)||(i>0&&row<=rows[i-1])))
      throw Error("Invalid native record selection");
    return this.readPage(key,from,to,undefined,rows.length,rows);
  }
  async page(key:string,from:number,to:number,after?:number,limit=512,descending=false):Promise<NativePage>{
    return this.readPage(key,from,to,after,limit,undefined,descending);
  }
  private async readPage(key:string,from:number,to:number,after:number|undefined,limit:number,selected?:number[],descending=false):Promise<NativePage>{
    if(!integer(from)||!integer(to)||from>=to||after!==undefined&&!integer(after)||!integer(limit)||limit<1||limit>4096)
      throw Error("Invalid native page request");
    const s=await this.backend.get(key);if(!s)throw Error("Unknown native source");
    if(s.retiredBy)throw Error("Native generation retired; recollect this window");
    const index=await this.backend.file(key,"index"),sodsFile=await this.backend.file(key,"sods");
    try{
      recover(index,s.files.index.end);recover(sodsFile,s.files.sods.end);
      // Binary probes, sequential rows, directory entries, and payloads have
      // different locality. Keep their small read windows independent.
      const readBounds=rangeReader(index,s.files.index.extents,4096);
      const readIndex=rangeReader(index,s.files.index.extents,16384);
      const readDirectory=rangeReader(sodsFile,s.files.sods.extents,16384);
      const readPayload=rangeReader(sodsFile,s.files.sods.extents,65536);
      const at=(row:number)=>readIndex({Start:row*INDEX_ROW_BYTES,End:(row+1)*INDEX_ROW_BYTES});
      const records:Uint8Array[]=[],values:Uint8Array[]=[],offsets:number[]=[],common:(Uint8Array|undefined)[]=[];
      const bases:number[]=[],ordinals:number[]=[];
      let total=0,last:number|undefined,done=true;
      outer: for(const context of descending?[...(s.contexts??[])].reverse():s.contexts??[]){
        const placement=context.placement;
        if(Number(placement.to)<=from||Number(placement.from)>=to)continue;
        if(!((timeLeaf(s.timeTrie,Number(placement.from))?.flags??0)&TIME_SODS))continue;
        const range={Start:Number(placement.firstRow),End:Number(placement.firstRow+placement.rows)};
        const lower=(start:number,end:number,time:number)=>{while(start<end){const mid=start+Math.floor((end-start)/2);if(indexMillis(readBounds({Start:mid*INDEX_ROW_BYTES,End:(mid+1)*INDEX_ROW_BYTES}),Number(placement.from))<time)start=mid+1;else end=mid;}return start;};
        let first=Math.max(range.Start,after===undefined||descending?0:after+1);
        if(first>=range.End)continue;
        first=selected?first:lower(first,range.End,from);
        const end=Math.min(selected?range.End:lower(first,range.End,to),descending&&after!==undefined?after:Infinity);
        const requested=selected?.filter(row=>row>=first&&row<end);
        for(let position=0,row=requested?requested[0]:descending?end-1:first;row!==undefined&&row<end&&row>=first;row=requested?requested[++position]:row+(descending?-1:1)){
          if(!selected&&(records.length>=limit||total>=8*1024*1024)){done=false;break outer;}
          const raw=at(row).slice();
          if(selected&&(indexMillis(raw,Number(placement.from))<from||indexMillis(raw,Number(placement.from))>=to))
            throw Error("Selected record outside requested hour");
          const base=Number(placement.memberStart),directory=base-Number(placement.rows)*4;
          const offsetAt=directory+(row-range.Start)*4,offsetBytes=readDirectory({Start:offsetAt,End:offsetAt+(row+1<range.End?8:4)}),offsetView=new DataView(offsetBytes.buffer,offsetBytes.byteOffset,offsetBytes.length);
          const relativeStart=offsetView.getUint32(0,true),relativeEnd=row+1<range.End?offsetView.getUint32(4,true):Number(placement.memberBytes);
          if(relativeStart>=relativeEnd||relativeEnd>Number(placement.memberBytes))throw Error("Invalid PAC directory entry");
          const start=base+relativeStart,finish=base+relativeEnd;
          bases.push(Number(placement.from));ordinals.push(row);
          if(!Number.isSafeInteger(finish)||finish<=start)throw Error("Invalid PAC local coordinates");
          const wanted={Start:start,End:finish};
          const bytes=readPayload(wanted).slice();common.push(context.common.length?context.common:undefined);
          records.push(raw);values.push(bytes);offsets.push(total,total+bytes.length);total+=bytes.length;last=row;
        }
      }
      if(selected&&records.length!==selected.length)throw Error("Missing canonical search record");
      const rows=new Uint8Array(records.length*INDEX_ROW_BYTES),payload=new Uint8Array(total);
      records.forEach((b,i)=>rows.set(b,i*INDEX_ROW_BYTES));values.forEach((b,i)=>payload.set(b,offsets[i*2]));
      return {ordinals:Float64Array.from(ordinals),bases:Float64Array.from(bases),rows,payload,offsets:Uint32Array.from(offsets),common,after:last,done};
    }finally{index.close();sodsFile.close();}
  }
}
function unhex(s:string){return s.match(/../g)!.map(v=>parseInt(v,16));}
function rangeReader(file:FileIO,extents:Extent[],windowBytes=1<<20){
  // Header/event writes can publish separate extents in the same local page.
  // Merge only contiguous known bytes at contiguous physical positions, so a
  // 20-byte window is not split merely by the original write boundaries.
  const contiguous:Extent[]=[];
  let previousEnd=0;
  for(const e of extents){
    if(!integer(e.Start)||!integer(e.End)||!integer(e.local)||e.Start<previousEnd||e.End<=e.Start)throw Error('Invalid native extents');
    previousEnd=e.End;const last=contiguous.at(-1);
    if(last&&last.End===e.Start&&last.local+last.End-last.Start===e.local)last.End=e.End;
    else contiguous.push({...e});
  }
  extents=contiguous;
  let start=-1,end=-1,cached=new Uint8Array();
  return (wanted:Span):Uint8Array=>{
    if(wanted.Start>=start&&wanted.End<=end)return cached.subarray(wanted.Start-start,wanted.End-start);
    // Binary lookup avoids sorting/scanning every extent on each cache miss.
    let lo=0,hi=extents.length;
    while(lo<hi){const mid=(lo+hi)>>>1;if(extents[mid].End<=wanted.Start)lo=mid+1;else hi=mid;}
    const extent=extents[lo];
    if(extent&&extent.Start<=wanted.Start&&extent.End>=wanted.End){
      // Align within the extent so descending scans reuse the window too.
      start=extent.Start+Math.floor((wanted.Start-extent.Start)/windowBytes)*windowBytes;
      end=Math.min(extent.End,Math.max(wanted.End,start+windowBytes));
      cached=readFile(file,extent.local+start-extent.Start,end-start);
      return cached.subarray(wanted.Start-start,wanted.End-start);
    }
    const parts=resolve(extents,wanted);
    const result=new Uint8Array(wanted.End-wanted.Start);
    for(const p of parts)result.set(readFile(file,p.local,p.End-p.Start),p.Start-wanted.Start);
    return result;
  };
}

/** Derive wire coverage only from published branch leaves and their segments. */
export function committedRanges(source:NativeSource):{index:Span[];sods:Span[]} {
 const index:Span[]=[],sods:Span[]=[];
 if(source.retiredBy)return {index,sods};
 for(const range of source.timeTrie?.ranges??[])for(let at=range.from;at<range.to;at+=HOUR){
  const leaf=timeLeaf(source.timeTrie,at)!;
  if((leaf.flags&TIME_INDEX)&&leaf.count)index.push({Start:leaf.start+1,End:leaf.end});
 }
 for(const {placement:p} of source.contexts??[]){
  if(!((timeLeaf(source.timeTrie,Number(p.from))?.flags??0)&TIME_SODS))continue;
  if(p.memberBytes)sods.push({Start:Number(p.memberStart)-4*Number(p.rows)-4,End:Number(p.memberStart+p.memberBytes)});
 }
 return {index:normalize(index),sods:normalize(sods)};
}
