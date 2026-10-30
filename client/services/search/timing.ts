import type {ViewTimings} from '../pil/view.ts';

const remoteStages = [
 ['planning','Preparation','Prepare the comparison search.'],
 ['request','Request & Wait','Server wait and network time until response headers.'],
 ['response','Response & Decode','Receive and decode the search response.'],
 ['summaries','Summaries','Fetch or reuse daily summaries, decode and aggregate them.'],
 ['events','Event Data','Load requested events from local storage or complete remote hours.'],
] as const;
const localStages = [
 ['discovery','Time Trie Sync','Read local receipts, fetch missing time trie ranges and save them locally. Includes network and storage waits.'],
 ['planning','Local Planning','Read coverage, bind Definitions and prepare the search snapshot. Includes local storage waits.'],
 ['events','Collection & Matching','Collect missing event packs, read local records, match predicates and aggregate results.'],
] as const;

/** Sequential wall-clock stages, not CPU timings or sums of parallel requests. */
export function searchTimingRows(ms:number, phases?:ViewTimings) {
 if(!phases)return [];
 const stages=phases.discovery===undefined?remoteStages:localStages;
 const rows=stages.map(([key,label,hint])=>({key:key as string,label:label as string,hint:hint as string,ms:Math.max(0,phases[key]??0)}));
 const measured=rows.reduce((sum,row)=>sum+row.ms,0);
 rows.push({key:'client',label:'Other Elapsed',hint:'Elapsed time outside the measured stages, including worker queueing and tab delivery. Not a CPU measurement.',ms:Math.max(0,ms-measured)});
 return rows;
}
