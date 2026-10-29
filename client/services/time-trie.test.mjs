import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {mergeTimeWindow,timeLeaf,missingHours,retireTime,markTimeFiles,timeFileRanges,HOUR,HEADER,YEAR_BYTES,DAY_BYTES,TIME_NONE,TIME_SUM,TIME_INDEX,TIME_STATE,TIME_INVERTED,TIME_SODS} from './store/time-trie.ts';
const fixture=n=>new Uint8Array(readFileSync(new URL(`./fixtures/time-trie/${n}`,import.meta.url)));
const source=new Uint8Array(16);source[0]=1;
const complete=TIME_INDEX|TIME_SODS,start=Date.UTC(2026,0,1);
function part(a,z){
 const full=fixture('window.bin'),out=new Uint8Array(80+(z-a)*16);
 out.set(full.subarray(0,80));out.set(full.subarray(80+a*16,80+z*16),80);
 const w=new DataView(out.buffer);w.setBigUint64(64,BigInt(start+a*HOUR),true);w.setBigUint64(72,BigInt(start+z*HOUR),true);return out;
}
function serverBytes(tree){const header=tree.bytes.slice(0,HEADER);header[46]=0;return Uint8Array.from([...header,...tree.bytes.subarray(HEADER).filter((_,i)=>i%17!==16)]);}

test('received time leaves start with no file bits, including known empty hours',()=>{
 const tree=mergeTimeWindow(undefined,part(0,1),source);
 assert.equal(timeLeaf(tree,start).count,0);
 assert.equal(timeLeaf(tree,start).flags,TIME_NONE);
 assert.equal(timeLeaf(tree,start+HOUR),undefined);
 assert.deepEqual(missingHours(tree,start,start+HOUR,TIME_NONE),[]);
 assert.deepEqual(missingHours(tree,start,start+HOUR,TIME_SODS),[{from:start,to:start+HOUR}]);
 assert.equal(tree.bytes.length,HEADER+YEAR_BYTES);
 assert.equal(fixture('server.tri').length,HEADER+366*24*16,'server has no client tail');
});
test('sparse time replication preserves server leaves and differentiates missing from empty',()=>{
 let tree=mergeTimeWindow(undefined,part(3,4),source,complete);
 assert.equal(timeLeaf(tree,start+3*HOUR).segments,1);
 assert.equal(timeLeaf(tree,start+2*HOUR),undefined);
 assert.deepEqual(missingHours(tree,start,start+5*HOUR),[{from:start,to:start+3*HOUR},{from:start+4*HOUR,to:start+5*HOUR}]);
 tree=mergeTimeWindow(tree,part(2,3),source);
 assert.equal(timeLeaf(tree,start+2*HOUR).flags,0);
 for(const h of [1,4,0,2])tree=mergeTimeWindow(tree,part(h,h+1),source,complete);
 assert.deepEqual(serverBytes(tree),fixture('server.tri'));
 assert.deepEqual(tree.ranges,[{from:start,to:start+5*HOUR}]);
 assert.deepEqual(missingHours(tree,start,start+5*HOUR),[]);
 const unused=HEADER+365*DAY_BYTES;assert.ok(tree.bytes.subarray(unused,unused+DAY_BYTES).every(v=>v===0));
});
test('retention removes sparse head leaves without changing surviving addresses or bits',()=>{
 const tree=mergeTimeWindow(undefined,fixture('window.bin'),source,complete),tail=retireTime(tree,start+2*HOUR);
 assert.equal(timeLeaf(tail,start),undefined);
 assert.deepEqual(timeLeaf(tail,start+3*HOUR),timeLeaf(tree,start+3*HOUR));
 assert.deepEqual(missingHours(tail,start+2*HOUR,start+5*HOUR),[]);
 const next=retireTime(tail,Date.UTC(2027,0,1));assert.equal(next.bytes.length,HEADER+YEAR_BYTES);
 assert.deepEqual(next.ranges,[]);
});
test('invalid patches and changed source bindings cannot mutate committed completeness',()=>{
 const good=part(1,2),tree=mergeTimeWindow(undefined,good,source,complete),before=structuredClone(tree);
 const bad=good.slice();new DataView(bad.buffer).setUint32(80,0xffffffff,true);
 assert.throws(()=>mergeTimeWindow(tree,bad,source,complete),/extent/);
 assert.deepEqual(tree,before);
 const wrong=source.slice();wrong[1]=1;assert.throws(()=>mergeTimeWindow(tree,good,wrong,complete),/source/);
 const rebased=part(3,4);new DataView(rebased.buffer).setBigUint64(16,BigInt(start-HOUR),true);
 assert.throws(()=>mergeTimeWindow(tree,rebased,source,complete),/binding changed/);
});
test('widened server address columns preserve received leaves and their completion bits',()=>{
 const narrow=part(3,4),widths=[5,6,6,3,3,3],wide=new Uint8Array(100);
 wide.set(narrow.subarray(0,80));wide.set(widths,40);let input=80,output=80;
 for(const [i,w] of [4,5,5,2].entries()){wide.set(narrow.subarray(input,input+w),output);input+=w;output+=widths[i];}
 wide[84]=1;wide[90]=1;wide[96]=1;
 new DataView(wide.buffer).setBigUint64(24,(1n<<32n)+100n,true);
 const old=mergeTimeWindow(undefined,part(1,2),source,complete),before=timeLeaf(old,start+HOUR);
 const merged=mergeTimeWindow(old,wide,source,complete),added=timeLeaf(merged,start+3*HOUR),original=timeLeaf(mergeTimeWindow(undefined,narrow,source,complete),start+3*HOUR);
 assert.deepEqual(timeLeaf(merged,start+HOUR),before);
 assert.equal(added.start,original.start+2**32);assert.equal(added.pods,original.pods+2**40);assert.equal(added.sods,original.sods+2**40);
 assert.equal(merged.bytes.length,64+366*24*21);
 assert.deepEqual(missingHours(merged,start+HOUR,start+4*HOUR),[{from:start+2*HOUR,to:start+3*HOUR}]);
 assert.deepEqual(timeLeaf(old,start+HOUR),before);
});
test('transaction-owned merges reuse only their detached copy',()=>{
 const published=mergeTimeWindow(undefined,part(0,1),source,complete),before=structuredClone(published);
 let working=structuredClone(published),immutable=published;
 for(const h of [3,1,4,2]){const prior=working;working=mergeTimeWindow(working,part(h,h+1),source,complete,false,true);immutable=mergeTimeWindow(immutable,part(h,h+1),source,complete);assert.equal(working,prior);assert.deepEqual(working,immutable);assert.deepEqual(published,before);}
 assert.deepEqual(serverBytes(working),fixture('server.tri'));
});
test('each replicated file uses an independent bit and later time refreshes preserve it',()=>{
 const hour=start+HOUR,metadata=mergeTimeWindow(undefined,part(1,2),source),before=structuredClone(metadata);
 let tree=metadata;
 for(const bit of [TIME_SUM,TIME_INDEX,TIME_STATE,TIME_INVERTED,TIME_SODS]){tree=markTimeFiles(tree,source,hour,hour+HOUR,bit);assert.deepEqual(missingHours(tree,hour,hour+HOUR,bit),[]);}
 assert.equal(timeLeaf(tree,hour).flags,31);assert.deepEqual(metadata,before);
 tree=mergeTimeWindow(tree,part(1,2),source);assert.equal(timeLeaf(tree,hour).flags,31);
 tree=markTimeFiles(tree,source,hour,hour+HOUR,TIME_SODS,false);
 assert.equal(timeLeaf(tree,hour).flags,15);assert.equal(missingHours(tree,hour,hour+HOUR).length,1);
 assert.deepEqual(missingHours(tree,hour,hour+HOUR,TIME_INVERTED),[]);
});
test('completion changes require received leaves and matching immutable source coordinates',()=>{
 const hour=start+HOUR,tree=mergeTimeWindow(undefined,part(1,2),source),wrong=source.slice();wrong[0]++;
 assert.throws(()=>markTimeFiles(tree,wrong,hour,hour+HOUR,TIME_INDEX),/source mismatch/);
 assert.throws(()=>markTimeFiles(tree,source,start,start+HOUR,TIME_INDEX),/No time trie leaf/);
 const changed=part(1,2);changed[84]++;
 assert.throws(()=>mergeTimeWindow(tree,changed,source,complete),/leaf changed/);
 assert.equal(timeLeaf(tree,hour).flags,0);
 assert.throws(()=>markTimeFiles(tree,source,hour,hour+HOUR,32),/requirement/);
});
test('one client tail byte updates only its selected hour',()=>{
 const all=mergeTimeWindow(undefined,fixture('window.bin'),source),marked=markTimeFiles(all,source,start+HOUR,start+2*HOUR,TIME_SUM|TIME_INVERTED);
 assert.equal(timeLeaf(marked,start).flags,0);assert.equal(timeLeaf(marked,start+HOUR).flags,9);assert.equal(timeLeaf(marked,start+2*HOUR).flags,0);
 assert.equal(marked.bytes.length,HEADER+366*24*17);
 assert.throws(()=>timeLeaf({...marked,bytes:marked.bytes.subarray(0,marked.bytes.length-1)},start),/extent/);
});
test('server ranges cannot carry or certify a client completion byte',()=>{
 const patch=part(1,2);patch[46]=1;assert.throws(()=>mergeTimeWindow(undefined,patch,source),/header/);
 const extra=new Uint8Array(patch.length+1);extra.set(part(1,2));extra[extra.length-1]=31;
 assert.throws(()=>mergeTimeWindow(undefined,extra,source),/patch/);
});
test('sparse publication skips the unused calendar day when a range crosses years',()=>{
 const patch=part(0,2),v=new DataView(patch.buffer),from=Date.UTC(2026,11,31,23),to=from+2*HOUR;
 for(const at of [8,16,64])v.setBigUint64(at,BigInt(from),true);
 for(const at of [32,72])v.setBigUint64(at,BigInt(to),true);
 const tree=mergeTimeWindow(undefined,patch,source),parts=[...timeFileRanges(tree)];
 assert.deepEqual(parts.map(p=>[p.at,p.bytes.length]),[[0,HEADER],[HEADER+(364*24+23)*17,17],[HEADER+366*24*17,17]]);
 assert.equal(timeLeaf(tree,from).count,0);assert.equal(timeLeaf(tree,from+HOUR).count,6);
});
