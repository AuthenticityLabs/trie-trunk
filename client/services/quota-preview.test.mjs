import test from 'node:test';
import assert from 'node:assert/strict';
import { previewQuota, storageDays } from './quota-preview.ts';
const samples = [
 {path:['prod','one'],hour:3000,bytes:5},
 {path:['prod','one'],hour:1000,bytes:3},
 {path:['prod','two'],hour:1000,bytes:7},
 {path:['test','one'],hour:2000,bytes:100},
];
test('quota preview scopes data and removes complete oldest hours', () => {
 const p=previewQuota(samples,['prod'],10);
 assert.equal(p.total,15);
 assert.equal(p.retained,5);
 assert.deepEqual([...p.retired],[1000]);
 assert.equal(previewQuota(samples,['prod','one'],5).reclaimed,3);
 assert.equal(previewQuota(samples,['prod'],15).reclaimed,0);
});
test('zero, empty and invalid previews are explicit', () => {
 assert.equal(previewQuota(samples,['prod'],0).retained,0);
 assert.equal(previewQuota(samples,['missing'],10).total,0);
 for(const n of [-1,NaN,Infinity]) assert.throws(()=>previewQuota(samples,[],n));
});
test('calendar aggregation uses UTC boundaries', () => {
 assert.deepEqual(storageDays([[Date.UTC(2026,9,1,23),5],[Date.UTC(2026,9,2),7]]),[['2026-10-01',5],['2026-10-02',7]]);
});
test('retirement batches trigger at capacity and free the configured headroom', () => {
 const data=[0,1,2,3].map(hour=>({path:['prod'],hour:hour*3600000,bytes:10}));
 assert.equal(previewQuota(data,['prod'],50,20).reclaimed,0);
 const full=previewQuota(data,['prod'],40,15);
 assert.equal(full.reclaimed,20); // Complete oldest hours round the batch upward.
 assert.deepEqual([...full.retired],[0,3600000]);
 assert.equal(previewQuota(data,['prod'],30,10).retained,20); // Includes overage.
 assert.equal(previewQuota(data,['prod'],40,40).retained,0);
 for(const amount of [-1,41,NaN,Infinity])assert.throws(()=>previewQuota(data,['prod'],40,amount));
});
test('recovery uses actual chronological distribution, including gaps', async () => {
 const {recoveryHours}=await import('./quota-preview.ts');
 assert.equal(recoveryHours([[0,10],[3*3600000,10]],5),.5);
 assert.equal(recoveryHours([[0,10],[3*3600000,10]],15),3.5);
 assert.equal(recoveryHours([],1),null);
 assert.equal(recoveryHours([[0,0]],1),null);
 assert.equal(recoveryHours([[0,10]],0),0);
 const history=Array.from({length:418*24},(_,hour)=>[hour*3600000,100]);
 assert.equal(recoveryHours(history,418*24*100*.25)/24,104.5);
 // A busy first hour contains 75% of storage: calendar span / 4 is wrong.
 assert.equal(recoveryHours([[0,75],[23*3600000,25]],25),1/3);
});
test('all views keep the original 28 ring paths stationary', async () => {
 const {quotaRings}=await import('../../shared/quota-rings.mjs');
 const half=quotaRings(50,25,100),full=quotaRings(100,100,100);
 assert.equal(half.length,28);
 assert.equal(full.length,28);
 assert.deepEqual(half.map(p=>p.d),full.map(p=>p.d));
 assert.equal(half.filter(p=>p.role==='bark').length,14);
 assert.equal(half.filter(p=>p.role==='emptyRing').length,7);
 assert.equal(full.filter(p=>p.role==='filledRing').length,28);
 assert.equal(quotaRings(100,0,100).filter(p=>p.role==='emptyRing').length,28);
 assert.deepEqual(quotaRings(100,200,100),full);
});
test('history must reference a physical catalog leaf, never field taxonomy',async()=>{
 const {validateStorage}=await import('./quota-preview.ts');
 const paths=[[],['demo'],['demo','svc'],['demo','svc','res'],['demo','svc','res','write']];
 const kinds=['trunk','cache','service','resource','branch'];
 const nodes=paths.map((path,i)=>({path,kind:kinds[i],allocated:1,logical:1,ownAllocated:0,ownLogical:0,files:1}));
 const snapshot={nodes,samples:[{path:paths[4],hour:0,bytes:1}],allocated:1,shared:0};
 assert.equal(validateStorage(snapshot),snapshot);
 assert.throws(()=>validateStorage({...snapshot,samples:[{path:['demo','svc','res','error','write'],hour:0,bytes:1}]}));
 assert.throws(()=>validateStorage({...snapshot,samples:[{path:['demo','svc','res','unregistered'],hour:0,bytes:1}]}));
 assert.throws(()=>validateStorage({...snapshot,nodes:nodes.slice(1)}));
});

test('recovery cannot infer GB from a tiny month of observed history', async () => {
 const {GB,recoveryHours,observedHistoryHours,durationLabel}=await import('./quota-preview.ts');
 const history=Array.from({length:30*24},(_,hour)=>[hour*3600000,1000*1024/(30*24)]);
 assert.equal(observedHistoryHours(history),30*24);
 assert.equal(recoveryHours(history,10.15*GB*.25),null);
 assert.equal(durationLabel(recoveryHours(history,10.15*GB*.25)),'Not Enough Data');
 // Shrinking the target to available bytes produces a supported estimate.
 assert.ok(Math.abs(recoveryHours(history,250*1024)-7.5*24)<1e-8);
 for (const target of [-1,NaN,Infinity]) assert.equal(recoveryHours(history,target),null);
 assert.equal(recoveryHours([],0),0);
 assert.equal(observedHistoryHours([]),null);
 assert.equal(observedHistoryHours([[0,0]]),null);
 assert.equal(recoveryHours([[0,10]],11),null);
 assert.equal(recoveryHours([[0,10]],10),1);
});

test('administrator storage labels and quota units use decimal bytes', async () => {
 const {storageLabel,GB}=await import('./quota-preview.ts');
 assert.equal(GB,1_000_000_000);
 assert.equal(storageLabel(1000),'1 kB');
 assert.equal(storageLabel(1_000_000),'1 MB');
 assert.equal(storageLabel(GB),'1 GB');
 assert.equal(storageLabel(1000*GB),'1 TB');
 assert.equal(storageLabel(1024*1024*1024),`${(1.073741824).toLocaleString(undefined,{maximumFractionDigits:2})} GB`);
 assert.equal(storageLabel(0),'0 B');
 assert.equal(storageLabel(.5),`${(.5).toLocaleString()} B`);
});

test('retained history pairs after-recovery and capacity windows from newest data', async () => {
 const {retainedHistory,retainedHistoryLabel,GB}=await import('./quota-preview.ts');
 const history=Array.from({length:100*24},(_,hour)=>[hour*3600000,GB]);
 const range=retainedHistory(history,100*24*GB,5*24*GB);
 assert.deepEqual(range,{afterRecovery:95*24,atCapacity:100*24});
 assert.equal(retainedHistoryLabel(range),'≈ 95–100 Days');
 assert.equal(retainedHistoryLabel(retainedHistory(history,100*24*GB,0)),'≈ 100 Days');
 assert.equal(retainedHistoryLabel(retainedHistory(history,100*24*GB,100*24*GB)),'≈ 0–100 Days');
 // Busy recent hour: retaining half the bytes is not half the calendar span.
 assert.deepEqual(retainedHistory([[0,25],[23*3600000,75]],100,50),{afterRecovery:2/3,atCapacity:24});
 assert.equal(retainedHistory(history,101*24*GB,GB),null);
 assert.equal(retainedHistory([],GB,GB/4),null);
 for(const values of [[-1,0],[100,101],[Infinity,1],[100,NaN]])assert.equal(retainedHistory(history,...values),null);
});

test('retention forecasting does not require a full quota', async () => {
 const {estimateRetainedHistory,retentionEstimateLabel,GB}=await import('./quota-preview.ts');
 const history=Array.from({length:30},(_,day)=>[day*86400000,GB]);
 const result=estimateRetainedHistory(history,100*GB,25*GB);
 assert.equal(result.status,'ready');
 assert.ok(result.range.atCapacity>result.range.afterRecovery);
 assert.ok(result.range.atCapacity>30*24);
 const tiny=history.map(([hour])=>[hour,1024000/30]);
 const extreme=estimateRetainedHistory(tiny,10.89*GB,10.89*GB*.25);
 assert.equal(extreme.status,'out-of-range');
 assert.ok(extreme.range.atCapacity>200000*24);
 assert.match(retentionEstimateLabel(extreme),/^≈ .*Months$/);
 assert.notEqual(retentionEstimateLabel(extreme),'Beyond Forecast Range');
 assert.equal(retentionEstimateLabel(estimateRetainedHistory([],GB,GB/4)),'Not Enough Data');
 assert.equal(estimateRetainedHistory(history,300*GB,0).status,'ready');
 assert.equal(estimateRetainedHistory(history,301*GB,0).status,'out-of-range');
});

test('forecast basis states decimal storage per day and recorded coverage', async () => {
 const {retentionBasisLabel}=await import('./quota-preview.ts');
 const hours=Array.from({length:48},(_,i)=>[i*3600000,2000/24]);
 assert.equal(retentionBasisLabel(hours),'Based on 2 kB/day across 2 days of recorded history.');
 assert.equal(retentionBasisLabel([]),'No recorded storage rate.');
});

test('extrapolation requires a week and 24 distinct populated hours, not a minimum byte volume', async () => {
 const {estimateRetainedHistory,retentionEstimateLabel}=await import('./quota-preview.ts');
 const hour=3_600_000;
 const burst=Array.from({length:24},(_,i)=>[i*hour,1]);
 const sparse=[[0,100],[167*hour,100]];
 const enough=Array.from({length:24},(_,i)=>[(i===23?167:i)*hour,1]);
 for(const history of [burst,sparse,enough.map(([time,bytes])=>[Math.min(time,166*hour),bytes])]) {
  const result=estimateRetainedHistory(history,1000,250);
  assert.equal(result.status,'insufficient');
  assert.equal(retentionEstimateLabel(result),'Not Enough Data');
 }
 // Duplicated samples from many branches do not provide more observed hours.
 assert.equal(estimateRetainedHistory(Array.from({length:24},()=>sparse).flat(),10000,0).status,'insufficient');
 assert.equal(estimateRetainedHistory(enough,240,60).status,'ready');
 assert.equal(estimateRetainedHistory(enough,241,60).status,'out-of-range');
 // A directly measured window remains useful even during the first hour.
 assert.deepEqual(estimateRetainedHistory([[0,100]],50,25),{
  status:'ready',range:{afterRecovery:.25,atCapacity:.5}
 });
});

test('quota estimates switch to months only above 365 days, using one unit for ranges', async () => {
 const {durationLabel,retainedHistoryLabel}=await import('./quota-preview.ts');
 const format=n=>n.toLocaleString(undefined,{maximumFractionDigits:1});
 assert.equal(durationLabel(23),'≈ 23 Hours');
 assert.equal(durationLabel(24),'≈ 1 Days');
 assert.equal(durationLabel(365*24),'≈ 365 Days');
 assert.equal(durationLabel(366*24),`≈ ${format(366*12/365)} Months`);
 assert.equal(durationLabel(730*24),'≈ 24 Months');
 assert.equal(retainedHistoryLabel({afterRecovery:365*24*.75,atCapacity:366*24}),`≈ 9–${format(366*12/365)} Months`);
 assert.equal(retainedHistoryLabel({afterRecovery:365*24,atCapacity:730*24}),'≈ 12–24 Months');
 assert.equal(durationLabel(null),'Not Enough Data');
});
