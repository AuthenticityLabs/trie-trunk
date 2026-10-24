import { mergeTimeWindow, missingHours, timeLeaf, retireTime } from "./time-trie.ts";
import { indexMillis } from '../pil/pac-native.ts';
import { pacHeader } from "../pil/pac.ts";
import { pacPlacement, pacLocalRows, nativePAC, type PACPlacement } from "../pil/pac-native.ts";
import { resourceBinding, type PILDefinition } from "../pil/layout.ts";
import { cbor } from "../pil/cbor.ts";
import { matchesSource } from "../source.ts";
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
type Kind = "index" | "population" | "trie" | "map" | "state";
export type NativeControl = { serial:number; scopes:Record<string,number> };
export type NativeManifest = {
  TimeTrie?:Uint8Array;TimeRetired?:boolean;TimeRetainedFrom?:number;
  RetiredRows?:number;RetiredMembers?:number;RetiredPopulation?:number;
  Definition: Uint8Array; Generation: Uint8Array; Branch: number;
  From: number; To: number; Tail: number; Checkpoint: number; RowWidth: 16;
  TrieNodes: number; Index: Span[]; Population: Span[];
  Provisional: boolean; IndexPeriod: string; Timezone: string;
  StateEpoch?: Uint8Array; StateSequence?: number;
  GenerationSequence?: number;
};
export type NativeSource = {
  timeTrie?:Uint8Array;
  key: string; tap: string; digest: string; generation: string;
  files: Record<Kind, File>;
  index: Span[]; population: Span[]; trieNodes: number;
  retiredRows?:number;retiredMembers?:number;
  contexts?: {placement:PACPlacement;common:Uint8Array}[];
  packages: NativeManifest[];
  state?: StateSnapshot;
  stateFileRevision?: number;
  generationSequence?: number;
  retiredBy?: string;
  reclaimed?: boolean;
};
// The host serializes each operation across tabs. File flush precedes a strict
// metadata transaction. No IDB transaction is held across OPFS I/O.
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
  const m=raw as unknown as NativeManifest;
  if(!(m.Definition instanceof Uint8Array)||hex(m.Definition)!==binding.digest||
    !(m.Generation instanceof Uint8Array)||m.Generation.length!==16||m.RowWidth!==16||
    !integer(m.Branch)||m.Branch>4||!integer(m.Tail)||m.Tail>Number.MAX_SAFE_INTEGER/16||
    !integer(m.Checkpoint)||!integer(m.TrieNodes)||m.TrieNodes<1||m.TrieNodes>65536||
    !integer(m.From)||!integer(m.To)||m.From>=m.To||
    typeof m.Provisional!=="boolean"||typeof m.IndexPeriod!=="string"||typeof m.Timezone!=="string")
    throw Error("Invalid native manifest");
  if(m.GenerationSequence!==undefined&&(!integer(m.GenerationSequence)||m.GenerationSequence===0))throw Error("Invalid native generation sequence");
  m.Index=normalize(m.Index??[]);m.Population=normalize(m.Population??[]);
  if(m.Index.some(r=>r.End>m.Tail))
    throw Error("Invalid native index boundary");
  const rows=m.RetiredRows??0,members=m.RetiredMembers??0;
  if(!integer(rows)||rows>m.Tail||!integer(members)||!integer(m.RetiredPopulation??0)||
    m.Index.some(r=>r.Start<rows)||m.Population.some(r=>r.Start<members))throw Error("Invalid PAC retirement boundary");
  // CBOR byte strings are views into the received buffer. Structured cloning a
  // view copies its entire backing buffer, not just its visible bytes. Persist
  // owned digest/identity bytes so each hourly manifest cannot retain a whole
  // network response (hundreds of MB across a month of small packages).
  return structuredClone({...m,Definition:m.Definition.slice(),Generation:m.Generation.slice(),
    ...(m.StateEpoch instanceof Uint8Array?{StateEpoch:m.StateEpoch.slice()}:{}),
    ...(m.TimeTrie instanceof Uint8Array?{TimeTrie:m.TimeTrie.slice()}:{})});
}
function empty(key:string,b:Binding,m:NativeManifest):NativeSource {
  const file=():File=>({end:0,extents:[],blobs:[]});
  return {key,tap:b.tap,digest:b.digest,generation:hex(m.Generation),
    files:{index:file(),population:file(),trie:file(),map:file(),state:file()},
    index:[],population:[],trieNodes:1,packages:[]};
}
type Package = { rows?:{start:number;bytes:Uint8Array}[]; key:string; binding:Binding; manifest:NativeManifest; state?:StateSnapshot; stateFileRevision?:number; stateComplete:boolean; stateCount:number; stateLast?:string; closed:boolean };
export type NativeSession = { packages: Package[]; active: Map<number,Package>; finished:boolean; serial:number };
export type NativePage = { bases: Float64Array; common?:(Uint8Array|undefined)[]; rows: Uint8Array; population: Uint8Array; offsets: Uint32Array; after?: number; done: boolean };

export class NativeStore {
  private backend: NativeBackend;
  constructor(backend: NativeBackend) { this.backend=backend; }
  async serial():Promise<number>{return (await this.backend.control()).serial;}
  begin(serial=0):NativeSession {return {packages:[],active:new Map(),finished:false,serial};}
  async record(session:NativeSession,record:NativeRecord,definition?:Metadata){
    if(session.finished)throw Error("Native response already finished");
    const control=await this.backend.control();
    if(control.serial!==session.serial)throw Error("Native cache rebased; retry collection");
    const b=record.binding;
    if(record.kind==="manifest"){
      if(session.active.has(b.stream))throw Error("Overlapping native package");
      const m=manifest(record.manifest,b);
      if(m.Provisional&&(m.Index.length||m.Population.length))throw Error("Partial hour cannot be persisted");
      const salt=control.scopes[b.tap.split("/").slice(0,3).join("/")]??0;
      const key=await hash(new TextEncoder().encode(JSON.stringify([b.tap,b.digest,hex(m.Generation),...(salt?[salt]:[])])));
      const s=await this.backend.get(key)??empty(key,b,m);
      if(!integer(m.RetiredRows??0)||!integer(m.RetiredMembers??0)||(m.RetiredRows??0)<(s.retiredRows??0)||(m.RetiredMembers??0)<(s.retiredMembers??0))throw Error("Stale PAC retirement boundary");
      if(s.retiredBy)throw Error("Native generation retired; retry collection");
      if(!s.files.map.end){
        if(!definition||definition.digest!==b.digest||await hash(definition.store)!==b.digest||
          definition.store.length!==definition.pil.length||definition.store.some((v,i)=>v!==definition.pil[i]))
          throw Error("Native map is missing or inconsistent");
        await this.blob(s,"map",0,1,b.digest,definition.store);
      }
      const p:Package={key,binding:b,manifest:m,stateComplete:false,stateCount:0,closed:false};
      session.packages.push(p);session.active.set(b.stream,p);return;
    }
    const p=session.active.get(b.stream);
    if(!p||b.tap!==p.binding.tap||b.digest!==p.binding.digest)throw Error("Native control outside package");
    if(record.kind==="package-end"){
      if(p.rows?.length)throw Error("Incomplete population record");
      if(!p.stateComplete)throw Error("Missing native invalidation state");
      p.closed=true;session.active.delete(b.stream);return;
    }
    const s=await this.backend.get(p.key);
    if(!s)throw Error("Native source disappeared");
    if((p.manifest.RetiredRows??0)<(s.retiredRows??0)||(p.manifest.RetiredMembers??0)<(s.retiredMembers??0))throw Error("Stale PAC retirement boundary");
    if(s.retiredBy)throw Error("Native generation retired; retry collection");
    if(record.kind==="pac"){
      await this.pac(s,p,record.bytes,record.bounds);return;
    }
    const {file,start,bytes,digest}=record;
    if(!integer(start)||!bytes.length||await hash(bytes)!==digest)throw Error("Invalid native chunk");
    if(file==="index"||file==="population"){
      const unit=file==="index"?16:1;
      if(bytes.length%unit)throw Error("Partial native row");
      const end=start+bytes.length/unit;
      if(!integer(end)||missing([{Start:start,End:end}],file==="index"?p.manifest.Index:p.manifest.Population).length)
        throw Error("Native chunk outside requested ranges");
      if(file==="population")throw Error("Expected complete PAC");
      (p.rows??=[]).push({start,bytes:bytes.slice()});
    }else if(file==="trie"){
      const nodes=cbor(bytes);
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
  private async pac(s:NativeSource,p:Package,bytes:Uint8Array,bounds:Uint32Array){
    const header=pacHeader(bytes,0,0xffffffff,new Set([1,nativePAC]));
    if(!header||header.end!==bytes.length||header.digest!==s.digest||header.source!==s.tap)throw Error("PAC source mismatch");
    const placement=pacPlacement(header),first=Number(placement.firstRow),rows=Number(placement.rows);
    if(placement.pageSize!==(1<<20)||placement.firstRow<BigInt(p.manifest.RetiredRows??0)||placement.memberStart<BigInt(p.manifest.RetiredMembers??0)||placement.source!==s.generation||Number(placement.from)<p.manifest.From*1000||Number(placement.to)>p.manifest.To*1000||p.manifest.Provisional)throw Error("PAC outside sealed hour");
    if(bounds.length!==rows*2)throw Error("PAC bounds mismatch");
    const supplied=p.rows?.find(r=>r.start===first&&r.bytes.length===rows*16);
    if(!supplied)throw Error("Missing PAC index rows");
    const lengths=new Uint32Array(rows);let end=header.start;
    for(let i=0;i<rows;i++){
      if(bounds[i*2]!==end||bounds[i*2+1]<=end||bounds[i*2+1]>header.end)throw Error("Invalid PAC member bounds");
      lengths[i]=bounds[i*2+1]-end;end=bounds[i*2+1];
    }
    if(end!==header.end)throw Error("Trailing PAC data");
    const local=pacLocalRows(placement,supplied.bytes,lengths),start=Number(placement.memberStart);
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
    // Validate the temporal directory before publishing byte extents. The
    // supplied row identity stays canonical; no per-event UUID map is built.
    for(let i=0;i<rows;i++){
      const row=supplied.bytes.subarray(i*16,(i+1)*16), ms=indexMillis(row,Number(placement.from));
      if(BigInt(ms)<placement.from||BigInt(ms)>=placement.to)throw Error("Invalid PAC event row");
      if(i>0){const previous=supplied.bytes.subarray((i-1)*16,(i-1)*16+12);let compared=0;
        for(let j=0;j<12&&!compared;j++)compared=previous[j]-row[j];
        if(compared>=0)throw Error("Unordered PAC event rows");
      }
    }
    const sodsHeader=new Uint8Array(4);new DataView(sodsHeader.buffer).setUint32(0,Number(placement.memberBytes)+4,true);
    if(start<4)throw Error("Invalid SODS segment start");
    await this.range(s,"population",start-4,sodsHeader);
    await this.range(s,"population",start,bytes.subarray(header.start,header.end));
    const segmentHeader=new Uint8Array(16),hv=new DataView(segmentHeader.buffer);
    // Literal count and width match the server segment header.
    hv.setUint32(0,Number(placement.rows),true);hv.setBigInt64(4,placement.from,true);segmentHeader[12]=placement.rows<=65535n?2:placement.rows<=16777215n?3:4;
    await this.range(s,"index",(first-1)*16,segmentHeader);
    await this.range(s,"index",first*16,local);
    if(!prior){(s.contexts??=[]).push(context);s.contexts.sort((a,b)=>a.placement.firstRow<b.placement.firstRow?-1:1);}
    await this.backend.put([s]);
    p.rows=p.rows!.filter(r=>r!==supplied);
  }
  private async statePage(session:NativeSession,p:Package,s:NativeSource,start:number,digest:string,bytes:Uint8Array){
    const revision=s.stateFileRevision??0;
    if(p.stateFileRevision!==undefined&&p.stateFileRevision!==revision)throw Error("Native state compacted; retry collection");
    p.stateFileRevision=revision;
    const v=cbor(bytes) as Record<string,unknown>;
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
  private async range(s:NativeSource,kind:"index"|"population",start:number,bytes:Uint8Array){
    const f=s.files[kind], wanted={Start:start,End:start+bytes.length};
    if(!integer(wanted.End))throw Error("Native byte address overflow");
    const h=await this.backend.file(s.key,kind);
    try{
      // The IDB publication is authoritative across a kill between file and
      // metadata writes. Reconcile the minimal retired_bytes header on opening.
      recover(h,f.end);if(f.end===0){h.truncate(8);f.end=8;}
      const header=new Uint8Array(8);new DataView(header.buffer).setBigUint64(0,BigInt(f.retired??0),true);write(h,header,0);
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
        const prior=cbor(readFile(h,b.local,b.length)) as Record<string,unknown>[];
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
    if(await this.serial()!==session.serial)throw Error("Native cache rebased; retry collection");
    const changed=new Map<string,NativeSource>();
    for(const p of session.packages){
      const s=changed.get(p.key)??await this.backend.get(p.key);
      if(!s)throw Error("Missing native source");
      if(s.retiredBy)throw Error("Native generation retired; retry collection");
      const m=p.manifest;
      if((m.RetiredRows??0)<(s.retiredRows??0)||(m.RetiredMembers??0)<(s.retiredMembers??0))throw Error("Stale PAC retirement boundary");
      this.retire(s,m.RetiredRows??0,m.RetiredMembers??0,m.TimeRetainedFrom);
      if(s.generationSequence!==undefined&&m.GenerationSequence!==s.generationSequence)throw Error("Native generation sequence changed");
      s.generationSequence=m.GenerationSequence;
      if(!p.state||!p.stateComplete)throw Error("Incomplete native state snapshot");
      if(p.stateFileRevision!==(s.stateFileRevision??0))throw Error("Native state compacted; retry collection");
      if(s.state&&s.state.epoch!==p.state.epoch)throw Error("Native state epoch changed; rebase required");
      if(!s.state||p.state.sequence>s.state.sequence)s.state=structuredClone(p.state);
      for(const r of m.Index)resolve(s.files.index.extents,{Start:r.Start*16,End:r.End*16});
      if(missing(m.Population,s.files.population.extents).length)throw Error("Missing native population");
      if(m.TrieNodes>1&&missing([{Start:1,End:m.TrieNodes}],s.files.trie.blobs.map(b=>({Start:b.start,End:b.start+b.count}))).length)
        throw Error("Missing native trie dependency");
      if(!m.Provisional&&!m.TimeRetired&&!m.TimeTrie)throw Error("Missing sealed time trie");
      if(m.TimeTrie){
        if(m.Provisional)throw Error("Provisional time trie publication");
        const h=new DataView(m.TimeTrie.buffer,m.TimeTrie.byteOffset,m.TimeTrie.byteLength);
        if(Number(h.getBigUint64(64,true))<m.From*1000||Number(h.getBigUint64(72,true))>m.To*1000)throw Error("Time trie outside collection");
        s.timeTrie=mergeTimeWindow(s.timeTrie,m.TimeTrie,m.Generation,true,(m.RetiredRows??0)>0);
      }
      s.index=normalize([...s.index,...m.Index]);s.population=normalize([...s.population,...m.Population]);
      s.trieNodes=Math.max(s.trieNodes,m.TrieNodes);
      const old=s.packages.find(v=>v.From===m.From&&v.To===m.To);
      if(!old||old.Checkpoint<=m.Checkpoint){
        s.packages=s.packages.filter(v=>v!==old);s.packages.push(m);
      }
      changed.set(s.key,s);
    }
    // Publication orders physical generations, never response arrival times.
    // A failed or truncated replacement cannot retire committed coverage.
    const known=await this.backend.list();
    for(const current of [...changed.values()]){
      const peers=known.filter(s=>s.key!==current.key&&s.tap===current.tap&&s.digest===current.digest&&!s.retiredBy&&s.packages.length);
      if([...changed.values()].some(s=>s.key!==current.key&&s.tap===current.tap&&s.digest===current.digest))
        throw Error("Generation changed during collection; retry collection");
      for(const prior of peers){
        if(current.generationSequence===undefined){
          if(prior.generationSequence!==undefined)throw Error("Unordered native generation; retry collection");
          continue; // Legacy peers remain isolated until a sequenced response.
        }
        if(prior.generationSequence!==undefined&&prior.generationSequence>=current.generationSequence)
          throw Error("Stale or conflicting native generation; retry collection");
        if(prior.state?.epoch!==current.state?.epoch)throw Error("Native state epoch changed; rebase required");
        if(prior.state&&current.state&&prior.state.sequence>current.state.sequence)throw Error("Replacement state regressed; retry collection");
        prior.retiredBy=current.key;prior.index=[];prior.population=[];prior.packages=[];delete prior.state;
        changed.set(prior.key,prior);
      }
    }
    await this.backend.put([...changed.values()]);session.finished=true;
    for(const s of changed.values())if(!s.retiredBy)for(const kind of ["index","population"] as const){
      if(s.files[kind].end<8)continue;
      const h=await this.backend.file(s.key,kind);
      try{recover(h,s.files[kind].end);const header=new Uint8Array(8);new DataView(header.buffer).setBigUint64(0,BigInt(s.files[kind].retired??0),true);write(h,header,0);h.flush();}finally{h.close();}
    }
  }
  private retire(s:NativeSource,rows:number,members:number,first?:number){
    if(rows>0&&first!==undefined){s.timeTrie=retireTime(s.timeTrie,first);s.packages=s.packages.filter(m=>m.To*1000>first);}
    const clip=(spans:Span[],base:number)=>spans.filter(r=>r.End>base).map(r=>({Start:Math.max(r.Start,base),End:r.End}));
    for(const [kind,base] of [["index",rows*16],["population",members]] as const){
      const f=s.files[kind];f.retired=base;
      f.extents=f.extents.filter(e=>e.End>base).map(e=>({...e,local:e.local+Math.max(0,base-e.Start),Start:Math.max(e.Start,base)}));
      for(const [page,local] of Object.entries(f.pages??{}))if((Number(page)+1)*(1<<20)<=base){
        (f.free??=[]).push(local);delete f.pages![page];
      }
    }
    // Trim free physical tail pages after publication; interior slots remain
    // reusable without relocating any surviving source coordinates.
    for(const kind of ["index","population"] as const){
      const f=s.files[kind],free=new Set(f.free??[]);
      while(f.end>8&&free.delete(f.end-(1<<20)))f.end-=1<<20;
      f.free=[...free];
    }
    s.retiredRows=rows;s.retiredMembers=members;
    s.index=clip(s.index,rows);s.population=clip(s.population,members);
    s.contexts=s.contexts?.filter(c=>c.placement.firstRow+c.placement.rows>BigInt(rows));
    s.packages=s.packages.filter(m=>!m.Index.length||m.Index.some(r=>r.End>rows));
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
  async rebase(source:string):Promise<number>{
    objectParameters({source,id:""});
    const scope=source.split("/").slice(0,3).join("/");
    const control=await this.backend.control();
    const next=(control.scopes[scope]??0)+1;
    if(!integer(next)||!integer(control.serial+1))throw Error("Native rebase sequence exhausted");
    control.serial++;control.scopes[scope]=next;
    const sources=(await this.backend.list()).filter(s=>!s.retiredBy&&s.tap.split("/").slice(0,3).join("/")===scope);
    for(const s of sources){s.retiredBy=`rebase:${control.serial}`;s.index=[];s.population=[];s.packages=[];delete s.state;}
    await this.backend.put(sources,control);
    return sources.length;
  }
  async activeSelections<T extends {source:string}>(selections:T[]):Promise<T[]>{
    const live=new Set((await this.backend.list()).filter(s=>!s.retiredBy&&s.packages.length).map(s=>s.key));
    return selections.filter(s=>live.has(s.source));
  }
  async missingTime(key:string,from:number,to:number){
    const s=await this.backend.get(key);
    return missingHours(s?.retiredBy?undefined:s?.timeTrie,from,to);
  }
  async coverage(tap?:string){
    return (await this.backend.list()).filter(s=>!s.retiredBy&&matchesSource(tap??"",s.tap)).filter(s=>s.packages.length).map(s=>({
      Definition:unhex(s.digest),Generation:unhex(s.generation),Index:s.index,Population:s.population,TrieNodes:s.trieNodes,
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
    const definition=cbor(map) as PILDefinition;
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
        const snapshot=cbor(raw) as {Objects:{ID:Uint8Array;Token:number}[]};
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
  async read(key:string,kind:"index"|"population",wanted:Span){
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
  async page(key:string,from:number,to:number,after?:number,limit=512):Promise<NativePage>{
    if(!integer(from)||!integer(to)||from>=to||after!==undefined&&!integer(after)||!integer(limit)||limit<1||limit>4096)
      throw Error("Invalid native page request");
    const s=await this.backend.get(key);if(!s)throw Error("Unknown native source");
    if(s.retiredBy)throw Error("Native generation retired; recollect this window");
    const index=await this.backend.file(key,"index"),pop=await this.backend.file(key,"population");
    try{
      recover(index,s.files.index.end);recover(pop,s.files.population.end);
      const readIndex=rangeReader(index,s.files.index.extents),readPop=rangeReader(pop,s.files.population.extents);
      const at=(row:number)=>readIndex({Start:row*16,End:(row+1)*16});
      const records:Uint8Array[]=[],values:Uint8Array[]=[],offsets:number[]=[],common:(Uint8Array|undefined)[]=[];
      const bases:number[]=[];
      let total=0,last:number|undefined,done=true;
      outer: for(const context of s.contexts??[]){
        const placement=context.placement;
        if(Number(placement.to)<=from||Number(placement.from)>=to)continue;
        if(s.timeTrie&&timeLeaf(s.timeTrie,Number(placement.from))?.state!==2)continue;
        const range={Start:Number(placement.firstRow),End:Number(placement.firstRow+placement.rows)};
        if(missing([range],s.index).length)continue;
        const lower=(start:number,end:number,time:number)=>{while(start<end){const mid=start+Math.floor((end-start)/2);if(indexMillis(at(mid),Number(placement.from))<time)start=mid+1;else end=mid;}return start;};
        let first=Math.max(range.Start,after===undefined?0:after+1);
        if(first>=range.End)continue;
        first=lower(first,range.End,from);
        const end=lower(first,range.End,to);
        for(let row=first;row<end;row++){
          if(records.length>=limit||total>=8*1024*1024){done=false;break outer;}
          const raw=at(row).slice(),view=new DataView(raw.buffer,raw.byteOffset,16);
          const base=Number(placement.memberStart),start=base+view.getUint32(12,true);
          const next=row+1<range.End?at(row+1):undefined;
          const finish=base+(next?new DataView(next.buffer,next.byteOffset,16).getUint32(12,true):Number(placement.memberBytes));
          bases.push(Number(placement.from));
          if(!Number.isSafeInteger(finish)||finish<=start)throw Error("Invalid PAC local coordinates");
          const wanted={Start:start,End:finish};
          if(missing([wanted],s.population).length)throw Error("Native payload is not collected");
          const bytes=readPop(wanted).slice();common.push(context.common.length?context.common:undefined);
          records.push(raw);values.push(bytes);offsets.push(total,total+bytes.length);total+=bytes.length;last=row;
        }
      }
      const rows=new Uint8Array(records.length*16),population=new Uint8Array(total);
      records.forEach((b,i)=>rows.set(b,i*16));values.forEach((b,i)=>population.set(b,offsets[i*2]));
      return {bases:Float64Array.from(bases),rows,population,offsets:Uint32Array.from(offsets),common,after:last,done};
    }finally{index.close();pop.close();}
  }
}
function unhex(s:string){return s.match(/../g)!.map(v=>parseInt(v,16));}
function rangeReader(file:FileIO,extents:Extent[]){
  // Header/event writes can publish separate extents in the same local page.
  // Merge only contiguous known bytes at contiguous physical positions, so a
  // 20-byte window is not split merely by the original write boundaries.
  const contiguous:Extent[]=[];
  for(const e of extents){const last=contiguous.at(-1);
    if(last&&last.End===e.Start&&last.local+last.End-last.Start===e.local)last.End=e.End;
    else contiguous.push({...e});
  }
  extents=contiguous;
  let start=-1,end=-1,cached=new Uint8Array();
  return (wanted:Span):Uint8Array=>{
    if(wanted.Start>=start&&wanted.End<=end)return cached.subarray(wanted.Start-start,wanted.End-start);
    const parts=resolve(extents,wanted);
    if(parts.length===1){
      const p=parts[0],extent=extents.find(e=>e.Start<=p.Start&&e.End>=p.End)!;
      start=p.Start;end=Math.min(extent.End,Math.max(wanted.End,start+(1<<20)));
      cached=readFile(file,p.local,end-start);return cached.subarray(0,wanted.End-start);
    }
    const result=new Uint8Array(wanted.End-wanted.Start);
    for(const p of parts)result.set(readFile(file,p.local,p.End-p.Start),p.Start-wanted.Start);
    return result;
  };
}
