import {UPack,type Definition,open,decompress,CPack} from 'pill-packer/pack';
import {Seed,bindSeed,type SeedBinding} from './seed.ts';
import {timeFromLowWord} from './seed-identity.ts';
/** Trie-Trunk context around the portable codec; no PAC encoding is implemented here. */
export class Pack {
 readonly pack:UPack;readonly binding:SeedBinding;readonly rows:Uint8Array;readonly hour:number;
 private readonly taxonomyLeaf?:number;private readonly state?:(leaf:number,field:number)=>unknown;
 constructor(pack:UPack,rows:Uint8Array,context:{hour:number;taxonomyLeaf?:number;state?:(leaf:number,field:number)=>unknown}){
  if(rows.length!==pack.header.count*6)throw Error('Seed Pack/index count mismatch');
  this.pack=pack;this.binding=bindSeed(pack.definition.layout);this.rows=rows;this.hour=context.hour;this.taxonomyLeaf=context.taxonomyLeaf;this.state=context.state;
 }
 static async open(definition:Definition,bytes:Uint8Array,rows:Uint8Array,context:{hour:number;taxonomyLeaf?:number;state?:(leaf:number,field:number)=>unknown}):Promise<Pack>{
  const container=open(definition,bytes);return new Pack(container instanceof CPack?await decompress(container):container,rows,context);
 }
 get count(){return this.pack.header.count;}
 get(ordinal:number):Seed{
  const payload=this.pack.member(ordinal),row=new DataView(this.rows.buffer,this.rows.byteOffset+ordinal*6,6),leaf=row.getUint16(4,true);
  return new Seed(this.binding,payload,{time:timeFromLowWord(row.getUint32(0,true),this.hour),stateLeaf:leaf,taxonomyLeaf:this.taxonomyLeaf,common:this.pack.header.common,state:this.state?field=>this.state!(leaf,field):undefined});
 }
}
