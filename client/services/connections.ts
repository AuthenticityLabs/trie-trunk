export type ServerConnection={id:string;name:string;url:string;enabled:boolean};
export const localConnection=():ServerConnection=>({id:'local',name:'Local server',url:'',enabled:true});
export function connectionURL(raw:string):string {
 const text=raw.trim();if(!text)return '';
 const url=new URL(text);
 if(!['http:','https:'].includes(url.protocol)||url.username||url.password||url.search||url.hash)throw Error('Use an HTTP or HTTPS server address without credentials, query parameters, or a fragment.');
 url.search='';url.hash='';return url.href.replace(/\/+$/,'');
}
export function normalizeConnection(input:ServerConnection):ServerConnection {
 if(!input||typeof input.id!=='string'||!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$/.test(input.id)||typeof input.name!=='string'||!input.name.trim()||input.name.length>128||typeof input.url!=='string'||typeof input.enabled!=='boolean')throw Error('Each connection needs a name and valid server address.');
 // Credentials are deliberately excluded from the persisted registration.
 return {id:input.id,name:input.name.trim(),url:connectionURL(input.url),enabled:input.enabled};
}
const fetchScopes=new WeakMap<typeof fetch,Pick<ServerConnection,'id'|'url'>>();
export function connectionFetch(connection:Pick<ServerConnection,'url'> & {id?:string},fetcher:typeof fetch=fetch):typeof fetch {
 const base=connectionURL(connection.url);
 const scoped=((input:RequestInfo|URL,init?:RequestInit)=>{
  if(!base){
   const raw=typeof input==='string'?input:input instanceof URL?input.href:input.url;
   if(/^(?:[a-z][a-z0-9+.-]*:|\/\/)/i.test(raw)){
    const origin=globalThis.location?.origin;
    if(!origin||new URL(raw,origin).origin!==origin)throw Error('Request is outside the selected server.');
   }
   return fetcher(input,init);
  }
  const raw=typeof input==='string'?input:input instanceof URL?input.href:input.url;
  const url=/^https?:\/\//i.test(raw)?new URL(raw):new URL(raw.replace(/^\//,''),base+'/');
  const target=new URL(base+'/');
  if(url.origin!==target.origin||!url.pathname.startsWith(target.pathname))throw Error('Request is outside the selected server.');
  return fetcher(input instanceof Request?new Request(url,input):url.href,init);
 }) as typeof fetch;
 fetchScopes.set(scoped,{id:connection.id??base,url:base});return scoped;
}
let active=localConnection();
export function activeConnection():ServerConnection{return {...active};}
export function setActiveConnection(value:ServerConnection){active=normalizeConnection(value);}
export const apiFetch:typeof fetch=(input,init)=>connectionFetch(active,globalThis.fetch)(input,init);
export function connectionScope(connection:Pick<ServerConnection,'id'|'url'>,token:string){return JSON.stringify([connection.id,connectionURL(connection.url),token.trim()]);}
export function fetchCredentialScope(fetcher:typeof fetch,token:string){
 const connection=fetcher===apiFetch?active:fetchScopes.get(fetcher);
 return connection?connectionScope(connection,token):token;
}
