import {rejected} from './data';
export async function lostEventsAvailable(){return true}
export async function streamRejections(_source:string,_token:string,signal:AbortSignal,receive:(records:typeof rejected)=>void){signal.throwIfAborted();receive(rejected);return rejected.length}
