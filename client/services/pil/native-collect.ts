import {resourceURL,timeParameters} from '../resources.ts';
import type {LiveReceiver} from "../seeds/live-pack.ts";
import {registeredPath} from "./trunk.ts";
import { nativeCacheIssue } from "../store/errors.ts";
import type { Archive, NativeInput } from "../store/protocol.ts";
import { PILStream, consume, type StoredDefinition } from "./stream.ts";
import { Codec } from "./wasm.ts";
import type { Selection } from "../events.ts";

// A native collection is one negotiated GET. The server subtracts byte coverage;
// no schema fetch, full-event conversion, or local re-interning is necessary.
// The worker selects this path after the server advertises native-ranges.
// Batch bounded decoded records so a small hour commits once, after wire completion.
export type NativeCollectOptions = {
  tap: string;
  from: string;
  to: string;
  token: string;
  signal: AbortSignal;
  codec: Codec;
  definitions: Map<string, StoredDefinition>;
  archive: Archive;
  fetcher?: typeof fetch;
  data?: "none";
  artifact?: {path:string;kind:"idx"|"pods";states?:string};
  definitionVersion?: string;
  live?:LiveReceiver;
  onAttempt?:()=>void;
  onWindow?:(to:number)=>void;
  onVersion?: (version:string)=>void;
  onProvisional?: (tap:string,from:number,to:number)=>void;
  collection?: string;
  selection?: Selection;
};
export async function collectNative(options:NativeCollectOptions):Promise<string[]> {
  for(let attempt=0;;attempt++){
    try{return await collectOnce(options);}
    catch(error){
      // Another tab can compact state while this response is in flight. Retry
      // once with newly committed coverage; discarded offsets never publish.
      if(attempt!==0||options.signal.aborted||nativeCacheIssue(error)?.code!=="native-cache-race")throw error;
    }
  }
}
async function collectOnce(options:NativeCollectOptions):Promise<string[]> {
  const {tap,from,to,token,signal,codec,definitions,archive}=options;
  if(!Number.isFinite(Date.parse(from))||(to!=="now"&&!Number.isFinite(Date.parse(to)))||(to!=="now"&&Date.parse(from)>=Date.parse(to)))
    throw Error("Invalid native collection window");
  signal.throwIfAborted();
  options.onAttempt?.();
  const session=crypto.randomUUID();
  await archive(token,{kind:"native-begin",session});
  let complete=false;
  let records:NativeInput[]=[],queuedBytes=0;
  const flush=async()=>{if(!records.length)return;await archive(token,{kind:'native-records',session,records});records=[];queuedBytes=0;};
  const provisional:{tap:string;from:number;to:number}[]=[];
  try{
    const known=await archive(token,{kind:"native-coverage",tap});
    const coverage=known.nativeCoverage??[];
    // Omitting excess spans only causes extra transfer; never overstate cached
    // coverage or use a truncated/invalid header. Keep under the server's 16KiB.
    let have="";
    for(let n=Math.min(coverage.length,128);n>0;n--){
      const candidate=btoa(JSON.stringify(coverage.slice(0,n))).replaceAll("+","-").replaceAll("/","_").replace(/=+$/,"");
      if(candidate.length<=16384){have=candidate;break;}
    }
    const query=timeParameters(from,to);
    if(options.definitionVersion)query.set("definition",options.definitionVersion);
    if(options.artifact){
      const {path,kind,states}=options.artifact;
      if(!registeredPath(path)||!path.endsWith('.'+kind)||(kind==='idx')!==(options.data==='none'))throw Error("Invalid catalog collection artifact");
      if(states)query.set("states",states);
      // State filters remain a client planner concern; sealed packs stay whole.
    }
    const url=resourceURL(options.data==='none'?'indexes':'seeds',tap,query);
    const response=await (options.fetcher??fetch)(url,{
      method:"GET",signal,cache:"no-store",
      headers:{Accept:"application/pil","PIL-Version":"2","PIL-Features":"native-ranges,native-state-pages,adaptive-integers"+(options.live?",live-pacs":""),
        "Definition-Version":options.definitionVersion??"",
        "Definition-Known":[...definitions.keys()].slice(-128).join(","),
        ...(have?{"Trunk-Coverage":have}:{}),...(token?{Authorization:`Bearer ${token}`}:{})},
    });
    const version=response.headers.get("Definition-Version");
    const windowTo=Number(response.headers.get("Trunk-Window-To"));
    if(response.ok&&options.live&&(!Number.isSafeInteger(windowTo)||windowTo<=Date.parse(from)))throw Error("Invalid collection end time");
    if(response.ok&&(response.headers.get("Trunk-Format")!=="native-pac/0"||!version||!/^[a-f0-9]{64}$/.test(version))){
      await response.body?.cancel();throw Error("Native collection was not negotiated");
    }
    await consume(response,new PILStream(codec,definitions,()=>{},()=>{},async()=>{
      throw Error("Expanded event in native response");
    },async record=>{
      signal.throwIfAborted();
      if(record.kind==='manifest'&&options.artifact){
        if(record.binding.tap!==tap||record.binding.digest!==options.definitionVersion||Boolean(record.manifest.IndexOnly)!==(options.artifact.kind==='idx'))throw Error('Catalog collection binding mismatch');
      }
      const d=record.kind==="manifest"?definitions.get(record.binding.digest):undefined;
      const bytes=record.kind==='pac'?record.bytes.byteLength+record.bounds.byteLength:record.kind==='chunk'?record.bytes.byteLength:record.kind==='manifest'?(record.manifest.TimeTrie instanceof Uint8Array?record.manifest.TimeTrie.byteLength:0)+1024:64;
      if(records.length&&(queuedBytes+bytes>4*1024*1024||records.length>=4096))await flush();
      records.push({record,...(d?{definition:{digest:d.digest,store:d.store,pil:d.pil}}:{})});queuedBytes+=bytes;
      if(queuedBytes>=4*1024*1024)await flush();
      if(record.kind==="manifest"&&record.manifest.Provisional)provisional.push({tap:record.binding.tap,from:Number(record.manifest.From)*1000,to:Number(record.manifest.To)*1000});
    },undefined,undefined,options.live),signal);
    signal.throwIfAborted();
    const saved=await archive(token,{kind:"native-finish",session,records,collection:options.collection,selection:options.selection});
    complete=true;options.onWindow?.(windowTo);options.onVersion?.(version!);for(const p of provisional)options.onProvisional?.(p.tap,p.from,p.to);return saved.nativeSources??[];
  }finally{
    if(!complete)await archive(token,{kind:"native-abort",session}).catch(()=>{});
  }
}
