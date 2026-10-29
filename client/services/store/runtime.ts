import type { Command, ArchiveReply, Collection, NativeSelection } from './protocol.ts';
import type { Metadata } from '../pil/metadata.ts';
import { directory } from './files.ts';
import { localStorageRoot } from './io-error.ts';
import { nativeBackend } from './native-files.ts';
import { NativeStore } from './native.ts';
import { nativeTransaction } from './transaction.ts';
import type { NativeSession } from './native.ts';
import { conditions } from './conditions.ts';
import {saveTrunk,trunkDefinitions} from './trunk.ts';
export { conditions } from './conditions.ts';

// Page working sets are session state. Durable event availability is exclusively
// the committed branch trie; there is no per-event browser database or a separate record log.
export function createArchive(_modulePromise?:Promise<WebAssembly.Module>) {
 const sessions=new Map<string,NativeSession>();
 const definitions=new Map<string,Map<string,Metadata>>();
 const collections=new Map<string,Collection>();
 const selections=new Map<string,NativeSelection[]>();
 return async(namespace:string,command:Command):Promise<ArchiveReply>=>{
  const root=await directory(await localStorageRoot(),'snoot-store',namespace);
  const backend=nativeBackend(root),store=new NativeStore(backend),key=(id:string)=>`${namespace}:${id}`;
  switch(command.kind){
   case 'summary-read':return {summaryBytes:await store.summaryBytes(command.query)};
   case 'summary-save':await nativeTransaction(backend,b=>new NativeStore(b).importSummary(command.input));return {};
   case 'time-read':return {time:await store.timeWindow(command.query)};
   case 'time-save':await nativeTransaction(backend,b=>new NativeStore(b).importTime(command.input));return {};
   case 'trunk-save':await saveTrunk(root,command.snapshot);return {};
   case 'native-rebase': await store.rebaseMany(command.sources);await store.maintain();return {};
   case 'native-resolve':return {nativeDependencies:await store.resolveHour(command)};
   case 'native-coverage':return {nativeCoverage:await store.coverage(command.tap)};
   case 'native-object-state':return {nativeObjectState:await store.objectState(command.query)};
   case 'native-dependencies':return {nativeDependencies:await store.dependencies(command.source)};
   case 'native-page':return {nativePage:await store.page(command.source,command.from,command.to,command.after,command.limit,command.descending)};
   case 'native-rows':return {nativePage:await store.records(command.source,command.from,command.to,command.rows)};
   case 'native-begin':
    if(sessions.has(key(command.session)))throw Error('Duplicate collection session');
    if(sessions.size>=128)throw Error('Too many native responses');sessions.set(key(command.session),store.begin(await store.serial()));return {};
   case 'native-abort':sessions.delete(key(command.session));return {};
   case 'native-records':{
    const s=sessions.get(key(command.session));if(!s)throw Error('Unknown collection session');
    await nativeTransaction(backend,async b=>{const staged=new NativeStore(b);for(const item of command.records)await staged.record(s,item.record,item.definition);});return {};
   }
   case 'native-record':{
    const s=sessions.get(key(command.session));if(!s)throw Error('Unknown collection session');
    await store.record(s,command.record,command.definition);return {};
   }
   case 'native-finish':{
    const s=sessions.get(key(command.session));if(!s)throw Error('Unknown collection session');
    await nativeTransaction(backend,async b=>{const staged=new NativeStore(b);for(const item of command.records??[])await staged.record(s,item.record,item.definition);await staged.finish(s);});await store.maintain();
    if(command.collection&&command.selection){
     const where=conditions(command.selection);if(!where)throw Error('Unsupported local predicate');
     selections.set(key(command.collection),s.packages.filter(p=>!p.manifest.Provisional&&!p.manifest.TimeRetired).map(p=>({source:p.key,from:p.manifest.From*1000,to:p.manifest.To*1000,where})));
    }
    sessions.delete(key(command.session));return {nativeSources:[...new Set(s.packages.map(p=>p.key))]};
   }
   case 'definitions':{
    const all=new Map(definitions.get(namespace));for(const d of [...await trunkDefinitions(root),...await store.definitions()])all.set(d.digest,d);return {definitions:[...all.values()]};
   }
   case 'remember':{let known=definitions.get(namespace);if(!known){known=new Map();definitions.set(namespace,known);}for(const d of command.definitions)known.set(d.digest,d);return {};}
   case 'begin':collections.set(key(command.collection.id),command.collection);return {};
   case 'restore':{
    const collection=command.collection?collections.get(key(command.collection)):undefined;
    if(!collection)return {done:true};
    return {collection,cursor:key(collection.id),definitions:await store.definitions(),bindings:[],coverage:[],nativeSelections:await store.activeSelections(selections.get(key(collection.id))??[])};
   }
   // Live rows are transient; sealed hours arrive through the native PAC path.
   case 'save':return {batch:command.batch};
   case 'lookup':case 'finish':case 'release':return {};
   case 'next':return {done:true};
  }
 };
}
