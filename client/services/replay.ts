import { validSource } from "./source.ts";
import type { Archive } from "./store/protocol.ts";
import { rawValue, decodeValue, type LogEvent } from './dictionary.ts';

export type ObjectQuery = { source: string; id: string };
export type HistoryQuery = ObjectQuery & { from: string; to: string };
export type ObservedState = ObjectQuery & { token: string };
export type ObjectHistoryTarget = ObjectQuery & { field:string };

export function objectHistoryTarget(event:LogEvent, collections:readonly string[]):ObjectHistoryTarget|undefined {
  const role=event.dictionary.objectIdentity;
  if(!role||!validSource(event.dictionary.tap)||event.dictionary.tap.split('/').length!==4)return;
  const source=event.dictionary.tap.split('/').slice(0,3).join('/')+'/write';
  if(!collections.includes(source))return;
  const raw=rawValue(event,role.field);
  if(raw===null||raw===undefined)return;
  let id:string;
  if(role.type==='BYTES'&&raw instanceof Uint8Array){
    let binary='';for(const byte of raw)binary+=String.fromCharCode(byte);
    id=btoa(binary).replaceAll('+','-').replaceAll('/','_').replace(/=+$/,'');
  }else id=String(decodeValue(raw,role.field));
  try{objectIdentityBytes(role.type,id)}catch{return}
  return {source,id,field:role.field.name};
}
const statePattern = /^[a-f0-9]{64}$/;

export function objectIdentityBytes(kind:string, id:string):Uint8Array {
  if(kind==="STRING") {
    const bytes=new TextEncoder().encode(id);
    if(new TextDecoder().decode(bytes)!==id)throw Error("Invalid UTF-8 object identity");
    return bytes;
  }
  if(kind==="UUID") {
    if(!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id))throw Error("Invalid UUID object identity");
    return Uint8Array.from(id.replaceAll("-","").match(/../g)!,v=>parseInt(v,16));
  }
  if(kind==="BYTES") {
    if(!/^[A-Za-z0-9_-]*$/.test(id))throw Error("Invalid base64url object identity");
    const raw=atob(id.replaceAll("-","+").replaceAll("_","/"));
    if(btoa(raw).replaceAll("+","-").replaceAll("/","_").replace(/=+$/,"")!==id)throw Error("Noncanonical base64url object identity");
    return Uint8Array.from(raw,c=>c.charCodeAt(0));
  }
  const width=({U8:1,U16:2,U32:4,U64:8} as Record<string,number>)[kind];
  if(!width||!/^\d+$/.test(id))throw Error("Invalid unsigned object identity");
  let n=BigInt(id);
  if(n>=1n<<BigInt(width*8))throw Error("Object identity out of range");
  const result=new Uint8Array(width);
  for(let i=0;i<width;i++){result[i]=Number(n&255n);n>>=8n;}
  return result;
}

// This compares only successfully replicated invalidations. It is not an online
// origin-freshness check; use validateObject after reconnect or before trusting
// an origin refresh. Missing/ambiguous local state fails instead of returning true.
export async function validateCachedObject(state:ObservedState, credential:string, archive:Archive):Promise<boolean> {
  observedState(state,state.token);
  const reply=await archive(credential,{kind:"native-object-state",query:{source:state.source,id:state.id}});
  if(!reply.nativeObjectState||!statePattern.test(reply.nativeObjectState.token))throw Error("No committed cached object state");
  return reply.nativeObjectState.token===state.token;
}

export function objectParameters(query: ObjectQuery): URLSearchParams {
  const parts = query.source.split("/");
  if (!validSource(query.source) || parts.length !== 4 || typeof query.id !== "string")
    throw Error("Object lookup requires an exact cache/service/resource/branch source");
  return new URLSearchParams({ cache: parts[0], collection: `${parts[1]}/${parts[2]}`, id: query.id });
}
export function historyURL(query: HistoryQuery): string {
  const parameters = objectParameters(query);
  const from = Date.parse(query.from), to = Date.parse(query.to);
  if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to)
    throw Error("Object history requires a valid time window");
  parameters.set("from", query.from); parameters.set("to", query.to);
  return `/objects/history?${parameters}`;
}
export function observedState(query: ObjectQuery, token: string): ObservedState {
  objectParameters(query);
  if (!statePattern.test(token)) throw Error("Invalid object state token");
  // Read/error/diagnostic selections share the same collection/object state.
  return { source: query.source.split("/").slice(0, 3).join("/") + "/write", id: query.id, token };
}
async function stateRequest(query: ObjectQuery, credential: string, signal: AbortSignal,
  fetcher: typeof fetch, observed?: string): Promise<{ token: string; current?: boolean }> {
  const parameters = objectParameters(query);
  if (observed !== undefined) {
    if (!statePattern.test(observed)) throw Error("Invalid object state token");
    parameters.set("token", observed);
  }
  signal.throwIfAborted();
  const response = await fetcher(`/objects/state?${parameters}`, {
    signal, cache: "no-store", headers: { Accept: "application/json",
      ...(credential.trim() ? { Authorization: `Bearer ${credential.trim()}` } : {}) },
  });
  if (!response.ok) throw Error((await response.text()).trim() || `Object state request failed (${response.status})`);
  if (response.headers.get("Content-Type")?.split(";", 1)[0].trim() !== "application/json")
    throw Error("Invalid object state response");
  const value = await response.json();
  if (!value || typeof value.token !== "string" || !statePattern.test(value.token) ||
    observed !== undefined && (typeof value.current !== "boolean" || value.current !== (observed === value.token)))
    throw Error("Invalid object state response");
  signal.throwIfAborted();
  return value;
}
export async function observeObject(query: ObjectQuery, credential: string, signal: AbortSignal,
  fetcher: typeof fetch = fetch): Promise<ObservedState> {
  return observedState(query, (await stateRequest(query, credential, signal, fetcher)).token);
}
// Revalidate after reload/reconnect. A network failure is unknown freshness,
// never an authoritative cache hit. This does not prove a replay base exists.
export async function validateObject(state: ObservedState, credential: string, signal: AbortSignal,
  fetcher: typeof fetch = fetch): Promise<boolean> {
  return (await stateRequest(state, credential, signal, fetcher, state.token)).current!;
}
export class ObjectChangedDuringRefresh extends Error {
  constructor() { super("Object invalidated during origin refresh; fetch the complete object again"); }
}
// Caller provides its authoritative origin fetch (including a deletion result).
// Never return the fetched value as validated when a newer error arrived during
// the fetch; never clear a shared server token or validate another client's copy.
export async function reconcileObject<T>(query: ObjectQuery, credential: string, signal: AbortSignal,
  origin: (signal: AbortSignal) => Promise<T>, fetcher: typeof fetch = fetch): Promise<{ value: T; state: ObservedState }> {
  const state = await observeObject(query, credential, signal, fetcher);
  const value = await origin(signal);
  signal.throwIfAborted();
  if (!await validateObject(state, credential, signal, fetcher)) throw new ObjectChangedDuringRefresh();
  return { value, state };
}
