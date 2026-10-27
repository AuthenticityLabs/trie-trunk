// Positional UTC YYYY/DDD/HH directory. Widths are fixed per file and may grow.
// Missing storage and a complete hour with zero records remain distinct.
export const HOUR=3_600_000,HEADER=64,DAY_BYTES=24*16,YEAR_BYTES=366*DAY_BYTES+366*24;
const magic=new TextEncoder().encode('TTTIME00');
const uint=(b:Uint8Array,at:number,w:number)=>{let n=0;for(let i=w-1;i>=0;i--)n=n*256+b[at+i];if(!Number.isSafeInteger(n))throw Error('Unsafe trie address');return n;};
const put=(b:Uint8Array,at:number,w:number,n:number)=>{if(!Number.isSafeInteger(n)||n<0)throw Error('Invalid trie address');for(let i=0;i<w;i++){b[at+i]=n%256;n=Math.floor(n/256);}if(n)throw Error('Trie width overflow');};
const year=(n:number)=>new Date(n).getUTCFullYear();
function header(raw:Uint8Array){
 if(raw.length<HEADER||magic.some((v,i)=>v!==raw[i])||raw[46]||raw[47])throw Error('Invalid time trie header');
 const widths=Array.from(raw.subarray(40,46));widths.forEach((w,i)=>{if(w<(i===0?4:i<3?5:2)||w>8)throw Error('Invalid time trie widths');});
 const first=uint(raw,8,8),origin=uint(raw,16,8),tail=uint(raw,24,8),sealed=uint(raw,32,8);
 if(first%HOUR||origin%HOUR||sealed%HOUR||first>sealed)throw Error('Invalid time trie bounds');
 const leaf=widths.slice(0,4).reduce((a,b)=>a+b,0),yb=366*24*(leaf+1);
 return {first,origin,tail,sealed,widths,leaf,yb,baseYear:year(first)};
}
type Header=ReturnType<typeof header>;
// The low two bits distinguish missing, metadata-only, and committed events.
// Search is independent: metadata + search can exist without any .sods bytes.
export const TIME_KNOWN=1,TIME_EVENTS=2,TIME_SEARCH=4;
function directoryHeader(raw:Uint8Array){
 const h=header(raw),years=year(Math.max(h.first,h.sealed-1))-h.baseYear+1;
 if(raw.length!==HEADER+years*h.yb)throw Error('Invalid time trie extent; refresh the local cache');
 return h;
}
function positions(h:Header,ms:number){const d=new Date(ms),y=HEADER+(d.getUTCFullYear()-h.baseYear)*h.yb;
 const ordinal=Math.floor((Date.UTC(d.getUTCFullYear(),d.getUTCMonth(),d.getUTCDate())-Date.UTC(d.getUTCFullYear(),0,1))/86400000),n=ordinal*24+d.getUTCHours();
 return {hour:y+n*h.leaf,state:y+366*24*h.leaf+n};}
function values(raw:Uint8Array,at:number,h:Header){const v:number[]=[];for(const w of h.widths.slice(0,4)){v.push(uint(raw,at,w));at+=w;}return v;}
function write(raw:Uint8Array,h:Header,ms:number,v:number[],state:number){const p=positions(h,ms);let at=p.hour;v.forEach((n,i)=>{put(raw,at,h.widths[i],n);at+=h.widths[i];});raw[p.state]=state;}
export function timeLeaf(raw:Uint8Array|undefined,ms:number){
 if(!raw)return;const h=directoryHeader(raw);ms=Math.floor(ms/HOUR)*HOUR;if(ms<h.first||ms>=h.sealed)return;
 const p=positions(h,ms);if(p.state>=raw.length)return;const flags=raw[p.state],state=flags&3;if(!flags)return;if(!state||state===3||(flags&~7))throw Error('Reserved trie state');
 const [start,pods,sods,count]=values(raw,p.hour,h);return {state,flags,start,end:start+(count?count+1:0),pods,sods,count,segments:count?1:0};
}
export function missingHours(raw:Uint8Array|undefined,from:number,to:number,required:number=TIME_EVENTS){
 if(required!==TIME_EVENTS&&required!==TIME_SEARCH&&required!==(TIME_EVENTS|TIME_SEARCH))throw Error('Invalid replication requirement');
 if(!Number.isSafeInteger(from)||!Number.isSafeInteger(to)||from<0||from>=to)throw Error('Invalid time window');
 const out:{from:number;to:number}[]=[];for(let at=Math.floor(from/HOUR)*HOUR;at<to;at+=HOUR){const leaf=timeLeaf(raw,at);if(leaf&&(leaf.flags&required)===required)continue;const last=out.at(-1);if(last?.to===at)last.to=at+HOUR;else out.push({from:at,to:at+HOUR});}return out;
}
function allocate(template:Uint8Array,first:number,sealed:number,widths:number[]){const h=template.slice(0,HEADER);put(h,8,8,first);put(h,32,8,sealed);h.set(widths,40);const parsed=header(h),last=year(Math.max(first,sealed-1));const out=new Uint8Array(HEADER+(last-parsed.baseYear+1)*parsed.yb);out.set(h);return out;}
function copyHours(from:Uint8Array,to:Uint8Array){const a=directoryHeader(from),b=directoryHeader(to);for(let ms=Math.max(a.first,b.first);ms<Math.min(a.sealed,b.sealed);ms+=HOUR){const p=positions(a,ms),state=from[p.state];if(state)write(to,b,ms,values(from,p.hour,a),state);}}
export function retireTime(raw:Uint8Array|undefined,first:number){if(!raw)return;const old=directoryHeader(raw);if(first<=old.first)return raw;if(!Number.isSafeInteger(first)||first%HOUR)throw Error('Invalid retirement');const out=allocate(raw,first,Math.max(first,old.sealed),old.widths);copyHours(raw,out);return out;}
export function mergeTimeWindow(previous:Uint8Array|undefined,patch:Uint8Array,source:Uint8Array,complete:boolean,retained=false,reuse=false){
 const h=header(patch);if(patch.length<80||source.length!==16||source.some((v,i)=>v!==patch[48+i]))throw Error('Time trie source mismatch');
 const from=uint(patch,64,8),to=uint(patch,72,8),stride=h.leaf+1;
 if(from<h.first||from%HOUR||to%HOUR||from>=to||to>h.sealed||patch.length!==80+(to-from)/HOUR*stride)throw Error('Invalid time trie patch');
 const old=previous?directoryHeader(previous):undefined;
 if(previous&&(source.some((v,i)=>v!==previous[48+i])||old!.origin!==h.origin))throw Error('Time trie reference binding changed');
 if(old&&retained&&h.first<old.first)throw Error('Stale time trie retirement');
 const first=old&&!retained?Math.min(old.first,h.first):h.first,sealed=Math.max(old?.sealed??0,h.sealed),widths=h.widths.map((w,i)=>Math.max(w,old?.widths[i]??w));
 // A transaction owns a detached snapshot. When its layout is unchanged,
 // update that working copy instead of copying every prior hour for each PAC.
 const reusable=reuse&&previous&&old!.first===first&&old!.sealed===sealed&&widths.every((w,i)=>w===old!.widths[i]);
 const out=reusable?previous:allocate(patch,first,sealed,widths),target=header(out);put(out,24,8,Math.max(h.tail,old?.tail??0));if(previous&&!reusable)copyHours(previous,out);
 for(let ms=from,at=80;ms<to;ms+=HOUR,at+=stride){const state=patch[at+h.leaf];if(state===0)continue;if(state!==2)throw Error('Unsealed source hour');
  const v=values(patch,at,h),[start,pods,sods,count]=v,end=start+(count?count+1:0);if((count&&ms<h.origin)||end>h.tail)throw Error('Invalid time trie extent');
  const prior=timeLeaf(previous,ms);if(prior&&(prior.state===2||(prior.flags&TIME_SEARCH))&&(prior.start!==start||prior.pods!==pods||prior.sods!==sods||prior.count!==count))throw Error('Sealed trie leaf changed');
  write(out,target,ms,v,(complete||prior?.state===2?2:1)|(prior?.flags??0)&TIME_SEARCH);
 }return out;
}

/** Call only after verified Tantivy files are flushed, within the branch commit.
 * Returns a private snapshot: does not publish or mark event bytes available. */
export function markTimeSearch(raw:Uint8Array,source:Uint8Array,from:number,to:number,present=true):Uint8Array {
 const h=directoryHeader(raw);
 if(source.length!==16||source.some((b,i)=>b!==raw[48+i]))throw Error('Time trie source mismatch');
 if(!Number.isSafeInteger(from)||!Number.isSafeInteger(to)||from%HOUR||to%HOUR||from>=to||from<h.first||to>h.sealed)throw Error('Invalid search index window');
 const out=raw.slice();
 for(let at=from;at<to;at+=HOUR){
  const leaf=timeLeaf(raw,at);if(!leaf)throw Error('Search index has no time trie leaf');
  const p=positions(h,at);out[p.state]=present?leaf.flags|TIME_SEARCH:leaf.flags&~TIME_SEARCH;
 }
 return out;
}
