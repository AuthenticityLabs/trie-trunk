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

test('Seed builders preserve packing, row context, borrowed views, and sealed snapshots',async()=>{
 const {Prescription,encodeHeaders}=await import('pill-packer/pack');
 const rx=new Prescription(definition),hour=Date.UTC(2026,9,29,12);
 for(const index of [false,{addressBytes:2},{addressBytes:3},{addressBytes:4}])for(const fixed of [false,true]){
  const original=fill(definition,[absent,absent,Uint8Array.of(0,0,0),absent,absent]);
  const next=fill(definition,[absent,absent,Uint8Array.of(1,0,0),absent,absent]);
  const builder=Pack.create(rx,{hour,taxonomyLeaf:7},encodeHeaders({source:Uint8Array.of(99)}),{index,layout:fixed?{kind:'fixed',recordWidth:original.length}:{kind:'variable'}});
  builder.push(original,{time:hour+10,stateLeaf:15});builder.push(next,{time:hour+20,stateLeaf:16});
  assert.equal(builder.get(0).payload.buffer,original.buffer);
  assert.equal(builder.offset(original.length).eventId,1);
  assert.throws(()=>builder.push(next,{time:hour+3600000,stateLeaf:0}));assert.equal(builder.count,2);
  assert.throws(()=>builder.push(Uint8Array.of(255),{time:hour,stateLeaf:0}));assert.equal(builder.count,2);assert.equal(builder.rows.length,12);
  const saved=await builder.seal({algorithm:'gzip'}).unseal();
  const popped=builder.pop();assert.equal(popped.eventId,1);assert.equal(popped.time,hour+20);assert.equal(popped.field('@leaf'),16);
  builder.push(original,{time:hour+30,stateLeaf:17});assert.equal(popped.time,hour+20);assert.equal(saved.get(1).time,hour+20);
  const h=(await import('pill-packer/pac')).pacHeader(saved.bytes,0);
  assert.equal(h.addressBytes,index?index.addressBytes:0);assert.equal(!!h.recordWidth,fixed);assert.deepEqual(h.headers.source,Uint8Array.of(99));
  saved.pop();assert.equal(saved.count,1);assert.equal(saved.rows.length,6);
  const replicated=await Pack.open(definition,builder.bytes,builder.rows,{hour});assert.throws(()=>replicated.pop(),/immutable/);
 }
});
test('Seed reads rebase across low-word rollover and retain dictionary IDs for query matching',()=>{
 const time=Math.ceil(2**40/3600000)*3600000,record=fill(definition,[absent,Uint8Array.of(1),Uint8Array.of(0,0,0),absent,absent]);
 const rows=new Uint8Array(6);new DataView(rows.buffer).setUint32(0,time%2**32,true);
 const seed=new Pack(pack(definition,[record]),rows,{hour:time}).get(0);
 assert.equal(seed.field('status'),'paid');assert.equal(seed.value(1),1);assert.equal(seed.time,time);
});
