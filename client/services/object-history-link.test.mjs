import {test} from 'node:test';
import assert from 'node:assert/strict';
import {objectHistoryTarget,historyURL} from './replay.ts';
const source='demo/payment/transactions.v1/error',enabled=['demo/payment/transactions.v1/write'];
function event(type,value,name='id'){
 const field={key:1,name,uuid:type==='UUID',enums:new Map()};
 return {dictionary:{tap:source,objectIdentity:{field,type}},read:()=>value};
}
test('history links require advertised replay and a present object role',()=>{
 assert.deepEqual(objectHistoryTarget(event('U32',0),enabled),{source:enabled[0],id:'0',field:'id'});
 for(const value of [null,undefined])assert.equal(objectHistoryTarget(event('U32',value),enabled),undefined);
 assert.equal(objectHistoryTarget(event('U32',42),[]),undefined);
 assert.equal(objectHistoryTarget({dictionary:{tap:source},read:()=>42},enabled),undefined);
 assert.equal(objectHistoryTarget(event('U32',-1),enabled),undefined);
 assert.equal(objectHistoryTarget(event('U32',42),['other/payment/transactions.v1/write']),undefined);
});
test('history links preserve typed identities and resource scope without using the event UUID',()=>{
 for(const [type,value,id] of [
  ['U64',18446744073709551615n,'18446744073709551615'],
  ['STRING','a/b ?✓','a/b ?✓'],['STRING','',''],
  ['UUID',Uint8Array.from(Buffer.from('0199234567897abc8123456789abcdef','hex')),'01992345-6789-7abc-8123-456789abcdef'],
  ['BYTES',Uint8Array.of(0,255,254),'AP_-'],
 ]){
  const target=objectHistoryTarget(event(type,value,'customer_id'),enabled);
  assert.equal(target.id,id);assert.equal(target.field,'customer_id');
  const url=new URL(historyURL({...target,from:'1970-01-01T00:00:00Z',to:'2026-10-20T00:00:00Z'}),'http://localhost');
  assert.equal(url.searchParams.get('id'),id);assert.equal(url.searchParams.get('branch'),'payment/transactions.v1/write');
 }
});
