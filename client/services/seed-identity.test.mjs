import {test} from 'node:test';
import assert from 'node:assert/strict';
import {seedKey,timeFromLowWord,stateAddress} from './seed-identity.ts';
test('record keys retain time and the full 24-bit ordinal without entropy',()=>{
 for(const time of [0,1,2**32-1,2**32,Date.UTC(2026,9,29),2**48-1])for(const record of [0,1,255,256,65535,65536,0xffffff]){
  const key=seedKey(record,time);let t=0;for(let i=0;i<6;i++)t=t*256+key[i];assert.equal(t,time);assert.equal(key[13]*65536+key[14]*256+key[15],record);
  assert.equal(key[6],0x70);assert.equal(key[8],0x80);
 }
});
test('low timestamp rollover within one hour reconstructs in order',()=>{
 const boundary=2**32*417,hour=Math.floor(boundary/3600000)*3600000;
 for(const time of [hour,boundary-1,boundary,boundary+1,hour+3599999])assert.equal(timeFromLowWord(time%2**32,hour),time);
 assert.throws(()=>timeFromLowWord((hour+3600000)%2**32,hour));
});
test('identity and taxonomy reject invalid values',()=>{
 for(const n of [-1,1.5,2**24,NaN])assert.throws(()=>seedKey(n,0));
 assert.throws(()=>seedKey(0,2**48));
 assert.equal(stateAddress(0x1234,0xabcd),'0x1234abcd');assert.throws(()=>stateAddress(65536,1));
});
