import { validSource, matchesSource } from "./source.ts";

export async function lostEventsAvailable(token:string,signal:AbortSignal,fetcher:typeof fetch=fetch):Promise<boolean> {
  signal.throwIfAborted();
  const response=await fetcher('/rejections?format=availability',{
    signal,cache:'no-store',headers:{Accept:'application/json',...(token.trim()?{Authorization:`Bearer ${token.trim()}`}:{})},
  });
  if(!response.ok)throw Error(`Could not check alerts (${response.status})`);
  if(response.headers.get('Content-Type')?.split(';',1)[0].trim()!=='application/json')throw Error('Alert availability is not supported by this server');
  const value=await response.json();
  if(typeof value?.available!=='boolean')throw Error('Invalid alert availability');
  return value.available;
}

export type RejectedEvent = {
  received_at:string; source:string; event_id:string; definition_version:string;
  finalized_before:string; reason:string; event_json:string;
};

// Audit inspection is an explicit single-event display path. event_json stays
// text so JSON parsing never rounds an original 64-bit scalar.
export async function streamRejections(source:string, token:string, signal:AbortSignal,
  receive:(records:RejectedEvent[])=>void, fetcher:typeof fetch=fetch):Promise<number> {
  signal.throwIfAborted();
  const response=await fetcher(`/rejections?${new URLSearchParams({format:"inspection",...(source?{tap:source}:{})})}`,{
    signal,cache:"no-store",headers:{Accept:"application/x-ndjson",...(token.trim()?{Authorization:`Bearer ${token.trim()}`}:{})},
  });
  if(!response.ok)throw Error((await response.text()).trim()||`Rejection inspection failed (${response.status})`);
  if(response.headers.get("Content-Type")?.split(";",1)[0].trim()!=="application/x-ndjson"||response.headers.get("Trunk-Completion")!=="body"||!response.body){
    await response.body?.cancel();throw Error("Rejection inspection is not supported by this server");
  }
  const reader=response.body.getReader(),decoder=new TextDecoder("utf-8",{fatal:true});
  let pending="",count=0,complete=false;
  const line=(text:string,batch:RejectedEvent[])=>{
    if(text.length>256*1024*1024)throw Error("Rejected event exceeds inspection limit");
    if(!text.trim())return;
    if(complete)throw Error("Data after rejection completion");
    const value=JSON.parse(text);
    if(value&&"complete" in value){
      if(value.complete!==true||value.count!==count)throw Error(typeof value.error==="string"?value.error:"Invalid rejection completion");
      complete=true;return;
    }
    if(!value||!validSource(value.source)||!matchesSource(source,value.source)||
      !/^[a-f0-9]{32}$/.test(value.event_id)||!/^[a-f0-9]{64}$/.test(value.definition_version)||
      !Number.isFinite(Date.parse(value.received_at))||!Number.isFinite(Date.parse(value.finalized_before))||
      typeof value.reason!=="string"||typeof value.event_json!=="string")throw Error("Invalid rejected event");
    count++;batch.push(value);
  };
  try{
    for(;;){
      signal.throwIfAborted();
      const part=await reader.read();
      pending+=decoder.decode(part.value,{stream:!part.done});
      const batch:RejectedEvent[]=[];
      let start=0,end:number;
      while((end=pending.indexOf("\n",start))>=0){line(pending.slice(start,end),batch);start=end+1;}
      pending=pending.slice(start);
      // Bound one damaged line, not the complete audit. Original PIL frames
      // are limited to 64 MiB; JSON inspection may expand binary representations.
      if(pending.length>256*1024*1024)throw Error("Rejected event exceeds inspection limit");
      if(batch.length)receive(batch);
      if(part.done)break;
    }
    if(pending.trim()||!complete)throw Error("Incomplete rejection inspection");
    signal.throwIfAborted();return count;
  }finally{await reader.cancel().catch(()=>{});reader.releaseLock();}
}
