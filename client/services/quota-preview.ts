import {apiFetch} from './connections.ts';
// Read-only storage evaluation. No deletion or policy publication.
export type StorageSample = { path: string[]; hour: number; bytes: number };
export const GB = 1000 ** 3;
export function within(path: string[], scope: string[]) {
  return scope.every((part, index) => part === path[index]);
}
export function previewQuota(samples: StorageSample[], scope: string[], budget: number, retirementBytes = 0) {
  if (!Number.isFinite(retirementBytes) || retirementBytes < 0 || retirementBytes > budget) throw new RangeError('Invalid retirement amount');
  if (!Number.isFinite(budget) || budget < 0) throw new RangeError('Invalid quota preview');
  const hours = new Map<number, number>();
  for (const sample of samples) if (within(sample.path, scope)) {
    hours.set(sample.hour, (hours.get(sample.hour) ?? 0) + sample.bytes);
  }
  const ordered = [...hours].sort((a, b) => a[0] - b[0]);
  const total = ordered.reduce((sum, [, bytes]) => sum + bytes, 0);
  const target = total >= budget ? budget - retirementBytes : budget;
  let retained = total;
  const retired = new Set<number>();
  for (const [hour, bytes] of ordered) {
    if (retained <= target) break;
    retained -= bytes;
    retired.add(hour);
  }
  return { total, retained, reclaimed: total - retained, retired, hours: ordered };
}
export function storageDays(hours: [number, number][]) {
  const days = new Map<string, number>();
  for (const [hour, bytes] of hours) {
    const date = new Date(hour).toISOString().slice(0, 10);
    days.set(date, (days.get(date) ?? 0) + bytes);
  }
  return [...days];
}
export function storageLabel(bytes: number) {
  const units = ['B', 'kB', 'MB', 'GB', 'TB'];
  const i = bytes > 0 ? Math.max(0, Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1000)))) : 0;
  return `${(bytes / 1000 ** i).toLocaleString(undefined, {maximumFractionDigits: 2})} ${units[i]}`;
}
// Estimate against the actual recovery byte target, never a fraction of the
// sample history. Do not extrapolate unobserved future storage into a duration.
// Interpolate the boundary hour; the eventual recovery policy rounds whole hours.
export function recoveryHours(hours: [number, number][], recoveryBytes: number): number | null {
  if (!Number.isFinite(recoveryBytes) || recoveryBytes < 0) return null;
  if (recoveryBytes === 0) return 0;
  const ordered = hours.filter(([, bytes]) => bytes > 0).sort((a, b) => a[0] - b[0]);
  const total = ordered.reduce((sum, [, bytes]) => sum + bytes, 0);
  if (!total || recoveryBytes > total) return null;
  let remaining = recoveryBytes;
  for (const [hour, bytes] of ordered) {
    if (remaining <= bytes) return (hour - ordered[0][0]) / 3_600_000 + remaining / bytes;
    remaining -= bytes;
  }
  return observedHistoryHours(ordered);
}
// Model the newest retained window using recorded hourly byte density. A range
// needs enough history to cover the whole quota, not merely its recovery batch.
export function retainedHistory(hours: [number, number][], quotaBytes: number, recoveryBytes: number): {afterRecovery: number; atCapacity: number} | null {
  if (!Number.isFinite(quotaBytes) || quotaBytes < 0 || !Number.isFinite(recoveryBytes) || recoveryBytes < 0 || recoveryBytes > quotaBytes) return null;
  const newestFirst = hours.map(([hour, bytes]): [number, number] => [-hour, bytes]);
  const atCapacity = recoveryHours(newestFirst, quotaBytes);
  const afterRecovery = recoveryHours(newestFirst, quotaBytes - recoveryBytes);
  return atCapacity === null || afterRecovery === null ? null : {afterRecovery, atCapacity};
}
export type RetentionEstimate = {
  status: 'ready' | 'insufficient' | 'out-of-range';
  range: ReturnType<typeof retainedHistory>;
};
// Extrapolation needs a week of history and 24 distinct populated hours. This is
// a minimum evidence guardrail, not a statistical confidence guarantee. Direct
// windows covered by recorded bytes do not need a forecasting threshold.
// Flag extrapolation beyond 10x recorded history without hiding its range.
export function estimateRetainedHistory(hours: [number, number][], quotaBytes: number, recoveryBytes: number): RetentionEstimate {
  if (!Number.isFinite(quotaBytes) || quotaBytes < 0 || !Number.isFinite(recoveryBytes) || recoveryBytes < 0 || recoveryBytes > quotaBytes) return {status: 'insufficient', range: null};
  const exact = retainedHistory(hours, quotaBytes, recoveryBytes);
  if (exact) return {status: 'ready', range: exact};
  const span = observedHistoryHours(hours);
  const total = hours.reduce((sum, [, bytes]) => sum + Math.max(0, bytes), 0);
  if (!span || !total || !Number.isFinite(total)) return {status: 'insufficient', range: null};
  const populatedHours = new Set(hours.filter(([hour, bytes]) => Number.isFinite(hour) && bytes > 0).map(([hour]) => Math.floor(hour / 3_600_000)));
  if (span < 7 * 24 || populatedHours.size < 24) return {status: 'insufficient', range: null};
  const hoursPerByte = span / total;
  const range = {afterRecovery: (quotaBytes - recoveryBytes) * hoursPerByte, atCapacity: quotaBytes * hoursPerByte};
  return {status: quotaBytes > total * 10 ? 'out-of-range' : 'ready', range};
}
export function retentionEstimateLabel(estimate: RetentionEstimate): string {
  return retainedHistoryLabel(estimate.range);
}
export function retentionBasisLabel(hours: [number, number][]): string {
  const span = observedHistoryHours(hours);
  const bytes = hours.reduce((sum, [, value]) => sum + Math.max(0, value), 0);
  if (!span || !bytes || !Number.isFinite(bytes)) return 'No recorded storage rate.';
  const days = span / 24;
  const period = days >= 1
    ? `${days.toLocaleString(undefined, {maximumFractionDigits: 1})} days`
    : `${span.toLocaleString(undefined, {maximumFractionDigits: 1})} hours`;
  return `Based on ${storageLabel(bytes / days)}/day across ${period} of recorded history.`;
}
export function retainedHistoryLabel(range: ReturnType<typeof retainedHistory>): string {
  if (!range) return 'Not Enough Data';
  // Forecast months are average months (365 / 12 days), not calendar boundaries.
  // Use one unit for both ends so a range remains easy to compare.
  const months = range.atCapacity > 365 * 24, days = range.atCapacity >= 24;
  const unit = months ? 'Months' : days ? 'Days' : 'Hours';
  const divisor = months ? 365 * 24 / 12 : days ? 24 : 1;
  const format = (hours: number) => (hours / divisor).toLocaleString(undefined, {maximumFractionDigits: 1});
  const lower = format(range.afterRecovery), upper = format(range.atCapacity);
  return `≈ ${lower === upper ? upper : `${lower}–${upper}`} ${unit}`;
}
export function observedHistoryHours(hours: [number, number][]): number | null {
  let first = Infinity, last = -Infinity;
  for (const [hour, bytes] of hours) if (bytes > 0) {first = Math.min(first, hour); last = Math.max(last, hour);}
  return first === Infinity ? null : (last - first) / 3_600_000 + 1;
}
export function durationLabel(hours: number | null) {
  return retainedHistoryLabel(hours === null ? null : {afterRecovery: hours, atCapacity: hours});
}
export type StorageNode={path:string[];kind:'trunk'|'cache'|'service'|'resource'|'branch';allocated:number;logical:number;ownAllocated:number;ownLogical:number;files:number};
export type StorageSnapshot={nodes:StorageNode[];samples:StorageSample[];allocated:number;logical:number;shared:number;unattributed:number;captured:number};
export function validateStorage(data: StorageSnapshot) {
 const kinds=['trunk','cache','service','resource','branch'];
 const amount=(n:number)=>Number.isSafeInteger(n)&&n>=0;
 if(!Array.isArray(data.nodes)||!Array.isArray(data.samples)||!amount(data.allocated)||!amount(data.shared))throw new Error('Invalid storage inventory.');
 const nodes=new Map<string,StorageNode>();
 for(const n of data.nodes){
  if(!Array.isArray(n.path)||n.path.length>4||!n.path.every(p=>typeof p==='string'&&p.length>0)||n.kind!==kinds[n.path.length]||![n.allocated,n.logical,n.ownAllocated,n.ownLogical,n.files].every(amount)||nodes.has(JSON.stringify(n.path)))throw new Error('Invalid physical storage node.');
  nodes.set(JSON.stringify(n.path),n);
 }
 if(!nodes.has('[]'))throw new Error('Missing Trunk inventory.');
 for(const n of data.nodes)if(n.path.length&&!nodes.has(JSON.stringify(n.path.slice(0,-1))))throw new Error('Missing storage parent.');
 for(const sample of data.samples){
  if(nodes.get(JSON.stringify(sample.path))?.kind!=='branch'||!Number.isFinite(sample.hour)||!Number.isFinite(sample.bytes)||sample.bytes<0)throw new Error('History references an unknown physical branch.');
 }
 return data;
}
export async function loadStorage(token:string,signal:AbortSignal):Promise<StorageSnapshot>{
 const headers:Record<string,string>={Accept:'application/json'};
 if(token.trim())headers.Authorization=`Bearer ${token.trim()}`;
 const response=await apiFetch('/storage',{headers,signal,cache:'no-store'});
 if(!response.ok)throw new Error(`Storage measurements unavailable (${response.status}).`);
 const data=await response.json();
 return validateStorage(data);
}
