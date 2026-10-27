import {test} from 'node:test';
import assert from 'node:assert/strict';
import {SummaryReplication, summaryQuery, summaryWindows} from './summary-replication.ts';
const now = Date.parse('2026-10-26T17:00:00Z');
test('year-to-date windows cover exactly UTC history, newest first, without gaps', () => {
 const windows = summaryWindows(now);
 assert.equal(windows[0].to, new Date(now).toISOString());
 assert.equal(windows.at(-1).from, '2026-01-01T00:00:00.000Z');
 windows.forEach((w,i) => {
  assert.ok(Date.parse(w.to) > Date.parse(w.from));
  assert.ok(Date.parse(w.to) - Date.parse(w.from) <= 7*86400000);
  assert.equal(Date.parse(w.from)%86400000,0);
  if (i) assert.equal(w.to, windows[i-1].from);
 });
 assert.deepEqual(summaryWindows(Date.parse('2026-01-01T00:00:00Z')), []);
 assert.equal(summaryWindows(Date.parse('2024-12-31T23:00:00Z')).at(-1).from,'2024-01-01T00:00:00.000Z');
 assert.equal(summaryWindows(now,true).at(-1).from,'2026-10-25T00:00:00.000Z');
});
test('subscription requests select a service and summaries only', () => {
 const query = summaryQuery('prod/payment', summaryWindows(now)[0]);
 assert.equal(query.tap,'prod/payment/*/*');assert.equal(query.include,'summary');assert.equal(query.size,1);
 assert.throws(()=>summaryQuery('prod/payment/transactions.v1',summaryWindows(now)[0]));
});
test('tabs share priming work and service queues never overlap windows', async () => {
 let calls=0,active=0,max=0;
 const collect = new SummaryReplication(async () => {
  calls++; active++; max=Math.max(max,active);
  await new Promise(resolve=>setTimeout(resolve,1)); active--; return 2;
 });
 const a=[],b=[],signal=new AbortController().signal;
 await Promise.all([
  collect.collect('prod/payment',now,true,signal,p=>a.push(p)),
  collect.collect('prod/payment',now,true,signal,p=>b.push(p)),
  collect.collect('prod/identity',now,true,signal,()=>{}),
 ]);
 assert.equal(calls,2); assert.equal(max,1); assert.deepEqual(a.at(-1),b.at(-1));
 assert.deepEqual(a.at(-1),{completed:1,total:1,packets:2});
});
test('cancelling one tab does not abort another; cancelling all stops the job', async () => {
 let child;
 const collect=new SummaryReplication(async (_,signal)=>{
  child=signal;
  await new Promise((resolve,reject)=>{const t=setTimeout(resolve,20);signal.addEventListener('abort',()=>{clearTimeout(t);reject(signal.reason)},{once:true});});return 1;
 });
 const a=new AbortController(), b=new AbortController();
 const first=collect.collect('prod/payment',now,true,a.signal,()=>{});
 const second=collect.collect('prod/payment',now,true,b.signal,()=>{});
 await Promise.resolve(); a.abort(); await assert.rejects(first); assert.equal(child.aborted,false);
 await second;
 const c=new AbortController();const last=collect.collect('prod/payment',now,true,c.signal,()=>{});
 await Promise.resolve(); c.abort(); await assert.rejects(last); assert.equal(child.aborted,true);
});
test('a failed window stops without success and can be retried', async () => {
 let fail=true;
 const collect=new SummaryReplication(async ()=>{if(fail)throw Error('Offline');return 1});
 await assert.rejects(collect.collect('prod/payment',now,true,new AbortController().signal,()=>{}),/Offline/);
 fail=false;
 await collect.collect('prod/payment',now,true,new AbortController().signal,()=>{});
});
