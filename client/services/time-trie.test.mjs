import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {mergeTimeWindow,timeLeaf,missingHours,retireTime,HOUR,HEADER,YEAR_BYTES,DAY_BYTES,TIME_EVENTS,TIME_SEARCH,markTimeSearch} from './store/time-trie.ts';
const fixture=n=>new Uint8Array(readFileSync(new URL(`./fixtures/time-trie/${n}`,import.meta.url)));
const source=new Uint8Array(16);source[0]=1;
function part(a,z){
 const full=fixture('window.bin'),v=new DataView(full.buffer),from=Number(v.getBigUint64(64,true));
 const out=new Uint8Array(80+(z-a)*17);out.set(full.subarray(0,80));out.set(full.subarray(80+a*17,80+z*17),80);
 const w=new DataView(out.buffer);w.setBigUint64(64,BigInt(from+a*HOUR),true);w.setBigUint64(72,BigInt(from+z*HOUR),true);
 return out;
}
const start=Date.UTC(2026,0,1);
test('server and client share exact positional bytes; zero is complete and blank requests collection',()=>{
 let raw=mergeTimeWindow(undefined,part(3,4),source,true);
 assert.equal(timeLeaf(raw,start+3*HOUR).segments,1);
 assert.equal(timeLeaf(raw,start+2*HOUR),undefined);
 assert.deepEqual(missingHours(raw,start,start+5*HOUR),[{from:start,to:start+3*HOUR},{from:start+4*HOUR,to:start+5*HOUR}]);
 raw=mergeTimeWindow(raw,part(2,3),source,false);
 assert.equal(timeLeaf(raw,start+2*HOUR).state,1);
 assert.equal(missingHours(raw,start+2*HOUR,start+3*HOUR).length,1);
 for(const h of [1,4,0,2])raw=mergeTimeWindow(raw,part(h,h+1),source,true);
 assert.deepEqual(raw,fixture('server.tri'));
 assert.deepEqual(missingHours(raw,start,start+5*HOUR),[]);
 assert.equal(timeLeaf(raw,start).segments,0);
 assert.equal(timeLeaf(raw,start).state,2);
 assert.equal(raw.length,HEADER+YEAR_BYTES);
 // Entire unused day 366, including bitmap, stays null.
 const d=HEADER+365*DAY_BYTES;assert.ok(raw.subarray(d,d+DAY_BYTES).every(v=>v===0));
 assert.ok(raw.subarray(raw.length-24).every(v=>v===0));
});
test('retention blanks the head without changing retained addresses',()=>{
 const full=mergeTimeWindow(undefined,fixture('window.bin'),source,true);
 const tail=retireTime(full,start+2*HOUR);
 assert.equal(timeLeaf(tail,start),undefined);
 assert.deepEqual(timeLeaf(tail,start+3*HOUR),timeLeaf(full,start+3*HOUR));
 assert.deepEqual(missingHours(tail,start+2*HOUR,start+5*HOUR),[]);
 const next=retireTime(tail,Date.UTC(2027,0,1));assert.equal(next.length,HEADER+YEAR_BYTES);
 assert.equal(timeLeaf(next,Date.UTC(2027,0,1)),undefined);
});
test('invalid patches do not mutate committed completeness',()=>{
 const good=part(1,2),known=mergeTimeWindow(undefined,good,source,true),before=known.slice();
 const bad=good.slice();new DataView(bad.buffer).setUint32(80,0xffffffff,true);
 assert.throws(()=>mergeTimeWindow(known,bad,source,true),/extent/);
 assert.deepEqual(known,before);
 const wrong=source.slice();wrong[1]=1;assert.throws(()=>mergeTimeWindow(known,good,wrong,true),/source/);
});

test('a patch cannot rebase logical hour references',()=>{
 const known=mergeTimeWindow(undefined,part(1,2),source,true);
 const bad=part(3,4),v=new DataView(bad.buffer);
 v.setBigUint64(16,BigInt(start-HOUR),true);
 assert.throws(()=>mergeTimeWindow(known,bad,source,true),/binding changed/);
 assert.equal(timeLeaf(known,start+HOUR).start,0);
});

test('widened server columns preserve previously complete client hours',()=>{
 const narrow=part(3,4),widths=[5,6,6,3,3,3],wide=new Uint8Array(101);
 wide.set(narrow.subarray(0,80));wide.set(widths,40);
 let input=80,output=80;
 for(const [i,w] of [4,5,5,2].entries()){
  wide.set(narrow.subarray(input,input+w),output);
  input+=w;output+=widths[i];
 }
 wide[output]=narrow[input];
 // Exercise addresses above the original physical columns' limits.
 wide[84]=1;wide[90]=1;wide[96]=1;
 const v=new DataView(wide.buffer);v.setBigUint64(24,(1n<<32n)+100n,true);
 const old=mergeTimeWindow(undefined,part(1,2),source,true);
 const before=timeLeaf(old,start+HOUR);
 const merged=mergeTimeWindow(old,wide,source,true);
 assert.deepEqual(timeLeaf(merged,start+HOUR),before);
 const added=timeLeaf(merged,start+3*HOUR),original=timeLeaf(mergeTimeWindow(undefined,narrow,source,true),start+3*HOUR);
 assert.equal(added.start,original.start+2**32);
 assert.equal(added.pods,original.pods+2**40);
 assert.equal(added.sods,original.sods+2**40);
 assert.equal(added.state,2);
 assert.equal(merged.length,64+366*24*21);
 assert.deepEqual(missingHours(merged,start+HOUR,start+4*HOUR),[{from:start+2*HOUR,to:start+3*HOUR}]);
 assert.deepEqual(timeLeaf(old,start+HOUR),before);
});

test('transaction-owned trie updates match immutable merges without changing the published snapshot',()=>{
 const committed=mergeTimeWindow(undefined,part(0,1),source,true),before=committed.slice();
 let working=committed.slice(),immutable=committed;
 for(const h of [3,1,4,2]){
  const prior=working;
  working=mergeTimeWindow(working,part(h,h+1),source,true,false,true);
  immutable=mergeTimeWindow(immutable,part(h,h+1),source,true);
  assert.equal(working,prior,'unchanged allocation reuses only the private working copy');
  assert.deepEqual(working,immutable);
  assert.deepEqual(committed,before);
 }
 assert.deepEqual(working,fixture('server.tri'));
});

test('search-index replication never certifies event bytes and survives later event commits',()=>{
 const hour=start+HOUR,metadata=mergeTimeWindow(undefined,part(1,2),source,false),before=metadata.slice();
 const indexed=markTimeSearch(metadata,source,hour,hour+HOUR);
 assert.deepEqual(metadata,before,'marking returns an unpublished snapshot');
 assert.equal(timeLeaf(indexed,hour).state,1);
 assert.equal(timeLeaf(indexed,hour).flags,1|TIME_SEARCH);
 assert.deepEqual(missingHours(indexed,hour,hour+HOUR,TIME_SEARCH),[]);
 assert.deepEqual(missingHours(indexed,hour,hour+HOUR,TIME_EVENTS),[{from:hour,to:hour+HOUR}]);
 assert.deepEqual(missingHours(indexed,hour,hour+HOUR,TIME_EVENTS|TIME_SEARCH),[{from:hour,to:hour+HOUR}]);
 const complete=mergeTimeWindow(indexed,part(1,2),source,true);
 assert.equal(timeLeaf(complete,hour).flags,TIME_EVENTS|TIME_SEARCH);
 assert.deepEqual(missingHours(complete,hour,hour+HOUR,TIME_EVENTS|TIME_SEARCH),[]);
 const evicted=markTimeSearch(complete,source,hour,hour+HOUR,false);
 assert.equal(timeLeaf(evicted,hour).flags,TIME_EVENTS);
 assert.deepEqual(missingHours(evicted,hour,hour+HOUR),[]);
 assert.deepEqual(missingHours(evicted,hour,hour+HOUR,TIME_SEARCH),[{from:hour,to:hour+HOUR}]);
});

test('replication flags are bound to the physical source and immutable hour addresses',()=>{
 const hour=start+HOUR,metadata=mergeTimeWindow(undefined,part(1,2),source,false);
 const indexed=markTimeSearch(metadata,source,hour,hour+HOUR),wrong=source.slice();wrong[0]++;
 assert.throws(()=>markTimeSearch(indexed,wrong,hour,hour+HOUR),/source mismatch/);
 assert.throws(()=>markTimeSearch(indexed,source,start,start+HOUR),/no time trie leaf/);
 const changed=part(1,2);changed[84]++;
 assert.throws(()=>mergeTimeWindow(indexed,changed,source,true),/leaf changed/);
 assert.equal(timeLeaf(indexed,hour).flags,1|TIME_SEARCH);
 const retained=retireTime(indexed,hour);
 assert.equal(timeLeaf(retained,hour).flags,1|TIME_SEARCH);
 assert.equal(timeLeaf(retained,start),undefined);
});

test('one status byte identifies each hour without changing its neighbours',()=>{
 const all=mergeTimeWindow(undefined,fixture('window.bin'),source,true);
 const marked=markTimeSearch(all,source,start+HOUR,start+2*HOUR);
 assert.equal(timeLeaf(marked,start).flags,TIME_EVENTS);
 assert.equal(timeLeaf(marked,start+HOUR).flags,TIME_EVENTS|TIME_SEARCH);
 assert.equal(timeLeaf(marked,start+2*HOUR).flags,TIME_EVENTS);
 assert.equal(marked.length,HEADER+366*24*17);
 assert.throws(()=>timeLeaf(marked.subarray(0,marked.length-1),start),/extent/);
});

test('a remote availability flag cannot certify locally replicated search files',()=>{
 const patch=part(1,2);patch[patch.length-1]|=TIME_SEARCH;
 assert.throws(()=>mergeTimeWindow(undefined,patch,source,false),/Unsealed source hour/);
});
