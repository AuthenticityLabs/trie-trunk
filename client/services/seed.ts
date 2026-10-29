import {read,span,type PILDefinition} from 'pill-packer/layout';
import {seedKey,stateAddress} from './seed-identity.ts';
export type SeedDefinition=PILDefinition & {resource?:{roles?:{event_id?:string}}};
export class SeedBinding {
 readonly definition:SeedDefinition;readonly positions:ReadonlyMap<string,number>;
 constructor(definition:SeedDefinition){this.definition=definition;this.positions=new Map(definition.fields.map((f,i)=>[f.name,i]));}
 field(name:string):number|undefined{return this.positions.get(name);}
}
const bindings=new WeakMap<SeedDefinition,SeedBinding>();
export function bindSeed(definition:SeedDefinition):SeedBinding{let binding=bindings.get(definition);if(!binding){binding=new SeedBinding(definition);bindings.set(definition,binding)}return binding;}
/** A borrowed view. Payload/common owners must outlive this Seed. UUID and values are lazy. */
export class Seed {
 readonly binding:SeedBinding;readonly time:number;readonly stateLeaf:number;readonly taxonomyLeaf?:number;
 private readonly payload:Uint8Array;private readonly common:Uint8Array;private readonly state?: (field:number)=>unknown;
 private identity?:Uint8Array;
 constructor(binding:SeedBinding,payload:Uint8Array,context:{time:number;stateLeaf:number;taxonomyLeaf?:number;common?:Uint8Array;state?:(field:number)=>unknown}){
  if(!Number.isSafeInteger(context.time)||context.time<0||context.time>=2**48||!Number.isInteger(context.stateLeaf)||context.stateLeaf<0||context.stateLeaf>65535)throw Error('Invalid Seed context');
  this.binding=binding;this.payload=payload;this.common=context.common??new Uint8Array();this.time=context.time;this.stateLeaf=context.stateLeaf;this.taxonomyLeaf=context.taxonomyLeaf;this.state=context.state;
 }
 private encoded(field:number):unknown{
  const def=this.binding.definition;
  const body=this.common.length&&span(this.common,def,field).state!==0?this.common:this.payload;
  return read(body,def,field);
 }
 get eventId():number{const field=this.binding.field('@event_id');const value=field===undefined?undefined:this.encoded(field);if(typeof value!=='number'||!Number.isInteger(value)||value<0||value>=2**24)throw Error('Invalid Seed record number');return value;}
 get keyBytes():Uint8Array{return this.identity??=seedKey(this.eventId,this.time);}
 get stateSignature():string|undefined{return this.taxonomyLeaf===undefined?undefined:stateAddress(this.taxonomyLeaf,this.stateLeaf);}
 read(name:string|number):unknown{
  if(name==='@time')return this.time;if(name==='@leaf')return this.stateLeaf;if(name==='@event_id')return this.eventId;
  const def=this.binding.definition,index=typeof name==='number'?name:this.binding.field(name);if(index===undefined||!def.fields[index])return undefined;
  if(def.fields[index].name===def.resource?.roles?.event_id)return undefined;
  if(def.fields[index].name==='@time')return this.time;
  if(def.fields[index].name==='@leaf')return this.stateLeaf;
  if(def.fields[index].name==='@event_id')return this.eventId;
  const state=this.state?.(index);if(state!==undefined)return state;
  const value=this.encoded(index),values=def.fields[index].values;return values?.length&&value!==null&&value!==undefined?values[Number(value)]:value;
 }
 toJSON():Record<string,unknown>{const result:Record<string,unknown>={};for(const field of this.binding.definition.fields){const value=this.read(field.name);if(value!==undefined)result[field.name]=value;}result['@leaf']=this.stateLeaf;return result;}
}
