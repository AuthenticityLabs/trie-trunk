import {test} from 'node:test';
import assert from 'node:assert/strict';
import {Choices} from './pil/metadata.ts';
import {encodeObject} from 'pill-packer/object';
import {parseQuery} from './query.ts';
test('native definitions expose the duration role unit regardless of field name',()=>{
 const definition={pil:0,version:0,definition:'payment/transactions',fields:[{name:'event_id',type:'UUID'},{name:'elapsed',type:'U64'},{name:'count',type:'U64'}],resource:{service:'payment',resource:'transactions',roles:{event_id:'event_id',duration:'elapsed'}}};
 const data=encodeObject(definition),fields=new Choices([{digest:'example',store:data,pil:data}],[{stream:0,digest:'example',tap:'demo/payment/transactions'}]).fields();
 assert.equal(fields.find(f=>f.name==='elapsed').unit,'us');assert.equal(fields.find(f=>f.name==='count').unit,undefined);
 assert.deepEqual(parseQuery('events | where elapsed <= 10000',fields).plan.filters[0].value,{kind:'duration',value:'10000ms'});
});
