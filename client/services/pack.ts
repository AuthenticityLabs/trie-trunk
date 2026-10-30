import {UPack,Pack as MutablePack,Pill,SealedPack as CompressedPack,type Prescription,type Definition,type MemberView,type PackOptions,type Compression,open,decompress,CPack} from 'pill-packer/pack';
import {Seed,bindSeed,type SeedBinding,type SeedState} from './seed.ts';
import {timeFromLowWord} from './seed-identity.ts';
type Context={hour:number;taxonomyLeaf?:number;state?:(leaf:number,field:number)=>unknown};
type Row={time:number;stateLeaf:number};
const empty=new Uint8Array();
/** Borrowed PAC members and companion branch rows. Replicas are immutable;
 * create() explicitly starts a mutable builder. No intermediate Pill on get(). */
export class Pack {
 readonly binding:SeedBinding;readonly hour:number;
 private rowStore:Uint8Array;private rowView:DataView;
 private readonly state?:SeedState;
 private readonly view:MemberView={member:empty,common:empty};
 private readonly container:UPack|MutablePack;private readonly context:Context;
 constructor(container:UPack|MutablePack,rows:Uint8Array,context:Context){
  this.container=container;this.context=context;
  if(rows.length!==this.count*6)throw Error('Seed Pack/index count mismatch');
  if(!Number.isSafeInteger(context.hour)||context.hour<0||context.hour%3600000)throw Error('Expected UTC hour boundary');
  if(context.taxonomyLeaf!==undefined&&(!Number.isInteger(context.taxonomyLeaf)||context.taxonomyLeaf<0||context.taxonomyLeaf>65535))throw Error('Invalid taxonomy leaf');
  this.binding=bindSeed(container instanceof UPack?container.definition.layout:container.prescription.definition.layout);
  this.rowStore=rows;this.rowView=new DataView(rows.buffer,rows.byteOffset,rows.byteLength);this.hour=context.hour;this.state=context.state?{readField:context.state}:undefined;
 }
 static create(rx:Prescription,context:Context,headers?:Uint8Array,options:PackOptions={}):Pack{return new Pack(rx.pack(headers,options),empty,context);}
 static async open(definition:Definition,bytes:Uint8Array,rows:Uint8Array,context:Context,validation:'full'|'structural'='full'):Promise<Pack>{
  const container=open(definition,bytes,{validation});return new Pack(container instanceof CPack?await decompress(container,{validation}):container,rows,context);
 }
 get count(){return this.container instanceof UPack?this.container.header.count:this.container.count;}
 get rows():Uint8Array{return this.rowStore.subarray(0,this.count*6);}
 get bytes():Uint8Array{return this.container.bytes;}
 private member(ordinal:number):MemberView {
  if(this.container instanceof MutablePack)return this.container.view(ordinal,this.view);
  this.view.member=this.container.member(ordinal);this.view.common=this.container.header.common;return this.view;
 }
 get(ordinal:number):Seed{
  const view=this.member(ordinal),row=this.rowView,at=ordinal*6;
  return new Seed(this.binding,view.member,{time:timeFromLowWord(row.getUint32(at,true),this.hour),stateLeaf:row.getUint16(at+4,true),taxonomyLeaf:this.context.taxonomyLeaf,common:view.common,state:this.state});
 }
 offset(offset:number):Seed{return this.get(this.container.ordinalAtOffset(offset));}
 private mutable():MutablePack {
  if(!(this.container instanceof MutablePack))throw Error('Replicated Seed Packs are immutable; use a builder');
  return this.container;
 }
 push(input:Seed|Uint8Array,context?:Row):number {
  const pack=this.mutable(),row=input instanceof Seed?{time:input.time,stateLeaf:input.stateLeaf}:context;
  if(!row||!Number.isSafeInteger(row.time)||row.time<this.hour||row.time>=this.hour+3600000||row.time>=2**48||!Number.isInteger(row.stateLeaf)||row.stateLeaf<0||row.stateLeaf>65535)throw Error('Invalid Seed row');
  if(input instanceof Seed&&input.binding!==this.binding)throw Error('Seed Definition mismatch');
  const count=this.count,needed=(count+1)*6;
  // Reserve before mutation; validation completes before either count changes.
  let rows=this.rowStore;
  if(rows.length<needed){rows=new Uint8Array(Math.max(needed,rows.length*2,384));rows.set(this.rowStore);}
  const pill=input instanceof Seed?input.common.length?new Pill(pack.prescription,input.payload,input.common):input.payload:input;
  pack.push(pill);
  const target=new DataView(rows.buffer,rows.byteOffset+count*6,6);
  target.setUint32(0,row.time%2**32,true);target.setUint16(4,row.stateLeaf,true);this.rowStore=rows;this.rowView=new DataView(rows.buffer,rows.byteOffset,rows.byteLength);
  return pack.count;
 }
 pop():Seed|undefined {
  const pack=this.mutable();if(!pack.count)return undefined;
  const row=this.rowView,at=(pack.count-1)*6;
  const time=timeFromLowWord(row.getUint32(at,true),this.hour),stateLeaf=row.getUint16(at+4,true);
  if(time>=2**48)throw Error('Invalid Seed time');
  const view=pack.popView(this.view)!;
  return new Seed(this.binding,view.member,{time,stateLeaf,taxonomyLeaf:this.context.taxonomyLeaf,common:view.common,state:this.state});
 }
 unpack():Seed[]{return Array.from({length:this.count},(_,i)=>this.get(i));}
 seal(compression:Compression):SealedSeedPack {
  // Snapshot only the small companion index; later tail edits cannot change it.
  return new SealedSeedPack(this.mutable().seal(compression),this.rows.slice(),this.context);
 }
}
export class SealedSeedPack {
 private readonly container:CompressedPack;readonly rows:Uint8Array;private readonly context:Context;
 constructor(container:CompressedPack,rows:Uint8Array,context:Context){this.container=container;this.rows=rows;this.context=context;}
 get bytes(){return this.container.bytes;}
 async unseal(validation:'full'|'structural'='full'):Promise<Pack>{return new Pack(await this.container.unseal(validation),this.rows.slice(),this.context);}
}
