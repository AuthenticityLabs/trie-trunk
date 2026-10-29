import {test} from 'node:test';
import assert from 'node:assert/strict';
import {defaultProfile,normalizeProfile,loadProfile,saveProfile} from './profile.ts';
import {connectionURL,connectionFetch,connectionScope,fetchCredentialScope} from './connections.ts';
test('profile persists preferences and server registrations without credentials',async()=>{
 const original=globalThis.indexedDB,values=new Map();
 globalThis.indexedDB={open(){const open={};queueMicrotask(()=>{const store={get(key){return {result:values.get(key)}},put(value,key){values.set(key,structuredClone(value));return {result:key}}};open.result={close(){},transaction(){const tx={objectStore(){return store}};queueMicrotask(()=>tx.oncomplete());return tx}};open.onsuccess()});return open}};
 try{const profile=defaultProfile();profile.name='Jason';profile.appearance='dark';profile.palette='snoot';profile.connections=[{id:'one',name:'Server One',url:'https://one.example/api/',enabled:true,token:'do-not-store'}];profile.token='do-not-store';const saved=await saveProfile(profile);assert.equal(saved.connections[0].url,'https://one.example/api');assert(!JSON.stringify(values.get('profile')).includes('do-not-store'));assert.deepEqual(await loadProfile(),saved);assert.equal(saved.name,'Jason');}finally{globalThis.indexedDB=original}
});
test('registrations reject embedded credentials and isolate server namespaces',()=>{
 assert.equal(connectionURL(''),'');assert.equal(connectionURL('https://one.example/api/'),'https://one.example/api');
 for(const url of ['file:///etc','https://user:password@one.example','https://one.example?token=secret','https://one.example/#token'])assert.throws(()=>connectionURL(url));
 const a={id:'one',url:'https://one.example'},b={id:'two',url:'https://two.example'};assert.notEqual(connectionScope(a,''),connectionScope(b,''));assert.notEqual(connectionScope(a,'same-token'),connectionScope(b,'same-token'));
 const profile=defaultProfile();profile.connections.push({...profile.connections[0]});assert.throws(()=>normalizeProfile(profile),/unique/);
});
test('requests use actual folder paths under only their own registered server root',async()=>{
 const calls=[],connection={id:'one',url:'https://one.example/api'},scoped=connectionFetch(connection,async(url,init)=>{calls.push([url,init.headers.Authorization]);return new Response()});
 await scoped('/demo/payment/transactions.v1/0/write.tri?begin=2630100&end=2630101',{headers:{Authorization:'Bearer one-token'}});
 assert.deepEqual(calls,[['https://one.example/api/demo/payment/transactions.v1/0/write.tri?begin=2630100&end=2630101','Bearer one-token']]);
 for(const url of ['https://two.example/trunk','https://one.example/other'])assert.throws(()=>scoped(url,{headers:{Authorization:'Bearer one-token'}}),/outside/);
 assert.equal(calls.length,1);assert.equal(fetchCredentialScope(scoped,'one-token'),connectionScope(connection,'one-token'));
});
