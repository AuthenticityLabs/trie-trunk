import test from 'node:test';
import assert from 'node:assert/strict';
import {quotaBounds,assignedBytes,updateScope,quotaNodes,isManaged,configuredLimit} from './quota-policy.ts';
const read=['demo','svc','res','read'];
const write=['demo','svc','res','write'];
const base={poolBytes:1000,scopes:[{path:read,limitBytes:200},{path:write,limitBytes:300}]};
test('leaf reservations protect siblings through all ancestor gates',()=>{
 assert.equal(assignedBytes(base,[]),500);
 assert.deepEqual(quotaBounds(base,read,5000),{minimum:0,maximum:700});
 const gated=updateScope(base,['demo','svc'],{managed:true,limitBytes:600});
 assert.equal(quotaBounds(gated,read,5000).maximum,300);
 assert.equal(quotaBounds(gated,write,5000).maximum,400);
 const other=['demo','other','res','read'];
 assert.equal(quotaBounds(gated,other,5000).maximum,500);
});
test('intermediate gates do not reserve their unused space',()=>{
 const gated=updateScope(base,['demo','svc'],{managed:true,limitBytes:800});
 assert.equal(assignedBytes(gated,[]),500);
 assert.deepEqual(quotaBounds(gated,[],5000),{minimum:800,maximum:5000});
 assert.deepEqual(quotaBounds(gated,['demo','svc'],5000),{minimum:500,maximum:1000});
 assert.equal(quotaBounds(updateScope(gated,['demo','svc'],{limitBytes:undefined}),[],5000).minimum,500);
});
test('removing a quota releases capacity and preserves recovery settings',()=>{
 let p=updateScope(base,read,{recovery:{unit:'percent',value:25}});
 p=updateScope(p,read,{limitBytes:undefined});
 assert.equal(assignedBytes(p,[]),300);
 assert.equal(quotaBounds(p,write,5000).maximum,1000);
 assert.deepEqual(p.scopes.find(s=>s.path.at(-1)==='read').recovery,{unit:'percent',value:25});
});
test('inventory nodes do not depend on event samples or quota declarations',()=>{
 const inventory=[{path:[],kind:'trunk',allocated:10,logical:5,ownAllocated:0,ownLogical:0,files:1},{path:['empty'],kind:'cache',allocated:10,logical:5,ownAllocated:10,ownLogical:5,files:1}];
 const nodes=quotaNodes(inventory);
 assert.equal(nodes.length,2);
 assert.equal(nodes[0].path.length===0||nodes[1].path.length===0,true);
 assert.equal(nodes.find(n=>n.name==='empty').bytes,10);
 assert.equal(quotaNodes([]).length,0);
});
test('disk ceiling and fixed byte recovery constrain edits',()=>{
 assert.equal(quotaBounds({poolBytes:null,scopes:[]},[],12345).maximum,12345);
 const p=updateScope(base,read,{recovery:{unit:'bytes',value:75}});
 assert.equal(quotaBounds(p,read,5000).minimum,75);
});

test('Managed defaults to Trunk and leaves, with intermediate nodes inactive',()=>{
 for(let depth=0;depth<=4;depth++)assert.equal(isManaged(base,read.slice(0,depth)),depth===0||depth===4);
});
test('unmanaged nodes preserve settings without reserving space or gating descendants',()=>{
 const p=updateScope(base,read,{managed:false,recovery:{unit:'bytes',value:150}});
 assert.equal(configuredLimit(p,read),undefined);
 assert.equal(assignedBytes(p,[]),300);
 assert.equal(quotaBounds(p,write,5000).maximum,1000);
 assert.equal(p.scopes.find(s=>s.path.at(-1)==='read').limitBytes,200);
 const resumed=updateScope(p,read,{managed:true});
 assert.equal(configuredLimit(resumed,read),200);
 assert.equal(assignedBytes(resumed,[]),500);
 const pausedGate=updateScope(base,['demo','svc'],{managed:false,limitBytes:100});
 assert.equal(quotaBounds(pausedGate,read,5000).maximum,700);
 assert.equal(isManaged(pausedGate,write),true);
});
test('unmanaged Trunk releases its own cap without disabling leaves',()=>{
 const p={...base,managed:false};
 assert.equal(configuredLimit(p,[]),undefined);
 assert.equal(isManaged(p,read),true);
 assert.equal(quotaBounds(p,read,5000).maximum,4700);
});
