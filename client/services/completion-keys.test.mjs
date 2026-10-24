import {test} from 'node:test';
import assert from 'node:assert/strict';
import {completionKey} from './completion-keys.ts';
const state={shift:false,modified:false,composing:false,open:true,count:3,active:-1,collapsed:true,atEnd:true};
const key=(key,extra={})=>completionKey({...state,key,...extra});
test('Right opens first, then accepts an explicit selection or typed prefix',()=>{
 assert.deepEqual(key('Tab'),{kind:'accept',index:0});
 assert.deepEqual(key('ArrowRight'),{kind:'open',index:0});
 assert.deepEqual(key('ArrowRight',{open:false}),{kind:'open',index:0});
 assert.deepEqual(key('ArrowRight',{active:2}),{kind:'accept',index:2});
 assert.deepEqual(key('ArrowRight',{engaged:true}),{kind:'accept',index:0});
 assert.equal(key('ArrowRight',{atEnd:false}),undefined);
 assert.deepEqual(key('ArrowRight',{atEnd:false,active:1}),{kind:'accept',index:1});
 assert.equal(key('Enter'),undefined);assert.deepEqual(key('Enter',{active:1}),{kind:'accept',index:1});
});
test('arrows navigate with correct initial direction and wrap at either end',()=>{
 assert.deepEqual(key('ArrowDown'),{kind:'move',index:0});assert.deepEqual(key('ArrowUp'),{kind:'move',index:2});
 assert.deepEqual(key('ArrowDown',{active:2}),{kind:'move',index:0});assert.deepEqual(key('ArrowUp',{active:0}),{kind:'move',index:2});
 assert.deepEqual(key('ArrowDown',{open:false}),{kind:'move',index:0});
});
test('selection, modifiers, IME, dismiss and empty menus retain native editing/focus',()=>{
 for(const k of ['Tab','ArrowRight','ArrowDown','ArrowUp','Enter'])for(const extra of [{shift:true},{modified:true},{composing:true},{collapsed:false},{count:0}])assert.equal(key(k,extra),undefined);
 for(const k of ['Tab','Enter'])assert.equal(key(k,{open:false}),undefined);
 assert.equal(key('ArrowLeft'),undefined);
});
