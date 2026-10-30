import {test} from 'node:test';
import assert from 'node:assert/strict';
import {searchTimingRows} from './search/timing.ts';

test('local search reports measured stages without fabricated network or summary zeroes',()=>{
 const rows=searchTimingRows(21144,{discovery:21000,planning:120,events:2,request:0,response:0,summaries:0,client:0});
 assert.deepEqual(rows.map(r=>[r.label,r.ms]),[['Source Sync',21000],['Local Planning',120],['Retrieval & Matching',2],['Other Elapsed',22]]);
 assert.equal(rows.reduce((sum,r)=>sum+r.ms,0),21144);
});
test('comparison search retains measured request, decode and summary phases',()=>{
 const rows=searchTimingRows(100,{planning:10,request:20,response:5,summaries:15,events:40,client:0});
 assert.deepEqual(rows.map(r=>r.ms),[10,20,5,15,40,10]);
 assert.equal(rows[1].label,'Request & Wait');
});
test('missing phase measurements do not become zero-valued rows',()=>{
 assert.deepEqual(searchTimingRows(200),[]);
});
