export type Resource = 'registries'|'collections'|'seeds'|'summaries'|'timelines'|'indexes';
const component=/^[a-z0-9][a-z0-9._-]*$/;
/** Scope is a catalog identity; only the cache becomes a URL path component. */
export function resourceURL(resource:Resource,source:string,parameters:URLSearchParams=new URLSearchParams()):string {
 const parts=source.split('/'),cache=parts.shift()!;
 if(!component.test(cache)||parts.length!==0&&parts.length!==3||parts.some(p=>p!=='*'&&!component.test(p)))throw Error('Invalid cache resource scope');
 const query=new URLSearchParams(parameters);
 if(parts.length)query.set('branch',parts.join('/'));
 return `/${cache}/${resource}${query.size?'?'+query:''}`;
}
export function cacheScopes(scope:string,sources:readonly string[]):string[]{
 const parts=scope.split('/');
 if(parts[0]&&parts[0]!=='*')return [scope];
 const caches=[...new Set(sources.map(s=>s.split('/')[0]))];
 if(!caches.length)throw Error('Load the trunk catalog before selecting a cache');
 return caches.map(cache=>[cache,...(parts.length===4?parts.slice(1):['*','*','*'])].join('/'));
}
export function timeParameters(from:string|number,to?:string|number):URLSearchParams {
 const millis=(v:string|number)=>typeof v==='number'?v:Date.parse(v);
 const a=millis(from),z=to===undefined||to==='now'?undefined:millis(to);
 if(!Number.isSafeInteger(a)||a<0||z!==undefined&&(!Number.isSafeInteger(z)||z<=a))throw Error('Invalid resource time range');
 const q=new URLSearchParams({beginTime:String(a)});if(z!==undefined)q.set('endTime',String(z));return q;
}
