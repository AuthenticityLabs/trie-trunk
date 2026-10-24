import {test} from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {streamRejections} from "./rejections.ts";
const fixture=readFileSync(new URL("./fixtures/rejections/inspection.jsonl",import.meta.url),"utf8");
const source="demo/payment/transactions.v1/write";
const headers={"Content-Type":"application/x-ndjson","Trunk-Completion":"body"};
const response=(text,step=7)=>{
 const bytes=new TextEncoder().encode(text);let at=0;
 return new Response(new ReadableStream({pull(c){if(at===bytes.length){c.close();return;}const end=Math.min(at+step,bytes.length);c.enqueue(bytes.subarray(at,end));at=end;}}),{headers});
};
test("Go audit fixture streams exact event text with completed count and source scoping",async()=>{
 const events=[];
 const count=await streamRejections(source,"secret",new AbortController().signal,b=>events.push(...b),async(url,options)=>{
  assert.equal(new URL(url,"https://example.test").searchParams.get("tap"),source);
  assert.equal(options.headers.Authorization,"Bearer secret");return response(fixture);
 });
 assert.equal(count,1);assert.equal(events[0].source,source);
 assert.match(events[0].event_json,/18446744073709551615/);
 assert.equal("pil_base64" in events[0],false);
});
test("audit inspection refuses truncated, inconsistent, unnegotiated and cross-source data",async()=>{
 const row=fixture.split("\n")[0];
 for(const text of [row+"\n",row,fixture+'{"complete":true,"count":1}\n',row+'\n{"complete":true,"count":2}\n',row+'\n{"complete":false,"error":"read failed"}\n',fixture.replace(source,"other/payment/transactions.v1/write")]){
  await assert.rejects(streamRejections(source,"",new AbortController().signal,()=>{},async()=>response(text)));
 }
 await assert.rejects(streamRejections(source,"",new AbortController().signal,()=>{},async()=>new Response(fixture)),/not supported/);
 await assert.rejects(streamRejections(source,"",new AbortController().signal,()=>{},async()=>new Response("unauthorized",{status:401})),/unauthorized/);
 const abort=new AbortController();abort.abort();let calls=0;
 await assert.rejects(streamRejections(source,"",abort.signal,()=>{},async()=>{calls++;return response(fixture);}));
 assert.equal(calls,0);
});

test("lost-event availability is a small authenticated presence check, not an audit download",async()=>{
 const {lostEventsAvailable}=await import('./rejections.ts');
 for(const exists of [false,true]){
  assert.equal(await lostEventsAvailable('secret',new AbortController().signal,async(url,options)=>{
   assert.equal(url,'/rejections?format=availability');assert.equal(options.headers.Authorization,'Bearer secret');
   return Response.json({available:exists});
  }),exists);
 }
 for(const response of [Response.json({}),Response.json({available:0}),new Response('unavailable',{status:503}),new Response('<html>')]){
  await assert.rejects(lostEventsAvailable('',new AbortController().signal,async()=>response));
 }
});
