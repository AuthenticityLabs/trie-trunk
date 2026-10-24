import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadQuerySyntax, saveQuerySyntax, querySyntaxKey, querySyntaxPreference } from './query-preference.ts';

test('syntax preference validates stored values and shares the selected syntax',()=>{
 const descriptor=Object.getOwnPropertyDescriptor(globalThis,'localStorage');
 const values=new Map();
 Object.defineProperty(globalThis,'localStorage',{configurable:true,value:{getItem:k=>values.get(k)??null,setItem:(k,v)=>values.set(k,v)}});
 try{
  assert.equal(loadQuerySyntax(),'builder');
  assert.equal(saveQuerySyntax('pipeline'),true);
  assert.equal(values.get(querySyntaxKey),'pipeline');assert.equal(loadQuerySyntax(),'pipeline');
  assert.equal(saveQuerySyntax('builder'),true);assert.equal(loadQuerySyntax(),'builder');
  values.set(querySyntaxKey,'invalid');assert.equal(loadQuerySyntax(),'builder');
  assert.equal(querySyntaxPreference(null),'builder');
 }finally{if(descriptor)Object.defineProperty(globalThis,'localStorage',descriptor);else delete globalThis.localStorage}
});
test('blocked browser storage leaves editing usable and reports a failed save',()=>{
 const descriptor=Object.getOwnPropertyDescriptor(globalThis,'localStorage');
 Object.defineProperty(globalThis,'localStorage',{configurable:true,get(){throw new Error('Storage blocked')}});
 try{assert.equal(loadQuerySyntax(),'builder');assert.equal(saveQuerySyntax('pipeline'),false)}
 finally{if(descriptor)Object.defineProperty(globalThis,'localStorage',descriptor);else delete globalThis.localStorage}
});
