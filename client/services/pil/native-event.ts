import {INDEX_ROW_BYTES} from './pac-native.ts';
import { decodeObject } from "./control.ts";
import { indexMillis } from './pac-native.ts';
import {Seed,bindSeed} from '../seed.ts';
import { pack, type LogEvent } from "../dictionary.ts";
import { integer, read, scalar, span, width } from "./layout.ts";
import type { Mapping } from "./metadata.ts";

type Node = { parent:number; field:number; raw:Uint8Array; value?:unknown; decoded?:boolean };
// One shared trie per immutable map/source. Values are decoded at most once per
// node, when a view actually reads them, rather than expanded into every event.
export class NativeTrie {
  private nodes: (Node|undefined)[]=[undefined];
  readonly mapping: Mapping;
  constructor(mapping:Mapping){
    if(!mapping.native)throw Error("Native trie requires a PIL/resource map");
    this.mapping=mapping;
  }
  append(start:number,bytes:Uint8Array){
    const nodes=decodeObject(bytes) as Record<string,unknown>[];
    if(!Array.isArray(nodes)||!Number.isInteger(start)||start<1||start>this.nodes.length||start+nodes.length>65536)
      throw Error("Invalid native trie prefix");
    const path=[...this.mapping.dictionary.path];
    for(let i=0;i<nodes.length;i++){
      const id=start+i,n=nodes[i],parent=n?.["0"],field=n?.["1"],raw=n?.["2"];
      if(typeof parent!=="number"||!Number.isInteger(parent)||parent<0||parent>=id||
        typeof field!=="number"||!Number.isInteger(field)||!this.mapping.definition.fields[field]||
        !(raw instanceof Uint8Array)||!raw.length)throw Error("Invalid native trie node");
      let depth=0;
      for(let p=parent;p;p=this.node(p).parent)depth++;
      if(path[depth]!==field)throw Error("Native trie path differs from definition");
      const f=this.mapping.definition.fields[field],w=width(f);
      const adaptive=!f.values?.length&&(/^[IU](32|64)$/.test(f.type)||this.mapping.definition.adaptive_integers==="16"&&/^[IU]16$/.test(f.type))&&["16","32"].includes(this.mapping.definition.adaptive_integers??"");
      if(w===0||raw.length!==w&&(!adaptive||![1,2,4,8].includes(raw.length)||raw.length>w))throw Error("Invalid native trie value width");
      if(f.values?.length&&Number(integer(new DataView(raw.buffer,raw.byteOffset,raw.length),raw.length))>=f.values.length)
        throw Error("Unknown native dictionary reference");
      const prior=this.nodes[id];
      if(prior){
        if(prior.parent!==parent||prior.field!==field||prior.raw.length!==raw.length||raw.some((v,j)=>v!==prior.raw[j]))throw Error("Immutable native trie changed");
      }else this.nodes.push({parent,field,raw});
    }
  }
  private node(id:number):Node{const n=this.nodes[id];if(!n)throw Error("Unknown native trie reference");return n;}
  has(leaf:number){return leaf===0||!!this.nodes[leaf];}
  field(leaf:number,key:number):Node|undefined{
    for(let p=leaf;p;){const n=this.node(p);if(n.field===key)return n;p=n.parent;}
  }
  keys(leaf:number):number[]{
    const keys:number[]=[];for(let p=leaf;p;){const n=this.node(p);keys.push(n.field);p=n.parent;}return keys;
  }
  read(node:Node){
    if(!node.decoded){
      const f=this.mapping.definition.fields[node.field];
      const value=f.values?.length?integer(new DataView(node.raw.buffer,node.raw.byteOffset,node.raw.length),node.raw.length):scalar(node.raw,f.type);
      node.value=pack(value,this.mapping.dictionary.fields.keys.get(node.field));node.decoded=true;
    }
    return node.value;
  }
}

// Direct view over the canonical row and decoded PIL member bytes. Native
// members can inherit values from their PAC common frame and shared trie.
export class NativeEvent {
  readonly pil=true as const;
  private uuidText?:string;
  get uuid():string{return this.uuidText??=String.fromCharCode(...this.seed.keyBytes)}
  readonly millis:number;
  readonly dictionary;
  readonly fields;
  private seed:Seed;
  private payload:Uint8Array;
  private common?:Uint8Array;
  private trie:NativeTrie;
  readonly stateLeaf:number;
  get durationField(){return this.trie.mapping.definition.resource?.roles.duration;}
  private leaf:number;
  constructor(row:Uint8Array,payload:Uint8Array,trie:NativeTrie,common?:Uint8Array,epochBase=0){
    this.common=common;
    if(row.length!==INDEX_ROW_BYTES||!Number.isSafeInteger(epochBase))throw Error("Invalid native index row");
    this.payload=payload;this.trie=trie;
    const def=trie.mapping.definition,name=def.resource?.roles.shared;
    const ref=name?def.fields.findIndex(f=>f.name===name):-1;
    const refBody=ref>=0&&common&&span(common,def,ref).state!==0?common:payload;
    const value=ref>=0?read(refBody,def,ref):undefined;
    this.stateLeaf=new DataView(row.buffer,row.byteOffset,row.byteLength).getUint16(4,true);
    this.leaf=this.stateLeaf;
    if(value!==undefined&&Number(value)!==this.leaf)throw Error("State leaf differs from index");
    if(!trie.has(this.leaf))throw Error("Missing native trie reference");
    this.dictionary=trie.mapping.dictionary;this.fields=this.dictionary.fields;
    this.millis=indexMillis(row,epochBase);
    this.seed=new Seed(bindSeed(def),payload,{time:this.millis,stateLeaf:this.stateLeaf,common});
    // The PAC checksum and pill-packer framing have already been checked.
    // The canonical index leaf supplies shared values without payload duplicates.

  }
  private body(key:number){return this.common&&((this.common[key>>2]>>(6-2*(key%4)))&3)?this.common:this.payload;}
  read(key:number):unknown{
    if(key===this.dictionary.id.key)return undefined;
    if(this.trie.mapping.definition.fields[key]?.name==="@leaf")return this.stateLeaf;
    if(this.trie.mapping.definition.fields[key]?.name==="@time")return this.millis;
    const node=this.trie.field(this.leaf,key);if(node)return this.trie.read(node);
    if(!this.fields.keys.has(key))return undefined;
    return pack(read(this.body(key),this.trie.mapping.definition,key),this.fields.keys.get(key));
  }
  orderedKeys():number[]{
    return this.trie.mapping.keys.filter(key=>key!==this.dictionary.id.key&&(this.trie.mapping.definition.fields[key]?.name==="@leaf"||this.trie.mapping.definition.fields[key]?.name==="@time"||!!this.trie.field(this.leaf,key)||span(this.body(key),this.trie.mapping.definition,key).state!==0));
  }
}
const compatible=(event:NativeEvent):LogEvent=>event;
void compatible;
