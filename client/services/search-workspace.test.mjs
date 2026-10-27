import {test} from 'node:test';
import assert from 'node:assert/strict';
import {restoreWorkspace} from './search-workspace.ts';
const state={version:1,page:'Search',collapsed:false,tap:'demo/payment',range:'custom',from:'2026-09-25T00:00',to:'2026-10-26T00:00',localParameters:{},localTap:'',localFrom:'',localTo:'',pageSize:100,scroll:0,queryText:'events | where duration <= 10000',query:{tap:'demo/payment',from:'2026-09-25T00:00:00Z',to:'2026-10-26T00:00:00Z'}};
test('overscroll cannot discard the submitted query or selected time window',()=>{
 for(const range of ['custom','today','week']){
  const restored=restoreWorkspace({...state,range,scroll:-20,positions:{Search:{top:-40,left:0,vertical:-.2,horizontal:1.2},Dashboards:{top:'broken',left:0}}});
  assert.equal(restored.range,range);assert.equal(restored.from,state.from);assert.equal(restored.to,state.to);
  assert.deepEqual(restored.query,state.query);assert.equal(restored.queryText,state.queryText);
  assert.equal(restored.scroll,0);assert.deepEqual(restored.positions,{Search:{top:0,left:0,vertical:0,horizontal:1}});
 }
});
test('invalid core workspace is rejected independently of scroll recovery',()=>{
 assert.equal(restoreWorkspace({...state,pageSize:0}),undefined);
 assert.equal(restoreWorkspace({...state,from:null}),undefined);
 assert.equal(restoreWorkspace({...state,query:{...state.query,from:'invalid'}}),undefined);
 assert.equal(restoreWorkspace(null),undefined);
});
