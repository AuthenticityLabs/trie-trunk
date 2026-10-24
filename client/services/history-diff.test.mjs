import {test} from 'node:test';
import assert from 'node:assert/strict';
import {ChangeHistory,sameHistoryValue} from './history-diff.ts';
const fields=['event_id','id','revision_id','state','amount','detail'].map((name,key)=>({name,key,enums:new Map(),uuid:false}));
function event(values){return {pil:true,uuid:'\0'.repeat(16),millis:0,dictionary:{id:fields[0],objectIdentity:{field:fields[1]},revisionIdentity:fields[2],headers:[],fields:{keys:new Map(fields.map(f=>[f.key,f])),names:new Map(fields.map(f=>[f.name,f]))}},fields:{keys:new Map(fields.map(f=>[f.key,f]))},orderedKeys:()=>Object.keys(values).map(Number),read:key=>values[key]}}
test('sparse revisions compare last supplied values without invented removals or origins',()=>{
 const history=new ChangeHistory(),first=event({1:42,2:10,3:'pending',4:100}),second=event({1:42,2:11,3:'authorized'}),third=event({1:42,2:12,4:null});
 for(const e of [first,second,third])history.append(e);
 assert.deepEqual(history.get(first).changes,[{name:'state',kind:'observed',after:'pending'},{name:'amount',kind:'observed',after:100}]);
 assert.deepEqual(history.get(second).changes,[{name:'state',kind:'changed',before:'pending',after:'authorized'}]);
 assert.deepEqual(history.get(third).changes,[{name:'amount',kind:'changed',before:100,after:null}]);
 assert.equal(history.get(second).supplied,1);
});
test('unchanged values disappear from diffs and type changes remain visible',()=>{
 const history=new ChangeHistory(),a=event({3:'null',5:{a:1,b:2}}),b=event({3:null,5:{b:2,a:1}});
 history.append(a);history.append(b);
 assert.deepEqual(history.get(b).changes,[{name:'state',kind:'changed',before:'null',after:null}]);
 assert.equal(sameHistoryValue(18446744073709551615n,'18446744073709551615'),false);
 assert.equal(sameHistoryValue([1,2],[2,1]),false);
});
