import {test} from 'node:test';
import assert from 'node:assert/strict';
import {storeSummaryPacket, summaryStorageLimit} from './pil/summary-storage.ts';
test('summary disk cache retains more than 128 small packets and evicts by bytes', async () => {
 const entries=new Map();
 const cache={keys:async()=>[...entries.keys()].map(url=>({url})),match:async key=>entries.get(typeof key==='string'?key:key.url)?.clone(),delete:async key=>entries.delete(key),put:async(key,response)=>entries.set(key,response)};
 // Existing large packets exercise byte accounting without allocating 256 MB.
 entries.set('https://snoot.test/old',new Response('old',{headers:{'Content-Length':String(summaryStorageLimit-100)}}));
 for(let i=0;i<140;i++)await storeSummaryPacket(cache,`https://snoot.test/${i}`,new Uint8Array(1));
 assert.equal(entries.has('https://snoot.test/old'),false);
 assert.equal(entries.size,140);
 await storeSummaryPacket(cache,'https://snoot.test/1',new Uint8Array(2));
 assert.equal(entries.size,140);
 assert.equal((await entries.get('https://snoot.test/1').arrayBuffer()).byteLength,2);
 await assert.rejects(storeSummaryPacket({...cache,put:async()=>{throw Error('Quota exceeded')}},'https://snoot.test/fail',new Uint8Array(1)),/Quota/);
 await storeSummaryPacket(cache,'https://snoot.test/retry',new Uint8Array(1));
});

test('foreground writes are bounded and storage failure releases the queue',async()=>{
 const {queueSummaryPacket,summaryWriteQueueLimit}=await import('./pil/summary-storage.ts');
 let release,started;const entered=new Promise(resolve=>started=resolve),gate=new Promise(resolve=>release=resolve);
 const cache={keys:async()=>[],match:async()=>undefined,delete:async()=>true,put:async()=>{started();await gate;throw Error('Disk full')}};
 assert.equal(queueSummaryPacket(cache,'https://snoot.test/queued',new Uint8Array(summaryWriteQueueLimit)),true);
 await entered;
 assert.equal(queueSummaryPacket(cache,'https://snoot.test/overflow',new Uint8Array(1)),false);
 release();await new Promise(resolve=>setTimeout(resolve,0));
 assert.equal(queueSummaryPacket({...cache,put:async()=>{}},'https://snoot.test/recovered',new Uint8Array(1)),true);
});
