export type ConfigurationCategory = 'compression' | 'staging' | 'indexing' | 'replication' | 'transforms';
export type ConfigurationField = {key: string; label: string; type: 'number' | 'boolean' | 'select' | 'text'; help: string; fallback?: string | number | boolean; options?: string[]; min?: number; max?: number; resourceOnly?: boolean};
export const configurationCategories: {id: ConfigurationCategory; label: string; fields: ConfigurationField[]}[] = [
  {id: 'compression', label: 'Compression', fields: [
    {key: 'pac.gzip_level', label: 'Seed Pack Compression', type: 'number', min: 1, max: 9, fallback: 1, help: 'Gzip effort for newly packed seeds. Higher levels trade sealing time for smaller storage.'},
    {key: 'summary.gzip_level', label: 'Summary Compression', type: 'number', min: 1, max: 9, fallback: 6, help: 'Gzip effort for new daily columnar aggregate packets.'},
  ]},
  {id: 'staging', label: 'Staging', fields: [
    {key: 'staging.max_bytes', label: 'Pending Seed Bytes', type: 'number', min: 0, fallback: 0, resourceOnly: true, help: 'Memory staging limit across the resource’s branches and definitions. Zero means unlimited.'},
    {key: 'staging.max_records', label: 'Pending Seed Count', type: 'number', min: 0, fallback: 0, resourceOnly: true, help: 'Maximum pending seeds before new ingress is rejected for retry. Zero means unlimited.'},
    {key: 'staging.max_disk_bytes', label: 'Staging Disk Bytes', type: 'number', min: 0, fallback: 0, resourceOnly: true, help: 'Disk staging limit; acknowledged seeds remain intact. Zero means unlimited.'},
  ]},
  {id: 'indexing', label: 'Indexing & Replay', fields: [
    {key: 'search_index', label: 'Search Index', type: 'boolean', help: 'Control Tantivy indexing for this scope.'},
    {key: 'object_index', label: 'Collection Index', type: 'boolean', resourceOnly: true, help: 'Enable object history indexing where the definition supplies object identity. Enabling may require backfill.'},
    {key: 'cache_invalidation', label: 'Cache Invalidation', type: 'boolean', help: 'Configure object cache invalidation for collection replay.'},
  ]},
  {id: 'replication', label: 'Replication', fields: [
    {key: 'replication.index_period', label: 'Index Period', type: 'select', options: ['1h', '4h', '6h', '12h', '24h', 'day'], fallback: '1h', help: 'Period used for index replication packages.'},
    {key: 'replication.population', label: 'Population', type: 'select', options: ['lazy'], fallback: 'lazy', help: 'Fetch seed detail on demand.'},
    {key: 'replication.provisional_tail', label: 'Late Arrival Window', type: 'text', fallback: '15m', help: 'Keep the recent tail provisional for late arrivals. Duration such as 15m or 1h; minimum 15 minutes.'},
  ]},
  {id: 'transforms', label: 'Transforms', fields: []},
];
export type ConfigurationSnapshot = {
  scopes: string[][]; scope: string[]; file: string; revision: string; exists: boolean; inherit: boolean;
  local: Record<string, unknown>; effective: Record<string, unknown>;
  transforms: {definition: string; source: string}[];
};
export async function requestConfiguration(token: string, scope: string[], signal?: AbortSignal, change?: {revision: string; category: ConfigurationCategory; values: Record<string, unknown>}): Promise<ConfigurationSnapshot> {
  const headers: Record<string, string> = {Accept: 'application/json'};
  if (token.trim()) headers.Authorization = `Bearer ${token.trim()}`;
  if (change) {headers['Content-Type'] = 'application/json'; headers['If-Match'] = `"${change.revision}"`;}
  const response = await fetch(`/configurations?${new URLSearchParams({scope: JSON.stringify(scope)})}`, {
    method: change ? 'PUT' : 'GET', headers, signal, cache: 'no-store',
    body: change ? JSON.stringify({category: change.category, values: change.values}) : undefined,
  });
  if (!response.ok) throw new Error((await response.text()).trim() || 'Configuration request failed.');
  return response.json();
}
