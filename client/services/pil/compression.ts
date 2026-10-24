import { span, width, resourceBinding, type PILDefinition } from './layout.ts';

export type Inflater = (bytes:Uint8Array,length:number)=>Uint8Array;
export const compressionFeature = 'event-gzip-v1';
const maximum = 64 * 1024 * 1024;
export function identityKeys(def:PILDefinition):number[]{
 const roles=resourceBinding(def)?.roles;
 if(!roles)return [];
 return [roles.event_id,roles.id,roles.revision_id].filter((v):v is string=>!!v).map(name=>def.fields.findIndex(f=>f.name===name));
}
export function compressedMarker(raw:Uint8Array,def:PILDefinition):boolean{
 const event=identityKeys(def)[0],mask=Math.ceil(def.fields.length/4);
 if(event===undefined||event<0||raw.length<mask)return false;
 for(let i=0;i<mask;i++)if(raw[i]!== (i===Math.floor(event/4)?1<<(6-2*(event%4)):0))return false;
 return true;
}
export function envelopeLength(raw:Uint8Array,def:PILDefinition):number|undefined{
 const m=Math.ceil(def.fields.length/4),h=m+16;
 if(raw.length<h)return;
 if(!compressedMarker(raw,def)||raw[m]!==80||raw[m+1]!==67||raw[m+2]!==1||raw[m+3]!==1)throw Error('Unsupported compressed event');
 const view=new DataView(raw.buffer,raw.byteOffset,raw.length),id=view.getUint32(m+4,true),plain=view.getUint32(m+8,true),stored=view.getUint32(m+12,true);
 if(!id||!plain||!stored||id+plain>maximum||h+id+stored>maximum)throw Error('Invalid compressed event lengths');
 return h+id+stored;
}
function appendExtended(out:number[],n:number){if(n<65535)out.push(n&255,n>>>8);else out.push(255,255,n&255,(n>>>8)&255,(n>>>16)&255,n>>>24);}
// Copies encoded primitive spans. It does not decode values into JS objects.
export function mergeFrames(parts:Uint8Array[],def:PILDefinition,omit=new Set<number>()):Uint8Array{
 const m=Math.ceil(def.fields.length/4),mask=new Uint8Array(m);
 const fields=def.fields.map((f,i)=>{
  if(omit.has(i))return {state:0,bytes:new Uint8Array(),bits:0};
  const found=parts.map(raw=>({raw,s:span(raw,def,i)})).filter(v=>v.s.state!==0);
  if(found.length>1)throw Error('Overlapping compressed event fields');
  const v=found[0],state=v?.s.state??0;
  const bytes=v? v.raw.subarray(v.s.start,v.s.end):new Uint8Array();
  if(v&&(v.s.end>v.raw.length||v.s.start>v.s.end))throw Error('Invalid encoded field');
  mask[Math.floor(i/4)]|=state<<(6-2*(i%4));
  const adaptive=!f.values?.length&&['16','32'].includes(def.adaptive_integers??'');
  const bits=state===2&&adaptive?(/^[IU](32|64)$/.test(f.type)?2:def.adaptive_integers==='16'&&/^[IU]16$/.test(f.type)?1:0):0;
  if(state===2&&(![1,2,4,8,16].includes(bytes.length)||bytes.length>width(f)))throw Error('Invalid direct field width');
  return {state,bytes,bits};
 });
 const plane=new Uint8Array(Math.ceil(fields.reduce((n,f)=>n+f.bits,0)/8));let bit=0;
 const offsets:number[]=[];let end=0;
 for(const f of fields){
  if(f.bits){const code=Math.log2(f.bytes.length);for(let b=f.bits-1;b>=0;b--){plane[Math.floor(bit/8)]|=((code>>b)&1)<<(7-bit%8);bit++;}}
  if(f.state===3){end+=f.bytes.length;appendExtended(offsets,end);}
 }
 const length=m+plane.length+offsets.length+fields.reduce((n,f)=>n+f.bytes.length,0);
 if(length>maximum)throw Error('Expanded event exceeds limit');
 const out=new Uint8Array(length);out.set(mask);out.set(plane,m);let at=m+plane.length;
 for(const f of fields)if(f.state===2){out.set(f.bytes,at);at+=f.bytes.length;}
 out.set(offsets,at);at+=offsets.length;
 for(const f of fields)if(f.state===3){out.set(f.bytes,at);at+=f.bytes.length;}
 return out;
}
export async function expandEvent(raw:Uint8Array,def:PILDefinition,validate?:(b:Uint8Array)=>void,inflate?:Inflater):Promise<Uint8Array>{
 if(!compressedMarker(raw,def))return raw;
 if(envelopeLength(raw,def)!==raw.length)throw Error('Compressed event boundary mismatch');
 const m=Math.ceil(def.fields.length/4),h=m+16,view=new DataView(raw.buffer,raw.byteOffset,raw.length);
 const idLength=view.getUint32(m+4,true),plainLength=view.getUint32(m+8,true);
 const identity=raw.subarray(h,h+idLength);validate?.(identity);
 let body:Uint8Array;
 if(inflate){body=inflate(raw.subarray(h+idLength),plainLength);}
 else {
  const reader=new Blob([raw.slice(h+idLength).buffer]).stream().pipeThrough(new DecompressionStream('gzip')).getReader();
  body=new Uint8Array(plainLength);let at=0;
  try{for(;;){const {done,value}=await reader.read();if(done)break;if(at+value.length>body.length)throw Error('Expanded event exceeds declared length');body.set(value,at);at+=value.length;}}
  catch(e){await reader.cancel().catch(()=>{});throw e;}finally{reader.releaseLock();}
  if(at!==plainLength)throw Error('Expanded event length mismatch');
 }
 if(body.length!==plainLength)throw Error('Expanded event length mismatch');validate?.(body);
 const ids=new Set(identityKeys(def));
 for(let i=0;i<def.fields.length;i++){
  if(span(ids.has(i)?body:identity,def,i).state!==0)throw Error('Compressed identity/body overlap');
 }
 const event=identityKeys(def)[0],s=span(identity,def,event),id=identity.subarray(s.start,s.end);
 if(s.state!==2||id.length!==16||id[6]>>4!==7||id[8]>>6!==2)throw Error('Invalid compressed event identity');
 const full=mergeFrames([identity,body],def);validate?.(full);return full;
}