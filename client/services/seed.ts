import {read,span,type PILDefinition} from 'pill-packer/layout';
import {seedKey,stateAddress} from './seed-identity.ts';
export type SeedDefinition=PILDefinition & {resource?:{roles?:{event_id?:string;shared?:string}}};
export type SeedState = {readField(leaf:number,field:number):unknown;hasField?(leaf:number,field:number):boolean};
const empty=new Uint8Array();
export class SeedBinding {
 readonly definition:SeedDefinition;readonly positions:ReadonlyMap<string,number>;
 readonly identityField?:number;readonly timeField?:number;readonly leafField?:number;readonly eventIdField?:number;readonly sharedField?:number;
 constructor(definition:SeedDefinition){this.definition=definition;this.positions=new Map(definition.fields.map((f,i)=>[f.name,i]));
 this.identityField=this.positions.get(definition.resource?.roles?.event_id??'');this.timeField=this.positions.get('@time');this.leafField=this.positions.get('@leaf');this.eventIdField=this.positions.get('@event_id');this.sharedField=this.positions.get(definition.resource?.roles?.shared??'');}
 field(name:string):number|undefined{return this.positions.get(name);}
}
const bindings=new WeakMap<SeedDefinition,SeedBinding>();
export function bindSeed(definition:SeedDefinition):SeedBinding{let binding=bindings.get(definition);if(!binding){binding=new SeedBinding(definition);bindings.set(definition,binding)}return binding;}
/** A borrowed view. Payload/common owners must outlive this Seed. UUID and values are lazy. */
export class Seed {
 readonly binding:SeedBinding;readonly time:number;readonly stateLeaf:number;readonly taxonomyLeaf?:number;
 readonly payload:Uint8Array;readonly common:Uint8Array;private readonly state?: SeedState;
 private identity?:Uint8Array;
 constructor(binding:SeedBinding,payload:Uint8Array,context:{time:number;stateLeaf:number;taxonomyLeaf?:number;common?:Uint8Array;state?:SeedState}){
  if(!Number.isSafeInteger(context.time)||context.time<0||context.time>=2**48||!Number.isInteger(context.stateLeaf)||context.stateLeaf<0||context.stateLeaf>65535)throw Error('Invalid Seed context');
  this.binding=binding;this.payload=payload;this.common=context.common??empty;this.time=context.time;this.stateLeaf=context.stateLeaf;this.taxonomyLeaf=context.taxonomyLeaf;this.state=context.state;
 }
 private encoded(field:number):unknown{
  const def=this.binding.definition;
  const body=this.common.length&&((this.common[field>>2]>>(6-2*(field%4)))&3)!==0?this.common:this.payload;
  return read(body,def,field);
 }
 get eventId():number{const field=this.binding.field('@event_id');const value=field===undefined?undefined:this.encoded(field);if(typeof value!=='number'||!Number.isInteger(value)||value<0||value>=2**24)throw Error('Invalid Seed record number');return value;}
 get keyBytes():Uint8Array{return this.identity??=seedKey(this.eventId,this.time);}
 get stateSignature():string|undefined{return this.taxonomyLeaf===undefined?undefined:stateAddress(this.taxonomyLeaf,this.stateLeaf);}
 /** Dictionary IDs are retained for the query engine; field() expands them for callers. */
 value(name:string|number):unknown {
  const binding=this.binding,index=typeof name==='number'?name:binding.field(name);
  if(index===undefined||!binding.definition.fields[index]||index===binding.identityField)return undefined;
  if(index===binding.timeField)return this.time;
  if(index===binding.leafField)return this.stateLeaf;
  const state=this.state?.readField(this.stateLeaf,index);if(state!==undefined)return state;
  return this.encoded(index);
 }
 field(name:string|number):unknown {
  const index=typeof name==='number'?name:this.binding.field(name);if(index===undefined)return undefined;
  const value=this.value(index),values=this.binding.definition.fields[index]?.values;
  return values?.length&&typeof value==='number'?values[value]:value;
 }
 read(name:string|number):unknown{return this.field(name);}
 has(key:number):boolean {
  if(key===this.binding.identityField||!this.binding.definition.fields[key])return false;
  if(key===this.binding.timeField||key===this.binding.leafField)return true;
  if(this.state?.hasField?this.state.hasField(this.stateLeaf,key):this.state?.readField(this.stateLeaf,key)!==undefined)return true;
  const body=this.common.length&&((this.common[key>>2]>>(6-2*(key%4)))&3)?this.common:this.payload;
  return ((body[key>>2]>>(6-2*(key%4)))&3)!==0;
 }
 toJSON():Record<string,unknown>{const result:Record<string,unknown>={};for(const field of this.binding.definition.fields){const value=this.read(field.name);if(value!==undefined)result[field.name]=value;}result['@leaf']=this.stateLeaf;return result;}
}
