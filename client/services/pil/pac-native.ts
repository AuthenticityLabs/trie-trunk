import {timeFromLowWord,seedKey} from '../seed-identity.ts';
export const INDEX_ROW_BYTES=6;
import type { PACHeader } from './pac.ts';
export const nativePAC = 0x5454524b;
export type PACPlacement = {
 source: string; trie: string; block: number; pageSize: number;
 from: bigint; to: bigint; firstRow: bigint; rows: bigint;
 memberStart: bigint; memberBytes: bigint; pages: { row: bigint; page: bigint }[];
};
const hex=(b:Uint8Array)=>Array.from(b,n=>n.toString(16).padStart(2,'0')).join('');
/** Explicit opt-in. Generic event readers reject this required native profile. */
export function pacPlacement(pac:PACHeader):PACPlacement {
 const e=pac.extensions.find(e=>e.type===nativePAC);
 if(!e?.required||e.version!==0||e.bytes.length<106)throw Error('Missing native PAC binding');
 const b=e.bytes,v=new DataView(b.buffer,b.byteOffset,b.byteLength);
 const p:PACPlacement={source:hex(b.subarray(0,16)),trie:hex(b.subarray(16,48)),block:v.getUint32(48,true),pageSize:v.getUint32(52,true),
 from:v.getBigInt64(56,true),to:v.getBigInt64(64,true),firstRow:v.getBigUint64(72,true),rows:v.getBigUint64(80,true),memberStart:v.getBigUint64(88,true),memberBytes:v.getBigUint64(96,true),pages:[]};
 let at=104,n=BigInt(v.getUint16(at,true));at+=2;
 if(n===65535n){if(b.length-at<4)throw Error('Truncated PAC page count');n=BigInt(v.getUint32(at,true));at+=4;
  if(n===4294967295n){if(b.length-at<8)throw Error('Truncated PAC page count');n=v.getBigUint64(at,true);at+=8;if(n<4294967295n)throw Error('Nonminimal PAC page count')}
  else if(n<65535n)throw Error('Nonminimal PAC page count');
 }
 if(n*16n!==BigInt(b.length-at))throw Error('Invalid PAC page count');
 const max=0xffffffffffffffffn;
 if(!p.pageSize||p.from>=p.to||p.rows!==BigInt(pac.count)||p.firstRow+p.rows>max||p.memberStart+p.memberBytes>max||((p.rows===0n)!==(p.memberBytes===0n)))throw Error('Invalid PAC placement bounds');
 const last=(p.memberStart+p.memberBytes-1n)/BigInt(p.pageSize);
 for(let i=0n;i<n;i++){
  const row=v.getBigUint64(at,true),page=v.getBigUint64(at+8,true);at+=16;
  const prev=p.pages.at(-1);
  if(row>=p.rows||page>last||(prev&&(row<=prev.row||page<=prev.page)))throw Error('Invalid PAC page transition');
  p.pages.push({row,page});
 }
 if(p.rows===0n?p.pages.length!==0:!p.pages.length||p.pages[0].row!==0n||p.pages[0].page!==p.memberStart/BigInt(p.pageSize))throw Error('Invalid PAC starting page');
 return p;
}
/** Immutable index rows are identical on both ends. Validate; do not rewrite. */
export function pacLocalRows(p:PACPlacement,server:Uint8Array,lengths:Uint32Array):Uint8Array {
 if(BigInt(lengths.length)!==p.rows||server.length!==lengths.length*INDEX_ROW_BYTES)throw Error('PAC row count mismatch');
 let end=0,marker=0;let previous=-1n;
 for(let i=0;i<lengths.length;i++) { const page=(p.memberStart+BigInt(end))/BigInt(p.pageSize);if(page!==previous){const m=p.pages[marker++];if(!m||m.row!==BigInt(i)||m.page!==page)throw Error("PAC page marker mismatch");previous=page} if(!lengths[i])throw Error('Empty PAC member');end+=lengths[i]; }
 if(marker!==p.pages.length)throw Error("PAC page markers mismatch");
 if(BigInt(end)!==p.memberBytes)throw Error('PAC member extent mismatch');
 return server.slice();
}
export function indexMillis(row:Uint8Array,base:number):number {if(row.length!==INDEX_ROW_BYTES)throw Error('Invalid index row');return timeFromLowWord(new DataView(row.buffer,row.byteOffset,row.byteLength).getUint32(0,true),base);}
export function indexUUID(row:Uint8Array,base:number,ordinal:number):Uint8Array{return seedKey(ordinal,indexMillis(row,base));}
