import {workerService} from './worker-runtime.ts';
import {connectionFetch,connectionScope,localConnection,normalizeConnection,type ServerConnection} from '../connections.ts';
import type {Archive} from '../store/protocol.ts';
import type {Request,Reply} from './protocol.ts';
function buffers(value:unknown,out=new Set<ArrayBuffer>(),seen=new Set<object>()):ArrayBuffer[]{
 if(value instanceof ArrayBuffer)out.add(value);
 else if(ArrayBuffer.isView(value)){if(value.buffer instanceof ArrayBuffer)out.add(value.buffer);}
 else if(value&&typeof value==='object'&&!seen.has(value)){seen.add(value);for(const child of Object.values(value))buffers(child,out,seen);}
 return [...out];
}
/** Each registered endpoint owns its codec, query caches, and file namespace. */
export function connectionWorker(module:Promise<WebAssembly.Module>,fetcher:typeof fetch=fetch){
 const services=new Map<string,ReturnType<typeof workerService>>();
 return (port:MessagePort,archive:Archive)=>{
  const channels=new Map<string,MessagePort>(),jobs=new Map<number,MessagePort>();
  port.onmessage=({data}:{data:Request & {connection?:ServerConnection}})=>{
   if(data.kind==='close'){for(const channel of channels.values()){channel.postMessage({kind:'close'});channel.close()}channels.clear();archive.close?.();port.close();return;}
   if(data.kind==='cancel'||data.kind==='ack'){jobs.get(data.id)?.postMessage(data);if(data.kind==='cancel')jobs.delete(data.id);return;}
   let connection:ServerConnection;
   try{connection=normalizeConnection(data.connection??localConnection())}catch(error){port.postMessage({id:data.id,kind:'error',message:String(error)});return;}
   const key=JSON.stringify([connection.id,connection.url]);let channel=channels.get(key);
   if(!channel){
    let service=services.get(key);if(!service){service=workerService(module,connectionFetch(connection,fetcher));services.set(key,service)}
    const pair=new MessageChannel();
    const storage:Archive=(token,command)=>archive(connectionScope(connection,token),command);
    service(pair.port1,storage);channel=pair.port2;
    channel.onmessage=({data:reply}:{data:Reply})=>{if(reply.kind==='done'||reply.kind==='error')jobs.delete(reply.id);port.postMessage(reply,buffers(reply));};
    channel.start();channels.set(key,channel);
   }
   jobs.set(data.id,channel);channel.postMessage(data);
  };
  port.start();
 };
}
