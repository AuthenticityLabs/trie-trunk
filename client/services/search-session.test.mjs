import {test} from 'node:test';
import assert from 'node:assert/strict';
import {SearchSession,credentialScope,clearTabWorkspace} from './search-session.ts';
const query={tap:'demo/payment/transactions.v1/write',from:'2026-09-01T12:00:00Z',to:'2026-09-01T14:00:00Z',size:2};
class Storage{
 values=new Map();get length(){return this.values.size}key(i){return [...this.values.keys()][i]??null}
 getItem(k){return this.values.get(k)??null}setItem(k,v){this.values.set(k,v)}removeItem(k){this.values.delete(k)}
}
test('search settings survive reload without serializing any event, definition, result or trie',()=>{
 const storage=new Storage(),session=new SearchSession(storage);
 const payload=new Proxy({},{get(){throw Error('Result payload must not be accessed');}});
 session.append(query,query,'anonymous',payload);session.position({top:431,left:28},'selected-row');
 const saved=new SearchSession(storage).restore(query,'anonymous');
 assert.deepEqual(saved,{credential:'anonymous',input:query,query,position:{top:431,left:28},selected:'selected-row'});
 assert.deepEqual([...storage.values.keys()],['snoot:search:workspace']);
});
test('settings are isolated by tab, query and credentials; tokens are not saved',async()=>{
 const a=new Storage(),b=new Storage(),scope=await credentialScope('secret');
 new SearchSession(a).append(query,query,scope,{});
 assert.equal(new SearchSession(b).restore(query,scope),undefined);
 assert.equal(new SearchSession(a).restore(query,'anonymous'),undefined);assert.equal(a.length,0);
 new SearchSession(a).append(query,query,scope,{});
 assert.ok(!JSON.stringify([...a.values]).includes('secret'));
 assert.equal(new SearchSession(a).restore({...query,tap:'other'},scope),undefined);
});
test('zoom preserves its query without retaining its results',()=>{
 const storage=new Storage(),session=new SearchSession(storage),zoom={...query,from:'2026-09-01T12:30:00Z'};
 session.append(query,zoom,'anonymous',{});assert.deepEqual(new SearchSession(storage).restore(query,'anonymous').query,zoom);
 session.clear();assert.equal(storage.length,0);
});
test('logout clears all Snoot workspace settings and preserves unrelated storage',()=>{
 const storage=new Storage();for(const k of ['snoot:workspace','snoot:search:workspace','snoot:collections:workspace:anonymous','another-app','snoot-signed-out'])storage.setItem(k,'value');
 clearTabWorkspace(storage);assert.deepEqual([...storage.values.keys()],['another-app','snoot-signed-out']);
});
