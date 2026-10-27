import test from 'node:test';
import assert from 'node:assert/strict';
import {resolveTheme, nextTheme, defaultTheme, loadTheme, saveTheme} from './preferences.ts';

test('Auto follows system appearance while explicit choices stay fixed', () => {
 assert.equal(defaultTheme,'auto');
 assert.equal(resolveTheme('auto',false),'light');
 assert.equal(resolveTheme('auto',true),'trunk');
 for(const systemDark of [false,true]){
  assert.equal(resolveTheme('dark',systemDark),'trunk');
  assert.equal(resolveTheme('light',systemDark),'light');
 }
 assert.equal(nextTheme('light'),'dark');
 assert.equal(nextTheme('dark'),'auto');
 assert.equal(nextTheme('auto'),'light');
});

test('theme persistence saves Auto itself rather than the resolved system color', async () => {
 const original=globalThis.indexedDB;
 let saved;
 globalThis.indexedDB={open(){
  const open={};
  queueMicrotask(()=>{
   const store={get(){return {result:saved}},put(value,key){assert.equal(key,'theme');saved=value;return {result:key}}};
   open.result={close(){},transaction(){const tx={objectStore(){return store}};queueMicrotask(()=>tx.oncomplete());return tx}};
   open.onsuccess();
  });
  return open;
 }};
 try{
  assert.equal(await loadTheme(),'auto');
  for(const choice of ['light','dark','auto']){await saveTheme(choice);assert.equal(await loadTheme(),choice)}
 }finally{globalThis.indexedDB=original;}
});
