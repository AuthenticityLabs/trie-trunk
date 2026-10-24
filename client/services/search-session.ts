import type { ViewQuery, ViewResponse, ViewResult } from './pil/view.ts';
import type { Metadata } from './pil/metadata.ts';
import { viewEvents } from './pil/view-events.ts';

const root = 'snoot-search-v0';
export type { ScrollPosition as Position } from './ui-position.ts';
import { validPosition, type ScrollPosition as Position } from './ui-position.ts';
type SavedDefinition = { digest: string; store: string; pil: string };
type SavedPage = { native?:({row:string;base:number;trie:number}|null)[]; nativeTries?:{start:number;bytes:string}[][]; buckets?:ViewResult["buckets"]; bindings: ViewResponse['bindings']; refs: ViewResult['page']; bytes: string; rows: number[]; prefixes?: string[]; common?: number[] };
type Checkpoint = {
  version: 0; id: string; credential: string; input: ViewQuery; query: ViewQuery;
  pages: number; result: ViewResult; definitions: SavedDefinition[];
  position: Position; selected?: string;
};
const encode = (bytes: Uint8Array) => {
  let text = '';
  for (let at = 0; at < bytes.length; at += 8192)
    text += String.fromCharCode(...bytes.subarray(at, at + 8192));
  return btoa(text);
};
const decode = (text: string) => Uint8Array.from(atob(text), c => c.charCodeAt(0));
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

/** Append only new PIL pages. Small manifest updates never rewrite previous frames. */
export class SearchSession {
  private checkpoint?: Checkpoint;
  private storage: Storage | undefined;
  constructor(storage: Storage | undefined = tabStorage()) { this.storage = storage; }
  clear() {
    this.checkpoint = undefined;
    if (!this.storage) return;
    const keys: string[] = [];
    for (let i = 0; i < this.storage.length; i++) {
      const key = this.storage.key(i)!;
      if (key === root || key.startsWith(root + ':')) keys.push(key);
    }
    for (const key of keys) this.storage.removeItem(key);
  }
  restore(input: ViewQuery, credential: string) {
    const raw = this.storage?.getItem(root);
    if (!raw) return;
    const saved: Checkpoint = JSON.parse(raw);
    if (saved.version !== 0 || saved.credential !== credential || JSON.stringify(saved.input) !== JSON.stringify(input)) {
      this.clear(); return;
    }
    if (!Number.isSafeInteger(saved.pages) || saved.pages < 1 || !saved.id || !saved.result || !saved.query)
      throw Error('Invalid search checkpoint');
    const definitions: Metadata[] = saved.definitions.map(d => ({digest:d.digest, store:decode(d.store), pil:decode(d.pil)}));
    const events = [], keys: string[] = [], views:ViewResponse[]=[];
    for (let i = 0; i < saved.pages; i++) {
      const rawPage = this.storage!.getItem(`${root}:${saved.id}:${i}`);
      if (!rawPage) throw Error('Missing saved event page');
      const page: SavedPage = JSON.parse(rawPage);
      const bytes = decode(page.bytes);
      const rows = Uint32Array.from(page.rows);
      if (page.rows.some((n, i) => !Number.isSafeInteger(n) || rows[i] !== n)) throw Error('Invalid saved row');
      const prefixes = (page.prefixes ?? []).map(decode);
      if (page.common && (page.common.length !== rows.length / 3 || page.common.some(n => !Number.isInteger(n) || n < -1 || n >= prefixes.length)))
        throw Error('Invalid saved PAC prefix');
      const nativeTries=page.nativeTries?.map(trie=>trie.map(t=>({start:t.start,bytes:decode(t.bytes)})));
      if(page.native && (page.native.length!==rows.length/3 || page.native.some(n=>n &&
        (!Number.isSafeInteger(n.base)||!Number.isInteger(n.trie)||n.trie<0||!nativeTries?.[n.trie]||decode(n.row).length!==16))))
        throw Error('Invalid saved native reference');
      const view={native:page.native?.map(n=>n?{row:decode(n.row),base:n.base,trie:nativeTries![n.trie]}:undefined),definitions,bindings:page.bindings,result:{...saved.result,page:page.refs,buckets:page.buckets},batch:{bytes:bytes.buffer,rows,common:page.common?.map(p=>p===-1?undefined:prefixes[p])}};
      events.push(...viewEvents(view));views.push(view);
      keys.push(...page.refs.map(ref => ref.key));
    }
    if (new Set(keys).size !== keys.length || !validPosition(saved.position))
      throw Error('Invalid saved search');
    this.checkpoint = saved;
    return { ...saved, events, keys, views };
  }
  append(input: ViewQuery, query: ViewQuery, credential: string, view: ViewResponse) {
    if (!this.storage) throw Error('Session storage unavailable');
    const previous = this.checkpoint;
    if (previous && previous.result.snapshot !== view.result.snapshot) throw Error('Search snapshot changed');
    const definitions = new Map((previous?.definitions ?? []).map(d => [d.digest,d]));
    for (const d of view.definitions) if (!definitions.has(d.digest))
      definitions.set(d.digest,{digest:d.digest,store:encode(d.store),pil:encode(d.pil)});
    const next: Checkpoint = {
      version:0, id:previous?.id ?? crypto.randomUUID(), credential, input, query,
      pages:(previous?.pages ?? 0)+1, result:view.result, definitions:[...definitions.values()],
      position:previous?.position ?? {top:0,left:0}, selected:previous?.selected,
    };
    const prefixes: string[] = [], seen = new Map<Uint8Array, number>();
    const common = view.batch.common?.map(p => {
      if (!p?.length) return -1;
      const previous = seen.get(p);
      if (previous !== undefined) return previous;
      const encoded = encode(p);
      let at = prefixes.indexOf(encoded);
      if (at === -1) { at = prefixes.length; prefixes.push(encoded); }
      seen.set(p, at); return at;
    });
    const nativeTries:NonNullable<SavedPage['nativeTries']>=[],trieIds=new Map<object,number>();
    const native=view.native?.map(n=>{if(!n)return null;let at=trieIds.get(n.trie);if(at===undefined){at=nativeTries.length;trieIds.set(n.trie,at);nativeTries.push(n.trie.map(t=>({start:t.start,bytes:encode(t.bytes)})))}return {row:encode(n.row),base:n.base,trie:at}});
    const page: SavedPage = {native,nativeTries,buckets:view.result.buckets,bindings:view.bindings,refs:view.result.page,bytes:encode(new Uint8Array(view.batch.bytes)),rows:Array.from(view.batch.rows),prefixes,common};
    const key = `${root}:${next.id}:${next.pages-1}`;
    try {
      this.storage.setItem(key,JSON.stringify(page));
      this.storage.setItem(root,JSON.stringify(next));
      this.checkpoint = next;
    } catch (error) {
      // Never leave an older cursor/row count claiming to be the current view.
      this.clear(); throw error;
    }
  }
  position(position: Position, selected?: string) {
    if (!this.checkpoint || !this.storage) return;
    this.checkpoint = {...this.checkpoint,position,selected};
    this.storage.setItem(root,JSON.stringify(this.checkpoint));
  }
}
