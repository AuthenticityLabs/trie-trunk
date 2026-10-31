import {resourceURL,timeParameters} from '../resources.ts';
import type {Archive} from '../store/protocol.ts';
import type {TrunkBootstrap} from './trunk.ts';
import {branchTargets} from './time-window.ts';
import {TIME_SUM} from '../store/time-trie.ts';
import {readSummaryPacks,summaryBatchLimit} from './summary-packs.ts';
import {decodeSummaryDay} from './summaries.ts';
const DAY=86_400_000;
/** Persist closed-day summaries. Return unfinished-day snapshots in memory;
 * they never acquire durable completion bits. */
export async function replicateSummaryRanges(trunk:TrunkBootstrap,scope:string,from:number,to:number,token:string,signal:AbortSignal,archive:Archive,fetcher:typeof fetch=fetch){
 const transient:{tap:string;digest:string;generation:string;day:number;bytes:Uint8Array}[]=[];
 for(const target of branchTargets(trunk,scope)){
  signal.throwIfAborted();const {file,tap,definition}=target,path=file.path.slice(0,-3)+'sum';
  if(!trunk.catalog.files.some(f=>f.path===path&&f.kind==='sum'&&f.definition===definition.digest))continue;
  const receipt=(await archive(token,{kind:'time-read',query:{tap,digest:definition.digest,from,to}})).time;
  if(!receipt?.generation)continue;
  const query={tap,digest:definition.digest,generation:receipt.generation};
  const days=[...new Set(receipt.rows.filter(r=>r.count&&!(r.flags&TIME_SUM)).map(r=>Math.floor(r.time/DAY)*DAY))].sort((a,b)=>a-b);
  const missing:number[]=[];
  for(const day of days){
   const saved=(await archive(token,{kind:'summary-read',query:{...query,day}})).summaryBytes;
   if(saved)await archive(token,{kind:'summary-save',input:{...query,day,serial:receipt.serial,bytes:saved}});
   else missing.push(day);
  }
  const ranges:{from:number;to:number}[]=[];
  for(const day of missing){const last=ranges.at(-1);if(last?.to===day&&(last.to-last.from)/DAY<summaryBatchLimit)last.to+=DAY;else ranges.push({from:day,to:day+DAY});}
  for(const range of ranges){
   signal.throwIfAborted();
   const response=await fetcher(resourceURL('summaries',tap,new URLSearchParams([...timeParameters(range.from,range.to),['definition',definition.digest]])),{signal,cache:'no-store',headers:{Accept:'application/pil',...(token?{Authorization:`Bearer ${token}`}:{})}});
   if(!response.ok)throw Error(`Summary range request failed (${response.status})`);
   const h=response.headers,count=Number(h.get('Trunk-Pack-Count'));
   if(h.get('Trunk-Format')!=='summary-packs/0'||h.get('Trunk-Path')!==path||h.get('Trunk-Source')!==tap||h.get('Definition-Version')!==definition.digest||h.get('Trunk-Generation')!==receipt.generation||Number(h.get('Trunk-Generation-Sequence'))!==receipt.sequence||!h.has('Trunk-Pack-Count')||!Number.isInteger(count)||count<0||count>(range.to-range.from)/DAY)throw Error('Summary range binding mismatch');
   // Commit each validated PAC independently; later truncation leaves only
   // complete packs usable. No receipt is inferred from the requested range.
   let prior=-1;
   await readSummaryPacks(response,count,128*1024*1024+32,signal,async(_,bytes)=>{
    const decoded=await decodeSummaryDay(bytes);
    if(decoded.day<range.from||decoded.day>=range.to||decoded.day<=prior)throw Error('Summary outside requested day range');
    prior=decoded.day;signal.throwIfAborted();
    if(decoded.sealed)await archive(token,{kind:'summary-save',input:{...query,day:decoded.day,serial:receipt.serial,bytes}});
    else transient.push({...query,day:decoded.day,bytes});
   });
  }
 }
 return transient;
}
