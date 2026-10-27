import {test} from 'node:test';
import assert from 'node:assert/strict';
import {capturePosition,resolvePosition,validPosition} from './ui-position.ts';
import {readResearch,writeResearch} from './research-session.ts';
const grid={scrollTop:200,scrollLeft:50,scrollHeight:1000,scrollWidth:600,clientHeight:200,clientWidth:400};
test('scroll restores pixels for appended pages and percentage for resized viewports',()=>{
 const saved=capturePosition(grid);
 assert.equal(saved.vertical,.25);assert.equal(saved.horizontal,.25);
 assert.deepEqual(resolvePosition(saved,{...grid,scrollHeight:2000}),{top:200,left:50});
 assert.deepEqual(resolvePosition(saved,{...grid,clientHeight:400,clientWidth:200}),{top:150,left:100});
 assert.equal(validPosition(saved),true);
 assert.equal(validPosition({...saved,top:-1}),false);
 assert.equal(validPosition({...saved,vertical:Infinity}),false);
 assert.equal(validPosition(null),false);
});
test('research retains submitted query separately from edits, scoped to credentials and view',()=>{
 const values=new Map();globalThis.sessionStorage={getItem:k=>values.get(k),setItem:(k,v)=>values.set(k,v)};
 try{
  const query={source:'demo/payment/transactions.v0/write',id:'7',from:'2026-10-01T00:00',to:'2026-10-02T00:00'};
  const state={query,draft:{...query,id:'unsubmitted'},visible:150,selected:'event-uuid',position:capturePosition(grid)};
  assert.equal(writeResearch('Object History','scope',state),true);
  assert.deepEqual(readResearch('Object History','scope'),state);
  assert.equal(readResearch('Object History','different-credential'),undefined);
  assert.equal(readResearch('Lost Events','scope'),undefined);
  writeResearch('Object History','scope',{...state,visible:-10});
  assert.equal(readResearch('Object History','scope'),undefined);
  sessionStorage.setItem=()=>{throw Error('QuotaExceeded')};
  assert.equal(writeResearch('Object History','scope',state),false);
 }finally{delete globalThis.sessionStorage}
});

test('captured scroll geometry is bounded during Safari bounce and layout shrink',()=>{
 assert.deepEqual(capturePosition({...grid,scrollTop:-50,scrollLeft:-20}),{top:0,left:0,vertical:0,horizontal:0,viewportHeight:200,viewportWidth:400});
 const end=capturePosition({...grid,scrollTop:1200,scrollLeft:400});
 assert.equal(end.top,800);assert.equal(end.left,200);assert.equal(end.vertical,1);assert.equal(validPosition(end),true);
 assert.equal(capturePosition({...grid,scrollHeight:100}).vertical,0);
});
