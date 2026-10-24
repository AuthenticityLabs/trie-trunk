// Version-zero PAC: decoded semantic header, or a small compression wrapper.
export type PACExtension={type:number;required:boolean;version:number;bytes:Uint8Array};
export type PACHeader={digest:string;source?:string;count:number;start:number;end:number;common:Uint8Array;extensions:PACExtension[]};
export function pacHeader(bytes:Uint8Array,offset:number,maximum=0xffffffff,supported:ReadonlySet<number>=new Set([1])):PACHeader|undefined {
 if(bytes.length-offset<64)return;
 const view=new DataView(bytes.buffer,bytes.byteOffset,bytes.byteLength);

 const flags=view.getUint32(offset+4,true);
 const number=(at:number)=>{const n=view.getBigUint64(at,true);if(n>BigInt(Number.MAX_SAFE_INTEGER))throw Error('PAC length exceeds addressable range');return Number(n)};
 const size=number(offset+8),header=number(offset+16),count=number(offset+24);
 if(view.getUint32(offset,true)!==size||flags>1||size>maximum||header<64||header>size)throw Error('Invalid PAC bounds/features');
 const end=offset+size;if(end>bytes.length)return;
 let at=offset+64;const limit=offset+header;
 const integer=()=>{
  if(limit-at<2)throw Error('Truncated PAC integer');let n=view.getUint16(at,true);at+=2;
  if(n===65535){if(limit-at<4)throw Error('Truncated PAC integer');n=view.getUint32(at,true);at+=4;
   if(n===4294967295){if(limit-at<8)throw Error('Truncated PAC integer');n=number(at);at+=8;if(n<4294967295)throw Error('Nonminimal PAC integer')}
   else if(n<65535)throw Error('Nonminimal PAC integer');
  }return n;
 };
 const n=integer();if(n>Math.floor((limit-at)/10))throw Error('Invalid PAC extension count');
 const extensions:PACExtension[]=[],seen=new Set<number>();let source:string|undefined;
 for(let i=0;i<n;i++){
  if(limit-at<8)throw Error('Truncated PAC extension');
  const type=view.getUint32(at,true),version=view.getUint16(at+4,true),ef=view.getUint16(at+6,true);at+=8;
  const length=integer();if(length>limit-at||ef>1||seen.has(type))throw Error('Invalid PAC extension');
  if((ef===1&&!supported.has(type))||(supported.has(type)&&version!==0))throw Error('Unsupported required PAC extension');
  if(ef===1&&version!==0)throw Error('Unsupported required PAC extension schema');
  seen.add(type);const data=bytes.subarray(at,at+length);at+=length;
  extensions.push({type,version,required:ef===1,bytes:data});
  if(type===1)source=new TextDecoder('utf-8',{fatal:true}).decode(data);
 }
 const commonLength=integer();if(commonLength!==limit-at||Boolean(flags&1)!==Boolean(commonLength))throw Error('Invalid PAC common frame');
 if(count>0xffffffff||count>size-header)throw Error('Invalid PAC record count');
 return {digest:Array.from(bytes.subarray(offset+32,offset+64),b=>b.toString(16).padStart(2,'0')).join(''),source,count,start:limit,end,common:bytes.subarray(at,limit),extensions};
}
export function validatePACMember(common:Uint8Array,member:Uint8Array,fields:number):void {
 if(!common.length)return;
 for(let i=0;i<fields;i++){
  const shift=6-2*(i%4);
  if(((common[i>>2]>>shift)&3)!==0&&((member[i>>2]>>shift)&3)!==0)throw Error('PAC common field overridden');
 }
}

// The compressed body is a complete decoded PAC. No outer-header bytes survive.
export async function expandPAC(bytes:Uint8Array,offset:number,maximum:number,inflate?:(b:Uint8Array,n:number)=>Uint8Array):Promise<{bytes:Uint8Array;end:number}|undefined>{
 if(bytes.length-offset<8)return;
 const v=new DataView(bytes.buffer,bytes.byteOffset+offset,bytes.length-offset),size=v.getUint32(0,true),flags=v.getUint32(4,true);
 const compressed=flags===2,min=compressed?16:64;
 if(flags>2||size<min||size>maximum)throw Error('Invalid PAC compression controls');
 if(bytes.length-offset<min)return;
 const decoded=compressed?v.getUint32(8,true):Number(v.getBigUint64(8,true));
 const count=compressed?v.getUint32(12,true):Number(v.getBigUint64(24,true));
 if(decoded<64||decoded>maximum||count>decoded-64)throw Error('Invalid PAC compression bounds');
 if(bytes.length-offset<size)return;
 const raw=bytes.subarray(offset,offset+size);
 const validate=(out:Uint8Array)=>{
  if(out.length!==decoded)throw Error('PAC expansion length mismatch');
  const h=new DataView(out.buffer,out.byteOffset,out.byteLength),head=Number(h.getBigUint64(16,true));
  if(h.getUint32(0,true)!==decoded||h.getUint32(4,true)>1||h.getBigUint64(8,true)!==BigInt(decoded)||head<64||head>decoded||h.getBigUint64(24,true)!==BigInt(count))throw Error('PAC decoded header mismatch');
 };
 if(!compressed){validate(raw);return {bytes:raw,end:offset+size};}
 let out:Uint8Array;
 if(inflate)out=inflate(raw.subarray(16),decoded);
 else {
  const reader=new Blob([raw.slice(16).buffer]).stream().pipeThrough(new DecompressionStream('gzip')).getReader();
  out=new Uint8Array(decoded);let at=0;
  try{for(;;){const {done,value}=await reader.read();if(done)break;if(at+value.length>out.length)throw Error('PAC expansion exceeds bound');out.set(value,at);at+=value.length;}if(at!==out.length)throw Error('PAC expansion length mismatch');}
  catch(e){await reader.cancel().catch(()=>{});throw e;}finally{reader.releaseLock();}
 }
 validate(out);
 return {bytes:out,end:offset+size};
}
