import {test} from 'node:test';
import assert from 'node:assert/strict';
import {newestCommonTimestamp,resumeFromCheckpoint,searchSignature,searchPredicates} from './search/searches-lru.ts';
import {parseQuery} from './query.ts';
const fields=[{name:'duration',type:'U64',values:[]},{name:'state',type:'STRING',values:['ok','error']}];
const plan=text=>parseQuery(text,fields).plan;
test('query signatures omit ordering/page/window/source and normalize builder/pipeline predicates',async()=>{
 const pipeline=plan('events | where duration > 1000 | page 10');
 const builder=plan('events.query().where(duration, >, 1000).order(timestamp, desc).page(100)');
 assert.equal(await searchSignature(pipeline),await searchSignature(builder));
 assert.equal(await searchSignature({...pipeline,from:1,to:2,tap:'prod/payment',identity:'secret'}),await searchSignature(pipeline));
 assert.notEqual(await searchSignature(pipeline),await searchSignature(plan('events | where duration >= 1000')));
 assert.equal(await searchSignature(undefined),await searchSignature(plan('events')));
});
test('Boolean signatures preserve grouping, NOT, XOR, literal type and text-search mode',async()=>{
 const a=plan('events | where duration > 1000 AND state == error');
 const b=plan('events | where state == error AND duration > 1000');
 assert.equal(await searchSignature(a),await searchSignature(b));
 for(const operator of ['OR','XOR'])assert.notEqual(await searchSignature(a),await searchSignature(plan(`events | where duration > 1000 ${operator} state == error`)));
 assert.notEqual(await searchSignature(a),await searchSignature(plan('events | where NOT (duration > 1000 AND state == error)')));
 assert.notEqual(searchPredicates({filters:[],search:{text:'error',mode:'contains'}}),searchPredicates({filters:[],search:{text:'error',mode:'prefix'}}));
});
test('a common checkpoint is the oldest newest timestamp; missing progress remains unknown',()=>{
 assert.equal(newestCommonTimestamp([12000,11000,15000]),11000);
 assert.equal(resumeFromCheckpoint({checkpointEpochMs:11000}),10999);
 assert.equal(newestCommonTimestamp([12000,undefined,15000]),undefined);
 assert.equal(newestCommonTimestamp([]),undefined);
 assert.equal(newestCommonTimestamp([NaN]),undefined);
 assert.equal(resumeFromCheckpoint({}),undefined);
 assert.equal(resumeFromCheckpoint({checkpointEpochMs:0}),0);
});
