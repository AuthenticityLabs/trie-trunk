import {storageNodeName, compareStoragePaths} from './storage-labels.ts';
import {within, type StorageNode} from './quota-preview.ts';

export type Recovery = {unit: 'percent' | 'bytes'; value: number};
export type QuotaScope = {path: string[]; managed?: boolean; limitBytes?: number; recovery?: Recovery};
export type QuotaPolicy = {managed?: boolean; poolBytes: number | null; scopes: QuotaScope[]};
export type QuotaSettings = {
  policy: QuotaPolicy;
  revision: string;
  disk: {totalBytes: number; availableBytes: number; ownedBytes: number; maximumBytes: number};
  enforced: boolean;
};
export type QuotaNode = StorageNode & {id: string; name: string; bytes: number};
export const scopeKey = (path: string[]) => JSON.stringify(path);
export const scopeSetting = (policy: QuotaPolicy, path: string[]) => policy.scopes.find(s => scopeKey(s.path) === scopeKey(path));
export function isManaged(policy: QuotaPolicy, path: string[]) {
  return path.length ? scopeSetting(policy, path)?.managed ?? path.length === 4 : policy.managed ?? true;
}
export function configuredLimit(policy: QuotaPolicy, path: string[]) {
  if (!isManaged(policy, path)) return undefined;
  return path.length ? scopeSetting(policy, path)?.limitBytes : policy.poolBytes ?? undefined;
}
export function assignedBytes(policy: QuotaPolicy, path: string[]) {
  return policy.scopes.reduce((n, s) => n + (s.path.length === 4 && isManaged(policy, s.path) && within(s.path, path) ? s.limitBytes ?? 0 : 0), 0);
}
export function quotaBounds(policy: QuotaPolicy, path: string[], diskMaximum: number) {
  let maximum = path.length === 4 ? diskMaximum - assignedBytes(policy, []) + assignedBytes(policy, path) : diskMaximum;
  for (let depth = 0; depth < path.length; depth++) {
    const parent = path.slice(0, depth), limit = configuredLimit(policy, parent);
    if (limit !== undefined) {
      const available = path.length === 4 ? limit - assignedBytes(policy, parent) + assignedBytes(policy, path) : limit;
      maximum = Math.min(maximum, available);
    }
  }
  let minimum = path.length === 4 ? 0 : assignedBytes(policy, path);
  for (const setting of policy.scopes) {
    if (!within(setting.path, path) || !isManaged(policy, setting.path)) continue;
    if (setting.path.length > path.length) minimum = Math.max(minimum, setting.limitBytes ?? 0);
    if (setting.recovery?.unit === 'bytes') minimum = Math.max(minimum, setting.recovery.value);
  }
  return {minimum, maximum: Math.max(0, maximum)};
}
export function updateScope(policy: QuotaPolicy, path: string[], change: Partial<Omit<QuotaScope, 'path'>>) {
  const previous = scopeSetting(policy, path) ?? {path};
  const setting = {...previous, ...change};
  const scopes = policy.scopes.filter(s => scopeKey(s.path) !== scopeKey(path));
  if (setting.managed !== undefined || setting.limitBytes !== undefined || setting.recovery !== undefined) scopes.push(setting);
  return {...policy, scopes};
}
export function quotaNodes(inventory: StorageNode[]): QuotaNode[] {
  return inventory.map(node=>({...node,id:scopeKey(node.path),name:storageNodeName(node.path),bytes:node.allocated})).sort((a,b)=>compareStoragePaths(a.path,b.path));
}
// Intermediate nodes are exactly the sum of their physical leaves. Zero stays
// zero: visual minimums must never invent storage or distort the scale.
export function quotaFlow(nodes: QuotaNode[], policy: QuotaPolicy): Map<string, number> {
  const flow = new Map<string, number>();
  const allocated = configuredLimit(policy, []) !== undefined;
  for (const node of nodes.filter(n => n.kind === 'branch')) {
    const value = allocated ? configuredLimit(policy, node.path) ?? 0 : node.bytes;
    for (let depth = 0; depth <= node.path.length; depth++) {
      const key = scopeKey(node.path.slice(0, depth));
      flow.set(key, (flow.get(key) ?? 0) + value);
    }
  }
  return flow;
}
export async function requestQuotas(token: string, signal?: AbortSignal, saved?: QuotaSettings, policy?: QuotaPolicy): Promise<QuotaSettings> {
  const headers: Record<string, string> = {Accept: 'application/json'};
  if (token.trim()) headers.Authorization = `Bearer ${token.trim()}`;
  if (saved) {
    headers['Content-Type'] = 'application/json';
    headers['If-Match'] = `"${saved.revision}"`;
  }
  const response = await fetch('/storage/quotas', {method: saved ? 'PUT' : 'GET', headers, signal, cache: 'no-store', body: saved ? JSON.stringify(policy) : undefined});
  if (!response.ok) throw new Error(response.status === 412 ? 'Another administrator changed these settings. Reload before saving.' : (await response.text()).trim() || `Quota settings unavailable (${response.status}).`);
  return response.json();
}
