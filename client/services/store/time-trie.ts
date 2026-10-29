// Server leaves contain addresses and counts. The sparse client copy appends
// one byte of file-completion bits to each received leaf. Allocation ranges
// identify which leaves exist, including an empty leaf with a zero receipt byte.
export const HOUR=3_600_000,HEADER=64,DAY_BYTES=24*17,YEAR_BYTES=366*DAY_BYTES;
export const TIME_NONE=0,TIME_SUM=1,TIME_INDEX=2,TIME_STATE=4,TIME_INVERTED=8,TIME_SODS=16;
const ALL_FILES=TIME_SUM|TIME_INDEX|TIME_STATE|TIME_INVERTED|TIME_SODS;
export type TimeRange={from:number;to:number};
export type ClientTimeTrie={bytes:Uint8Array;ranges:TimeRange[]};
const magic=new TextEncoder().encode('TTTIME00');
const uint=(b:Uint8Array,at:number,w:number)=>{let n=0;for(let i=w-1;i>=0;i--)n=n*256+b[at+i];if(!Number.isSafeInteger(n))throw Error('Unsafe trie address');return n;};
const put=(b:Uint8Array,at:number,w:number,n:number)=>{if(!Number.isSafeInteger(n)||n<0)throw Error('Invalid trie address');for(let i=0;i<w;i++){b[at+i]=n%256;n=Math.floor(n/256);}if(n)throw Error('Trie width overflow');};
const year=(n:number)=>new Date(n).getUTCFullYear();
function header(raw:Uint8Array,client=false){
 if(raw.length<HEADER||magic.some((v,i)=>v!==raw[i])||raw[46]!==Number(client)||raw[47])throw Error('Invalid time trie header');
 const widths=Array.from(raw.subarray(40,46));widths.forEach((w,i)=>{if(w<(i===0?4:i<3?5:2)||w>8)throw Error('Invalid time trie widths');});
 const first=uint(raw,8,8),origin=uint(raw,16,8),tail=uint(raw,24,8),sealed=uint(raw,32,8);
 if(first%HOUR||origin%HOUR||sealed%HOUR||origin>first||first>sealed)throw Error('Invalid time trie bounds');
 const leaf=widths.slice(0,4).reduce((a,b)=>a+b,0),stride=leaf+Number(client),yb=366*24*stride;
 return {first,origin,tail,sealed,widths,leaf,stride,yb,baseYear:year(first)};
}
type Header=ReturnType<typeof header>;
function directoryHeader(tree:ClientTimeTrie){
 const h=header(tree.bytes,true),years=year(Math.max(h.first,h.sealed-1))-h.baseYear+1;
 if(tree.bytes.length!==HEADER+years*h.yb)throw Error('Invalid time trie extent; refresh the local cache');
 let end=h.first;
 for(const r of tree.ranges){if(!Number.isSafeInteger(r.from)||!Number.isSafeInteger(r.to)||r.from%HOUR||r.to%HOUR||r.from<end||r.from>=r.to||r.to>h.sealed)throw Error('Invalid sparse time range');end=r.to;}
 return h;
}
function positions(h:Header,ms:number){const d=new Date(ms),y=HEADER+(d.getUTCFullYear()-h.baseYear)*h.yb;
 const ordinal=Math.floor((Date.UTC(d.getUTCFullYear(),d.getUTCMonth(),d.getUTCDate())-Date.UTC(d.getUTCFullYear(),0,1))/86400000),n=ordinal*24+d.getUTCHours();
 return {hour:y+n*h.stride,state:y+n*h.stride+h.leaf};}
function values(raw:Uint8Array,at:number,h:Header){const v:number[]=[];for(const w of h.widths.slice(0,4)){v.push(uint(raw,at,w));at+=w;}return v;}
function write(raw:Uint8Array,h:Header,ms:number,v:number[],flags:number){const p=positions(h,ms);let at=p.hour;v.forEach((n,i)=>{put(raw,at,h.widths[i],n);at+=h.widths[i];});raw[p.state]=flags;}
function contains(ranges:TimeRange[],ms:number){let lo=0,hi=ranges.length;while(lo<hi){const mid=(lo+hi)>>>1;if(ranges[mid].to<=ms)lo=mid+1;else hi=mid;}return !!ranges[lo]&&ranges[lo].from<=ms;}
function union(ranges:TimeRange[]):TimeRange[]{const out:TimeRange[]=[];for(const r of [...ranges].sort((a,b)=>a.from-b.from)){const last=out.at(-1);if(last&&r.from<=last.to)last.to=Math.max(last.to,r.to);else out.push({...r});}return out;}
function leaf(tree:ClientTimeTrie,h:Header,ms:number){
 if(ms<h.first||ms>=h.sealed||!contains(tree.ranges,ms))return;
 const p=positions(h,ms),flags=tree.bytes[p.state];if(flags&~ALL_FILES)throw Error('Reserved replication bit');
 const [start,pods,sods,count]=values(tree.bytes,p.hour,h);return {flags,start,end:start+(count?count+1:0),pods,sods,count,segments:count?1:0};
}
export function timeLeaf(tree:ClientTimeTrie|undefined,ms:number){if(tree)return leaf(tree,directoryHeader(tree),Math.floor(ms/HOUR)*HOUR);}
export function missingHours(tree:ClientTimeTrie|undefined,from:number,to:number,required:number=TIME_SODS){
 if(!Number.isInteger(required)||required<0||(required&~ALL_FILES))throw Error('Invalid replication requirement');
 if(!Number.isSafeInteger(from)||!Number.isSafeInteger(to)||from<0||from>=to)throw Error('Invalid time window');
 const h=tree?directoryHeader(tree):undefined,out:TimeRange[]=[];
 for(let at=Math.floor(from/HOUR)*HOUR;at<to;at+=HOUR){const item=tree&&leaf(tree,h!,at);if(item&&(item.flags&required)===required)continue;const last=out.at(-1);if(last?.to===at)last.to=at+HOUR;else out.push({from:at,to:at+HOUR});}return out;
}
function allocate(template:Uint8Array,first:number,sealed:number,widths:number[]){const raw=template.slice(0,HEADER);raw[46]=1;put(raw,8,8,first);put(raw,32,8,sealed);raw.set(widths,40);const h=header(raw,true),out=new Uint8Array(HEADER+(year(Math.max(first,sealed-1))-h.baseYear+1)*h.yb);out.set(raw);return out;}
function copyHours(from:ClientTimeTrie,to:Uint8Array){const a=directoryHeader(from),b=header(to,true);for(const r of from.ranges)for(let ms=Math.max(r.from,b.first);ms<Math.min(r.to,b.sealed);ms+=HOUR){const p=positions(a,ms);write(to,b,ms,values(from.bytes,p.hour,a),from.bytes[p.state]);}}
/** Byte extents for sparse file publication; gaps and unused calendar days are skipped. */
export function* timeFileRanges(tree:ClientTimeTrie){
 const h=directoryHeader(tree);yield {at:0,bytes:tree.bytes.subarray(0,HEADER)};
 for(const range of tree.ranges)for(let from=range.from;from<range.to;){
  const to=Math.min(range.to,Date.UTC(year(from)+1,0,1)),at=positions(h,from).hour;
  yield {at,bytes:tree.bytes.subarray(at,at+(to-from)/HOUR*h.stride)};from=to;
 }
}
export function retireTime(tree:ClientTimeTrie|undefined,first:number):ClientTimeTrie|undefined {
 if(!tree)return;const old=directoryHeader(tree);if(first<=old.first)return tree;if(!Number.isSafeInteger(first)||first%HOUR)throw Error('Invalid retirement');
 const bytes=allocate(tree.bytes,first,Math.max(first,old.sealed),old.widths);copyHours(tree,bytes);
 return {bytes,ranges:tree.ranges.filter(r=>r.to>first).map(r=>({from:Math.max(r.from,first),to:r.to}))};
}
export function mergeTimeWindow(previous:ClientTimeTrie|undefined,patch:Uint8Array,source:Uint8Array,completed:number=TIME_NONE,retained=false,reuse=false):ClientTimeTrie {
 const h=header(patch);if(patch.length<80||source.length!==16||source.some((v,i)=>v!==patch[48+i]))throw Error('Time trie source mismatch');
 if(!Number.isInteger(completed)||completed<0||(completed&~ALL_FILES))throw Error('Invalid replication completion');
 const from=uint(patch,64,8),to=uint(patch,72,8);
 if(from%HOUR||to%HOUR||from>=to||to>h.sealed||patch.length!==80+(to-from)/HOUR*h.leaf)throw Error('Invalid time trie patch');
 const old=previous?directoryHeader(previous):undefined;
 if(previous&&(source.some((v,i)=>v!==previous.bytes[48+i])||old!.origin!==h.origin))throw Error('Time trie reference binding changed');
 if(old&&retained&&h.first<old.first)throw Error('Stale time trie retirement');
 // Validate all received leaves before mutating a transaction-owned copy.
 for(let ms=from,at=80;ms<to;ms+=HOUR,at+=h.leaf){
  if(ms<h.first)continue;
  const [start,pods,sods,count]=values(patch,at,h),end=start+(count?count+1:0);
  if((count&&ms<h.origin)||end>h.tail)throw Error('Invalid time trie extent');
  const prior=previous&&leaf(previous,old!,ms);
  if(prior&&(prior.start!==start||prior.pods!==pods||prior.sods!==sods||prior.count!==count))throw Error('Sealed trie leaf changed');
 }
 const first=old&&!retained?Math.min(old.first,h.first):h.first,sealed=Math.max(old?.sealed??0,h.sealed),widths=h.widths.map((w,i)=>Math.max(w,old?.widths[i]??w));
 const reusable=reuse&&previous&&old!.first===first&&old!.sealed===sealed&&widths.every((w,i)=>w===old!.widths[i]);
 const bytes=reusable?previous.bytes:allocate(patch,first,sealed,widths),target=header(bytes,true);
 put(bytes,24,8,Math.max(h.tail,old?.tail??0));if(previous&&!reusable)copyHours(previous,bytes);
 for(let ms=Math.max(from,h.first),at=80+(Math.max(from,h.first)-from)/HOUR*h.leaf;ms<to;ms+=HOUR,at+=h.leaf){
  const prior=previous&&leaf(previous,old!,ms);write(bytes,target,ms,values(patch,at,h),(prior?.flags??0)|completed);
 }
 const ranges=union([...(previous?.ranges??[]).flatMap(r=>r.to<=first?[]:[{from:Math.max(first,r.from),to:r.to}]),...(to>Math.max(from,h.first)?[{from:Math.max(from,h.first),to}]:[])]);
 if(reusable){previous.ranges=ranges;return previous;}
 return {bytes,ranges};
}
/** Publish only after the specified files are validated and durably committed. */
export function markTimeFiles(tree:ClientTimeTrie,source:Uint8Array,from:number,to:number,files:number,present=true):ClientTimeTrie {
 const h=directoryHeader(tree);
 if(source.length!==16||source.some((b,i)=>b!==tree.bytes[48+i]))throw Error('Time trie source mismatch');
 if(!Number.isInteger(files)||files<=0||(files&~ALL_FILES))throw Error('Invalid replication requirement');
 if(!Number.isSafeInteger(from)||!Number.isSafeInteger(to)||from%HOUR||to%HOUR||from>=to||from<h.first||to>h.sealed)throw Error('Invalid file completion window');
 const bytes=tree.bytes.slice();
 for(let at=from;at<to;at+=HOUR){const item=leaf(tree,h,at);if(!item)throw Error('No time trie leaf');const p=positions(h,at);bytes[p.state]=present?item.flags|files:item.flags&~files;}
 return {bytes,ranges:tree.ranges.map(r=>({...r}))};
}
