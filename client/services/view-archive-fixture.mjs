import {readFileSync} from 'node:fs';
import {ViewCache as Base} from './pil/view.ts';
import {NativeStore} from './store/native.ts';
import {Backend} from './native-backend-fixture.mjs';

// Exercise sealed page fixtures through the real native store, including the
// additional whole-hour request. Page fixtures never synthesize full PILs.
export class ViewCache extends Base {
 constructor(fetcher) {
  super(async(url,opts)=>{
   if(!url.startsWith('/collect?'))return fetcher(url,opts);
   const from=new URL(url,'https://test').searchParams.get('from');
   const group=from.startsWith('2026-09')?'pac-buckets':'pac-live';
   const raw=readFileSync(new URL(`./fixtures/${group}/native.pil`,import.meta.url));
   return new Response(raw,{headers:{'Content-Type':'application/pil','PIL-Version':'2','Trunk-Format':'native-pac/0','Definition-Version':opts.headers['Definition-Version'],'Trunk-Completion':'body'}});
  });
  const store=new NativeStore(new Backend()),sessions=new Map();
  this.archive=async(token,c)=>{
   if(c.kind==='native-begin')sessions.set(c.session,store.begin(await store.serial()));
   for(const item of c.records??[])await store.record(sessions.get(c.session),item.record,item.definition);
  if(c.kind==='native-record')await store.record(sessions.get(c.session),c.record,c.definition);
   if(c.kind==='native-finish'){const tx=sessions.get(c.session);await store.finish(tx);sessions.delete(c.session);return {nativeSources:[...new Set(tx.packages.map(p=>p.key))]};}
   if(c.kind==='native-abort')sessions.delete(c.session);
   if(c.kind==='native-coverage')return {nativeCoverage:await store.coverage(c.tap)};
   if(c.kind==='native-dependencies')return {nativeDependencies:await store.dependencies(c.source)};
   if(c.kind==='native-rows')return {nativePage:await store.records(c.source,c.from,c.to,c.rows)};
   if(c.kind==='native-page')return {nativePage:await store.page(c.source,c.from,c.to,c.after,c.limit)};
   return {};
  };
 }
 query(q,token,signal,codec,definitions,archive=this.archive){return super.query(q,token,signal,codec,definitions,archive)}
}
