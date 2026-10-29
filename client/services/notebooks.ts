import { decodeObject, encodeObject } from "./pil/control.ts";
import { atomicFile } from './store/native-files.ts';
import { directory } from './store/files.ts';
type Saved = { notebooks:Notebook[]; references:EventReference[] };
async function access<T>(change:boolean, action:(saved:Saved)=>T):Promise<T>{
 return navigator.locks.request('snoot-saved-collections',async()=>{
  const dir=await directory(await navigator.storage.getDirectory(),'snoot-collections');
  let saved:Saved={notebooks:[],references:[]};
  try{saved=decodeObject(new Uint8Array(await (await (await dir.getFileHandle('bookmarks.pil')).getFile()).arrayBuffer())) as Saved;}
  catch(error){if(!(error instanceof DOMException&&error.name==='NotFoundError'))throw error;}
  const result=action(saved);
  if(change)await atomicFile(dir,'bookmarks.pil',encodeObject(saved));
  return result;
 });
}
import { eventId, type LogEvent } from "./dictionary.ts";

export type Notebook = {
  id: string;
  name: string;
  createdAt: number;
  lastUsed: number;
};

export type EventReference = {
  notebook: string;
  tap: string;
  id: string;
  version: number;
  digest: string;
  savedAt: number;
};

function reference(notebook: string, event: LogEvent): EventReference {
  return {
    notebook,
    tap: event.dictionary.tap,
    id: eventId(event),
    version: event.dictionary.version,
    digest: event.dictionary.digest,
    savedAt: Date.now(),
  };
}

export async function listNotebooks():Promise<Notebook[]>{
 return access(false,s=>s.notebooks.sort((a,b)=>b.lastUsed-a.lastUsed||b.createdAt-a.createdAt||a.id.localeCompare(b.id)));
}
export async function savedNotebooks(event:LogEvent):Promise<Set<string>>{
 return access(false,s=>new Set(s.references.filter(r=>r.tap===event.dictionary.tap&&r.id===eventId(event)).map(r=>r.notebook)));
}
export async function createNotebook(name:string,event:LogEvent):Promise<Notebook>{
 const title=name.trim();if(!title)throw Error('Enter a notebook name.');
 return access(true,s=>{const now=Date.now(),n={id:crypto.randomUUID(),name:title,createdAt:now,lastUsed:now};s.notebooks.push(n);s.references.push(reference(n.id,event));return n;});
}
export async function saveToNotebook(notebook:string,event:LogEvent):Promise<void>{
 await access(true,s=>{const n=s.notebooks.find(n=>n.id===notebook);if(!n)throw Error('Unknown notebook');n.lastUsed=Date.now();
 const r=reference(notebook,event),i=s.references.findIndex(v=>v.notebook===notebook&&v.tap===r.tap&&v.id===r.id);
 if(i<0)s.references.push(r);else s.references[i]=r;});
}
