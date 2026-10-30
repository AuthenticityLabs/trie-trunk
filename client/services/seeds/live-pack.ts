import type {Codec} from '../pil/wasm.ts';
import type {Binding} from '../pil/metadata.ts';
import type {PACHeader} from '../pil/pac.ts';
import {timeFromLowWord} from '../seed-identity.ts';
export type LiveWindow={binding:Binding;generation:string;sequence:number;sealed:number;retained:number};
export type LivePack={binding:Binding;hour:number;rows:Uint8Array;common:Uint8Array;members:Uint8Array[]};
export type LiveReceiver={window(value:LiveWindow):void;pack(value:LivePack):Promise<void>};
// Seed semantics stay above the portable PAC codec. Member/common buffers are
// borrowed from one owned pack; no event objects or expanded records are built.
export function livePack(codec:Codec,handle:number,binding:Binding,raw:Uint8Array,pac:PACHeader):LivePack {
 const hourBytes=pac.headers['seed.hour'],rows=pac.headers['seed.rows'];
 if(!hourBytes||hourBytes.length!==8||!rows||rows.length!==pac.count*6)throw Error('Invalid live Seed Pack headers');
 const hour=Number(new DataView(hourBytes.buffer,hourBytes.byteOffset,8).getBigUint64(0,true));
 if(!Number.isSafeInteger(hour)||hour<0||hour%3600000||hour>=2**48)throw Error('Invalid live Seed Pack hour');
 const index=new DataView(rows.buffer,rows.byteOffset,rows.byteLength);let previous=-1;
 for(let i=0;i<pac.count;i++){const time=timeFromLowWord(index.getUint32(i*6,true),hour);if(time<hour||time>=hour+3600000||time<previous||index.getUint16(i*6+4,true)!==0)throw Error('Invalid live Seed Pack row');previous=time;}
 codec.upload(raw);const bounds=codec.pacStructural(handle,0,raw.length,pac.count);
 const members=Array.from({length:pac.count},(_,i)=>raw.subarray(bounds[i*2],bounds[i*2+1]));
 return {binding,hour,rows,common:pac.common,members};
}
