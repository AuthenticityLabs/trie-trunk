import {test} from 'node:test';
import assert from 'node:assert/strict';
import {NativeCacheError,nativeCacheIssue,restoreCacheError,cacheErrorMessage} from './store/errors.ts';
import {storagePort} from './store/client.ts';
test('file-worker errors retain recovery details across MessagePort',async()=>{
 const {port1,port2}=new MessageChannel(),archive=storagePort(port1);
 const issue={code:'native-cache-conflict',source:'demo/payment/transactions.v1/write',reason:'generation-fork',detail:{cachedGeneration:'a',incomingGeneration:'b',cachedSequence:1,incomingSequence:1}};
 port2.onmessage=({data})=>port2.postMessage({id:data.id,error:'Local event store: technical conflict',cacheIssue:issue});
 try{
  await assert.rejects(archive('',{kind:'native-finish',session:'example'}),error=>{
   assert.ok(error instanceof NativeCacheError);assert.deepEqual(nativeCacheIssue(error),issue);
   assert.match(cacheErrorMessage(error),/Refresh its local copy/);assert.doesNotMatch(cacheErrorMessage(error),/corrupt|technical/);return true;
  });
 }finally{archive.close();port2.close()}
});
test('ordinary validation errors are not disguised as recoverable cache races',()=>{
 const error=restoreCacheError('Invalid native chunk');
 assert.equal(nativeCacheIssue(error),undefined);assert.equal(cacheErrorMessage(error),'Invalid native chunk');
});

test('page failures retain all conflict diagnostics and deduplicate repair at resource scope',async()=>{
 const {combineCacheErrors,cacheRefreshSources}=await import('./store/errors.ts');
 const make=source=>new NativeCacheError('conflict',{code:'native-cache-conflict',source,reason:'generation-fork',detail:{cachedSequence:1,incomingSequence:1}});
 const causes=[make('demo/payment/transactions.v1/read'),make('demo/payment/transactions.v1/write'),make('demo/billing/invoices.v1/read')];
 const error=combineCacheErrors(causes),issue=nativeCacheIssue(error);
 assert.equal(issue.conflicts.length,3);assert.equal(cacheRefreshSources(issue).length,2);
 assert.match(cacheErrorMessage(error),/Refresh these local copies together/);
 const corrupt=Error('Invalid native chunk');assert.equal(combineCacheErrors([...causes,corrupt]),corrupt);
});
