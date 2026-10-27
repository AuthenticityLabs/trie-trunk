import {test} from 'node:test';
import assert from 'node:assert/strict';
import {RowWindow} from './row-window.ts';

test('variable rows cover every scroll position without mounting the entire list',()=>{
 const rows=new RowWindow(100);rows.resize(10000);
 rows.measure(0,250);rows.measure(10,2000);rows.measure(8000,50);
 assert.equal(rows.offset(10000),1000000+150+1900-50);
 for(const top of [0,250,1300,500000,rows.offset(10000)-590]){
  const range=rows.range(top,590);
  assert.ok(range.end-range.start<20);
  assert.ok(range.before<=top);
  assert.ok(rows.offset(range.end)>=top+590);
  assert.equal(range.before+rows.offset(range.end)-rows.offset(range.start)+range.after,rows.offset(10000));
 }
 assert.equal(rows.index(249),0);assert.equal(rows.index(250),1);
 rows.resize(10100);assert.equal(rows.offset(1),250);
 rows.resize(100,true);assert.equal(rows.offset(1),100);
 rows.resize(0);assert.deepEqual(rows.range(0,590),{start:0,end:0,before:0,after:0});
});
