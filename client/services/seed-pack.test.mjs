import {test} from 'node:test';import assert from 'node:assert/strict';import {readFile} from 'node:fs/promises';
import {createCodec} from 'pill-packer';import {define,fill,pack,absent} from 'pill-packer/pack';
import {Pack} from './pack.ts';
import {PILEvent} from './pil/event.ts';
import {compile} from './pil/metadata.ts';
import {encodeObject} from 'pill-packer/object';
import {seedKey} from './seed-identity.ts';
const codec=await createCodec(await readFile(new URL(import.meta.resolve('pill-packer/codec.wasm'))));
const definition=await define(codec.definitionFromJSON(JSON.stringify({pil:0,definition:'payment/transactions',version:0,fields:[{name:'original',type:'UUID'},{name:'status',type:'STRING',values:['pending','paid']},{name:'@event_id',type:'U24'},{name:'@time',type:'TIMESTAMP_MS'},{name:'@leaf',type:'REF16'}],resource:{roles:{event_id:'original'}}})),codec);
test('Seed Pack binds numeric ordinals and index-only state fields',()=>{
 const time=Date.UTC(2026,9,29,12,30),hour=Math.floor(time/3600000)*3600000;
 const record=fill(definition,[absent,absent,new Uint8Array([0,0,0]),absent,absent]),container=pack(definition,[record],{index:{addressBytes:3}}),rows=new Uint8Array(6),v=new DataView(rows.buffer);
 v.setUint32(0,time%2**32,true);v.setUint16(4,15,true);
 const wrapper=new Pack(container,rows,{hour,taxonomyLeaf:7,state:(leaf,field)=>leaf===15&&field===1?'paid':undefined});
 const seed=wrapper.get(0);assert.equal(seed.eventId,0);assert.equal(seed.read('original'),undefined);assert.equal(seed.read('@time'),time);assert.equal(seed.read('status'),'paid');assert.equal(seed.read('@leaf'),15);assert.equal(seed.read(4),15);assert.equal(seed.toJSON()['@leaf'],15);assert.equal(seed.stateSignature,'0x0007000f');
 assert.equal(wrapper.get(0).binding,seed.binding);assert.equal(seed.read('missing'),undefined);
});
test('U24 uses three bytes through the WASM validator and portable reader',()=>{
 for(const n of [0,255,256,65535,65536,0xffffff]){
  const raw=fill(definition,[absent,absent,new Uint8Array([n,n>>>8,n>>>16]),absent,absent]);
  assert.equal(raw.length,5);
  const rows=new Uint8Array(6),time=Date.UTC(2026,9,29,12);new DataView(rows.buffer).setUint32(0,time%2**32,true);
  const seed=new Pack(pack(definition,[raw]),rows,{hour:time}).get(0);assert.equal(seed.eventId,n);
 }
 assert.throws(()=>fill(definition,[absent,absent,new Uint8Array(4),absent,absent]));
});

test('materialized live events expose the ordinal, not a counterfeit producer UUID',()=>{
 const d={pil:0,definition:'payment/live',version:0,fields:[{name:'original',type:'UUID'},{name:'@event_id',type:'U24'},{name:'@time',type:'TIMESTAMP_MS'},{name:'@leaf',type:'REF16'}],resource:{roles:{event_id:'original'}}};
 const bytes=encodeObject(d),mapping=compile({digest:'ordinal-live-test',store:bytes,pil:bytes},'demo/payment/live/error');
 const ms=Date.UTC(2026,9,29),mask=Uint8Array.of(0xaa),id=seedKey(7,ms),record=Uint8Array.of(7,0,0),time=new Uint8Array(8);new DataView(time.buffer).setBigUint64(0,BigInt(ms),true);
 const raw=new Uint8Array(30);raw.set(mask);raw.set(id,1);raw.set(record,17);raw.set(time,20);raw.set([15,0],28);
 const event=new PILEvent(raw,mapping);
 assert.equal(event.read(0),undefined);assert.equal(event.read(1),7);assert.equal(event.read(3),15);assert.deepEqual(event.orderedKeys(),[1,2,3]);assert.equal(event.millis,ms);
});
