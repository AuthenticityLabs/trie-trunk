export type NativeCacheIssue = {
  code:'native-cache-conflict'|'native-cache-race';
  source?:string;
  reason:string;
  conflicts?:{source:string;reason:string;detail?:Record<string,string|number|null>}[];
  detail?:Record<string,string|number|null>;
};
/** Preserve recovery information across the file worker, shared worker and UI. */
export class NativeCacheError extends Error {
  readonly issue:NativeCacheIssue;
  constructor(message:string,issue:NativeCacheIssue){super(message);this.name='NativeCacheError';this.issue=issue;}
}
export function nativeCacheIssue(error:unknown):NativeCacheIssue|undefined {
  return error instanceof NativeCacheError?error.issue:undefined;
}
export function restoreCacheError(message:string,issue?:NativeCacheIssue):Error {
  return issue?new NativeCacheError(message,issue):new Error(message);
}
export function cacheRefreshSources(issue:NativeCacheIssue):string[] {
  const sources=issue.conflicts?.map(c=>c.source)??(issue.source?[issue.source]:[]);
  return [...new Map(sources.map(source=>[source.split('/').slice(0,3).join('/'),source])).values()];
}
/** Keep every failed resource; a page can collect many branches concurrently. */
export function combineCacheErrors(errors:unknown[]):unknown {
  const ordinary=errors.find(error=>!nativeCacheIssue(error));if(ordinary!==undefined)return ordinary;
  const conflicts=errors.flatMap(error=>{
    const issue=nativeCacheIssue(error)!;
    return issue.code==='native-cache-conflict'?(issue.conflicts??(issue.source?[{source:issue.source,reason:issue.reason,detail:issue.detail}]:[])):[];
  });
  if(!conflicts.length)return errors[0];
  const unique=[...new Map(conflicts.map(c=>[c.source,c])).values()];
  return new NativeCacheError('Conflicting native generations',{code:'native-cache-conflict',source:unique[0].source,reason:'resource-conflicts',conflicts:unique});
}
export function cacheErrorMessage(error:unknown):string {
  const issue=nativeCacheIssue(error);
  if(issue?.code==='native-cache-conflict'){
    const resources=cacheRefreshSources(issue).map(s=>s.split('/').slice(0,3).join('/'));
    if(resources.length>1)return `Local caches for ${resources.join(', ')} no longer match the server. Refresh these local copies together to continue.`;
    return `${resources[0]?resources[0]+': ':''}The local cache no longer matches the server. Refresh its local copy to continue.`;
  }
  if(issue?.code==='native-cache-race')return 'The cache changed while loading. Retry the search.';
  return error instanceof Error?error.message:String(error);
}
