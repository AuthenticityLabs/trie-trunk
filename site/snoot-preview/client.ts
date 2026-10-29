import {dashboardData,events} from './data';
import type {FastRequest} from '../../client/services/fast-dashboard';
export async function loadFastDashboard(query:FastRequest,_token:string,signal:AbortSignal){signal.throwIfAborted();return dashboardData(query)}
export async function primeSummaries(_source:string,_refresh:boolean,_token:string,signal:AbortSignal,progress:(value:unknown)=>void){signal.throwIfAborted();progress({completed:30,total:30,packets:30})}
export async function streamObjectHistory(_query:unknown,_token:string,signal:AbortSignal,receive:(rows:typeof events)=>void){signal.throwIfAborted();receive(events.filter(e=>e.dictionary.tap.includes('/payment/')).slice(0,3));return undefined}
export async function loadDictionaries(){return []}
export async function streamEvents(){return undefined}
