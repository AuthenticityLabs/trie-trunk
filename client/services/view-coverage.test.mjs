import {test} from 'node:test';
import assert from 'node:assert/strict';
import {ViewCoverage} from './pil/view-coverage.ts';
const bindings=[{stream:0,digest:'d'.repeat(64),tap:'demo/payment/transactions.v1/write'}];
const bucket={key:'b'.repeat(64),stream:0,from:0,to:3600000,ranges:[{Start:10,End:13}],sealed:true};
const ref=n=>({stream:0,row:n,key:String(n).padStart(64,'0'),cached:false});
const known=c=>c.known('demo/payment',0,3600000).keys;
test('only every row in a sealed bucket certifies completeness, including across overlapping pages',()=>{
 const c=new ViewCoverage();
 c.add([bucket],bindings,[ref(11)],()=>true);assert.deepEqual(known(c),[]);
 c.add([bucket],bindings,[ref(10),ref(11)],()=>true);assert.deepEqual(known(c),[]);
 c.add([bucket],bindings,[ref(12)],()=>true);assert.deepEqual(known(c),[bucket.key]);
 assert.deepEqual(c.status(),{complete:1,partial:0});
 c.evict(ref(11).key);assert.deepEqual(known(c),[]);assert.deepEqual(c.status(),{complete:0,partial:1});
 c.add([bucket],bindings,[ref(11)],()=>true);assert.deepEqual(known(c),[bucket.key]);
 assert.deepEqual(c.known('demo/other',0,3600000).keys,[]);
 assert.deepEqual(c.known('demo',3600000,7200000).keys,[]);
});
test('open hours, missing frames and generation changes never inherit complete coverage',()=>{
 const c=new ViewCoverage(),refs=[ref(10),ref(11),ref(12)];
 c.add([{...bucket,sealed:false}],bindings,refs,()=>true);assert.deepEqual(known(c),[]);
 c.add([bucket],bindings,[],()=>true);assert.deepEqual(known(c),[bucket.key]);
 c.add([{...bucket,key:'c'.repeat(64)}],bindings,refs,k=>k!==ref(12).key);
 assert.deepEqual(known(c),[],'a replacement bucket invalidates the old receipt');
});
test('bucket manifests reject overlapping, incomplete and changed row mappings',()=>{
 const c=new ViewCoverage();
 assert.throws(()=>c.validate([{...bucket,ranges:[{Start:0,End:2},{Start:1,End:3}]}],bindings,[]),/rows/);
 assert.throws(()=>c.validate([bucket],bindings,[ref(15)]),/Missing/);
 assert.throws(()=>c.validate([bucket,{...bucket,key:'c'.repeat(64)}],bindings,[ref(10)]),/overlapping/);
 c.add([bucket],bindings,[ref(10)],()=>true);
 assert.throws(()=>c.add([{...bucket,ranges:[{Start:10,End:14}]}],bindings,[],()=>true),/identity/);
});
test('provisional page members never certify physical bucket completeness',()=>{
 const c=new ViewCoverage();
 c.add([bucket],bindings,[ref(10),{...ref(13),provisional:true}],()=>true);
 assert.deepEqual(known(c),[]);
 assert.throws(()=>c.validate([bucket],bindings,[{...ref(11),provisional:true}]),/Missing/);
 c.add([bucket],bindings,[ref(11),ref(12)],()=>true);
 assert.deepEqual(known(c),[bucket.key]);
 assert.equal(c.known('demo',0,3600000).events.has(ref(13).key),false);
});
