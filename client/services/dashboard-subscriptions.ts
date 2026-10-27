import { transact } from './database.ts';
import { validSource } from './source.ts';

export type SeedResource = { id: string; cache: string; service: string; resource: string; sources: string[] };
export type SeedService = { id: string; cache: string; service: string; resources: SeedResource[] };
export type SeedSubscription = { service: string; created: number };
export type DashboardState = { revision: number; subscriptions: SeedSubscription[]; service: string; drawerOpen: boolean };
export type DashboardChange =
  | { kind: 'subscribe'; services: string[] }
  | { kind: 'subscriptions'; add: string[]; remove: string[] }
  | { kind: 'remove'; service: string }
  | { kind: 'select'; service: string; closeDrawer?: boolean }
  | { kind: 'drawer'; open: boolean };
export const dashboardChangedEvent = 'snoot-dashboard-subscriptions-changed';
export const emptyDashboard = (): DashboardState => ({ revision: 0, subscriptions: [], service: '', drawerOpen: true });
const servicePattern = /^[a-z][a-z0-9._-]{0,127}\/[a-z][a-z0-9._-]{0,127}$/;

/** Resources and physical branches are discovered from the authorized catalog. */
export function seedDirectory(sources: readonly string[]): SeedResource[] {
  const resources = new Map<string, SeedResource>();
  for (const source of sources) {
    if (!validSource(source) || source.split('/').length !== 4) continue;
    const [cache, service, resource] = source.split('/'), id = `${cache}/${service}/${resource}`;
    const entry = resources.get(id) ?? { id, cache, service, resource, sources: [] };
    if (!entry.sources.includes(source)) entry.sources.push(source);
    resources.set(id, entry);
  }
  return [...resources.values()].sort((a, b) => a.id.localeCompare(b.id));
}
/** A service subscription covers all its current and future catalog resources. */
export function serviceDirectory(sources: readonly string[]): SeedService[] {
  const services = new Map<string, SeedService>();
  for (const resource of seedDirectory(sources)) {
    const id = `${resource.cache}/${resource.service}`;
    const entry = services.get(id) ?? { id, cache: resource.cache, service: resource.service, resources: [] };
    entry.resources.push(resource);
    services.set(id, entry);
  }
  return [...services.values()];
}
export function readDashboard(value: unknown): DashboardState {
  if (value === undefined) return emptyDashboard();
  const s = value as DashboardState;
  if (!s || !Number.isSafeInteger(s.revision) || s.revision < 0 || typeof s.drawerOpen !== 'boolean' || typeof s.service !== 'string' || !Array.isArray(s.subscriptions) ||
    s.subscriptions.some(v => !v || typeof v.service !== 'string' || !servicePattern.test(v.service) || !Number.isSafeInteger(v.created) || v.created < 0) ||
    new Set(s.subscriptions.map(v => v.service)).size !== s.subscriptions.length ||
    s.service && !s.subscriptions.some(v => v.service === s.service)) throw Error('Unable to read saved dashboard subscriptions.');
  return s;
}
export function changeDashboard(current: DashboardState, change: DashboardChange, now = Date.now()): DashboardState {
  const next = { ...current, revision: current.revision + 1, subscriptions: [...current.subscriptions] };
  switch (change.kind) {
    case 'subscriptions':
    case 'subscribe':
      if (change.kind === 'subscriptions') next.subscriptions = next.subscriptions.filter(s => !change.remove.includes(s.service));
      for (const service of change.kind === 'subscribe' ? change.services : change.add) {
        if (!servicePattern.test(service)) throw Error('Invalid service subscription.');
        if (!next.subscriptions.some(s => s.service === service)) next.subscriptions.push({ service, created: now });
      }
      break;
    case 'remove': next.subscriptions = next.subscriptions.filter(s => s.service !== change.service); break;
    case 'select': next.service = change.service; if (change.closeDrawer) next.drawerOpen = false; break;
    case 'drawer': next.drawerOpen = change.open; break;
  }
  if (next.service && !next.subscriptions.some(s => s.service === next.service)) next.service = '';
  return readDashboard(next);
}
// IDB is origin scoped. Until account identity is exposed, credentials partition
// local state without persisting the bearer token itself.
export async function dashboardPartition(token: string): Promise<string> {
  const hash = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(token.trim()));
  return 'dashboard-services:' + [...new Uint8Array(hash)].map(b => b.toString(16).padStart(2, '0')).join('');
}
const workspaceKey=(key:string)=>'snoot:dashboards:workspace:'+key;
function withWorkspace(profile:unknown,key:string):DashboardState {
 const settings={...emptyDashboard(),...(profile as Partial<DashboardState>??{})};
 let ui:Partial<DashboardState>={};try{ui=JSON.parse(sessionStorage.getItem(workspaceKey(key))??'{}');}catch{}
 const service=typeof ui.service==='string'&&settings.subscriptions.some(s=>s.service===ui.service)?ui.service:'';
 return readDashboard({...settings,service,drawerOpen:typeof ui.drawerOpen==='boolean'?ui.drawerOpen:true});
}
function saveWorkspace(key:string,state:DashboardState){sessionStorage.setItem(workspaceKey(key),JSON.stringify({service:state.service,drawerOpen:state.drawerOpen}));}
export async function loadDashboard(key:string):Promise<DashboardState>{
 return withWorkspace(await transact('preferences','readonly',s=>s.get(key)),key);
}
/** Subscriptions are profile preferences; selected service and drawer state belong to the tab. */
export async function saveDashboardChange(key:string,change:DashboardChange):Promise<DashboardState>{
 if(change.kind==='select'||change.kind==='drawer'){
  const next=changeDashboard(await loadDashboard(key),change);saveWorkspace(key,next);return next;
 }
 let next:DashboardState|undefined,failure:unknown;
 try{await transact('preferences','readwrite',(store,tx)=>{
  const request=store.get(key);request.onsuccess=()=>{try{
   next=changeDashboard(withWorkspace(request.result,key),change);
   store.put({revision:next.revision,subscriptions:next.subscriptions},key);
  }catch(error){failure=error;tx.abort();}};return request;
 });}catch(error){throw failure??error;}
 saveWorkspace(key,next!);return next!;
}
