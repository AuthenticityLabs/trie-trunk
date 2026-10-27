import type { ViewQuery, ViewResponse } from './pil/view.ts';
export type { ScrollPosition as Position } from './ui-position.ts';
import { validPosition, type ScrollPosition as Position } from './ui-position.ts';
const root = 'snoot:search:workspace';
type Checkpoint = { credential:string; input:ViewQuery; query:ViewQuery; position:Position; selected?:string };
export async function credentialScope(token: string): Promise<string> {
  if (!token) return 'anonymous';
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(token));
  return Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, '0')).join('');
}
export function tabStorage(): Storage | undefined {
  try { return sessionStorage; } catch { return undefined; }
}
export function readTab<T>(key: string): T | undefined {
  try { const value = tabStorage()?.getItem(key); return value ? JSON.parse(value) : undefined; } catch { return undefined; }
}
export function writeTab(key: string, value: unknown): boolean {
  try { const storage = tabStorage(); if (!storage) return false; storage.setItem(key, JSON.stringify(value)); return true; } catch { return false; }
}
export function clearTabWorkspace(storage: Storage | undefined = tabStorage()): void {
  if (!storage) return;
  try {
    const keys: string[] = [];
    for (let i = 0; i < storage.length; i++) {
      const key = storage.key(i)!;
      if (key.startsWith('snoot:')) keys.push(key);
    }
    for (const key of keys) storage.removeItem(key);
  } catch { /* The in-memory workspace still closes if storage is unavailable. */ }
}

/** UI state only. Event bytes, definitions, tries and results remain out of session storage. */
export class SearchSession {
 private checkpoint?:Checkpoint;
 private storage:Storage|undefined;
 constructor(storage:Storage|undefined=tabStorage()){this.storage=storage;}
 clear(){this.checkpoint=undefined;this.storage?.removeItem(root);}
 restore(input:ViewQuery,credential:string){
  const raw=this.storage?.getItem(root);if(!raw)return;
  const saved=JSON.parse(raw) as Checkpoint;
  if(saved.credential!==credential||JSON.stringify(saved.input)!==JSON.stringify(input)){this.clear();return;}
  if(!saved.query||!validPosition(saved.position))throw Error('Invalid saved search settings');
  return this.checkpoint=saved;
 }
 append(input:ViewQuery,query:ViewQuery,credential:string,_view:ViewResponse){
  if(!this.storage)throw Error('Session storage unavailable');
  this.checkpoint={credential,input,query,position:this.checkpoint?.position??{top:0,left:0},selected:this.checkpoint?.selected};
  this.storage.setItem(root,JSON.stringify(this.checkpoint));
 }
 position(position:Position,selected?:string){
  if(!this.checkpoint||!this.storage)return;
  this.checkpoint={...this.checkpoint,position,selected};this.storage.setItem(root,JSON.stringify(this.checkpoint));
 }
}
