import type { Binding } from './metadata.ts';
import type { ViewBucket, ViewReference } from './view.ts';

type Entry = { bucket:ViewBucket; source:string; digest:string; count:number; rows:Map<number,string> };
/** Logical PIL coverage. Never advertise it as native byte-range replication. */
export class ViewCoverage {
 private buckets=new Map<string,Entry>();
 private owners=new Map<string,Map<string,number>>();
 validate(buckets:ViewBucket[],bindings:Binding[],refs:ViewReference[]) {
  const hashes=new Set<string>();
  for(const b of buckets){
   if(!b||! /^[a-f0-9]{64}$/.test(b.key)||hashes.has(b.key)||!bindings.some(s=>s.stream===b.stream)||
    !Number.isSafeInteger(b.from)||!Number.isSafeInteger(b.to)||b.from<0||b.to<=b.from||typeof b.sealed!=='boolean'||
    !Array.isArray(b.ranges)||!b.ranges.length||b.ranges.length>10000)throw Error('Invalid bucket manifest');
   hashes.add(b.key);
   let end=-1,count=0;
   for(const r of b.ranges){
    if(!Number.isSafeInteger(r.Start)||!Number.isSafeInteger(r.End)||r.Start<0||r.End<=r.Start||r.Start<end)throw Error('Invalid bucket rows');
    end=r.End;count+=r.End-r.Start;
   }
   if(!Number.isSafeInteger(count))throw Error('Invalid bucket count');
   const binding=bindings.find(s=>s.stream===b.stream)!;
   const entry=this.buckets.get(b.key);
   if(entry&&(entry.source!==binding.tap||entry.digest!==binding.digest||entry.bucket.from!==b.from||entry.bucket.to!==b.to||JSON.stringify(entry.bucket.ranges)!==JSON.stringify(b.ranges)))throw Error('Changed bucket identity');
   for(const ref of refs){
    const old=!ref.provisional&&ref.stream===b.stream?entry?.rows.get(ref.row):undefined;
    if(old&&old!==ref.key)throw Error('Changed bucket row identity');
   }
  }
  if(buckets.length && refs.some(ref=>buckets.filter(b=>b.stream===ref.stream&&b.ranges.some(r=>ref.row>=r.Start&&ref.row<r.End)).length!==(ref.provisional?0:1)))
   throw Error('Missing or overlapping event bucket');
 }
 add(buckets:ViewBucket[],bindings:Binding[],refs:ViewReference[],has:(key:string)=>boolean){
  this.validate(buckets,bindings,refs);
  for(const b of buckets){
   const binding=bindings.find(s=>s.stream===b.stream)!;
   let entry=this.buckets.get(b.key);
   if(entry&&(entry.source!==binding.tap||entry.digest!==binding.digest||entry.bucket.from!==b.from||entry.bucket.to!==b.to||JSON.stringify(entry.bucket.ranges)!==JSON.stringify(b.ranges)))throw Error('Changed bucket identity');
   if(!entry){
    for(const [key,old] of this.buckets)if(key!==b.key&&old.source===binding.tap&&old.digest===binding.digest&&old.bucket.from===b.from&&old.bucket.to===b.to)this.drop(key);
    entry={bucket:b,source:binding.tap,digest:binding.digest,count:b.ranges.reduce((n,r)=>n+r.End-r.Start,0),rows:new Map()};this.buckets.set(b.key,entry)}
   entry.bucket={...b,sealed:b.sealed||entry.bucket.sealed};
   for(const ref of refs){
    if(ref.provisional||ref.stream!==b.stream||!b.ranges.some(r=>ref.row>=r.Start&&ref.row<r.End)||!has(ref.key))continue;
    const old=entry.rows.get(ref.row);
    if(old&&old!==ref.key)throw Error('Changed bucket row identity');
    entry.rows.set(ref.row,ref.key);
    let owners=this.owners.get(ref.key);if(!owners){owners=new Map();this.owners.set(ref.key,owners)}
    owners.set(b.key,ref.row);
   }
  }
  while(this.buckets.size>4096)this.drop(this.buckets.keys().next().value!);
 }
 private drop(key:string){
  const entry=this.buckets.get(key);if(!entry)return;
  for(const event of entry.rows.values()){
   const owners=this.owners.get(event);owners?.delete(key);if(!owners?.size)this.owners.delete(event);
  }
  this.buckets.delete(key);
 }
 evict(key:string){
  const owners=this.owners.get(key);
  for(const [bucket,row] of owners??[])this.buckets.get(bucket)?.rows.delete(row);
  this.owners.delete(key);
 }
 known(tap:string,from:number,to:number):{keys:string[];events:Set<string>} {
  const keys:string[]=[],events=new Set<string>();
  // Most recently encountered buckets first; an omitted bucket costs bytes only.
  for(const [key,e] of [...this.buckets].reverse()){
   if(keys.length===120)break;
   if(!e.bucket.sealed||e.rows.size!==e.count||e.bucket.to<=from||e.bucket.from>=to)continue;
   if(tap&&e.source!==tap&&!e.source.startsWith(tap+'/'))continue;
   keys.push(key);for(const key of e.rows.values())events.add(key);
  }
  return {keys,events};
 }
 complete(){return [...this.buckets.values()].filter(e=>e.bucket.sealed&&e.rows.size===e.count).map(e=>({key:e.bucket.key,tap:e.source,from:e.bucket.from,to:e.bucket.to}));}
 status(tap='',from=-Infinity,to=Infinity){let complete=0,partial=0;for(const e of this.buckets.values()){
  if(e.bucket.to<=from||e.bucket.from>=to||(tap&&e.source!==tap&&!e.source.startsWith(tap+'/')))continue;
  if(e.bucket.sealed&&e.rows.size===e.count)complete++;else partial++}return {complete,partial}}
}
