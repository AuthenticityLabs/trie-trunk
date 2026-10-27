import type { Page } from '../components/navigation';
import type { Parameters } from './search';
import type { ViewQuery } from './pil/view';
import { normalizePosition, type ScrollPosition } from './ui-position.ts';
export type TabState = {
 queryText?:string; version:1; page:Page; collapsed:boolean; tap:string; range:string; from:string; to:string;
 localParameters:Parameters; localTap:string; localFrom:string; localTo:string;
 searchView?:'events'|'charts'; query?:ViewQuery; pageSize:number; scroll:number; positions?:Partial<Record<Page,ScrollPosition>>;
};
export function restoreWorkspace(value:unknown):TabState|undefined {
 if(!value||typeof value!=='object')return;
 const state=value as TabState;
 if(!state||state.version!==1||!['Search','Dashboards','Alerts','Collections','Configs'].includes(state.page)||
  (state.queryText!==undefined&&typeof state.queryText!=='string')||
  (state.searchView!==undefined&&!['events','charts'].includes(state.searchView))||
  typeof state.collapsed!=='boolean'||
  !Number.isInteger(state.pageSize)||state.pageSize<1||state.pageSize>1000||
  !['tap','range','from','to','localTap','localFrom','localTo'].every(k=>typeof state[k as keyof TabState]==='string')||
  !state.localParameters||Object.values(state.localParameters).some(v=>typeof v!=='string')||
  (state.query&&(!Number.isFinite(Date.parse(state.query.from))||!Number.isFinite(Date.parse(state.query.to))||typeof state.query.tap!=='string')))return;
 // Scroll geometry is disposable UI state. Safari overscroll and a shrinking
 // result pane must never invalidate the selected window or submitted query.
 const positions:Partial<Record<Page,ScrollPosition>>={};
 if(state.positions&&typeof state.positions==='object')for(const [page,position] of Object.entries(state.positions)){
  const restored=normalizePosition(position);if(restored)positions[page as Page]=restored;
 }
 return {...state,positions,scroll:Number.isFinite(state.scroll)?Math.max(0,state.scroll):0};
}
