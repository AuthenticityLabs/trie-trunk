import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadQuerySyntax, saveQuerySyntax, querySyntaxKey, querySyntaxPreference } from './query-preference.ts';

test('syntax preference validates stored values and shares the selected syntax',()=>{
 const descriptor=Object.getOwnPropertyDescriptor(globalThis,'sessionStorage');
 const values=new Map();
 Object.defineProperty(globalThis,'sessionStorage',{configurable:true,value:{getItem:k=>values.get(k)??null,setItem:(k,v)=>values.set(k,v)}});
 try{
  assert.equal(loadQuerySyntax(),'pipeline');
  assert.equal(saveQuerySyntax('pipeline'),true);
  assert.equal(values.get(querySyntaxKey),'pipeline');assert.equal(loadQuerySyntax(),'pipeline');
  assert.equal(saveQuerySyntax('builder'),true);assert.equal(loadQuerySyntax(),'builder');
  values.set(querySyntaxKey,'invalid');assert.equal(loadQuerySyntax(),'pipeline');
  assert.equal(querySyntaxPreference(null),'pipeline');
 }finally{if(descriptor)Object.defineProperty(globalThis,'sessionStorage',descriptor);else delete globalThis.sessionStorage}
});
test('blocked browser storage leaves editing usable and reports a failed save',()=>{
 const descriptor=Object.getOwnPropertyDescriptor(globalThis,'sessionStorage');
 Object.defineProperty(globalThis,'sessionStorage',{configurable:true,get(){throw new Error('Storage blocked')}});
 try{assert.equal(loadQuerySyntax(),'pipeline');assert.equal(saveQuerySyntax('pipeline'),false)}
 finally{if(descriptor)Object.defineProperty(globalThis,'sessionStorage',descriptor);else delete globalThis.sessionStorage}
});
