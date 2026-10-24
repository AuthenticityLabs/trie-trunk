import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Codec } from "./pil/wasm.ts";
import { PILStream } from "./pil/stream.ts";
import { compile } from "./pil/metadata.ts";
import { PILEvent } from "./pil/event.ts";
import { read } from "./pil/layout.ts";
import { cbor } from "./pil/cbor.ts";
import { cb } from "./pil-fixtures.mjs";
import { valueAt } from "./dictionary.ts";
import { StoreCodec } from "./store/wasm.ts";
const load = n => new Uint8Array(readFileSync(new URL(`./fixtures/native-pil/${n}`,import.meta.url)));
const pil = load("definition.cbor"), original = load("frame.pil");
const module = await WebAssembly.compile(readFileSync(new URL("../assets/pil.wasm",import.meta.url)));
test("REF16 translation metadata preserves ordinary PIL decoding in Rust/WASM and the view", async () => {
 const definition = new Uint8Array(readFileSync(new URL("./fixtures/references/definition.cbor", import.meta.url)));
 const frame = new Uint8Array(readFileSync(new URL("./fixtures/references/frame.pil", import.meta.url)));
 const codec = new Codec(await WebAssembly.instantiate(module));
 const handle = codec.define(definition); codec.upload(frame);
 assert.equal(codec.scan(handle, 0, frame.length), frame.length);
 const layout = cbor(definition);
 assert.equal(layout.resource.references.length, 2);
 assert.deepEqual(read(frame, layout, 0), [4, 65535, 0]);
 assert.equal(read(frame, layout, 3), 65535);
 codec.release(handle);
});
class MemoryFile {
 data=new Uint8Array();getSize(){return this.data.length}flush(){}close(){}
 truncate(n){const b=new Uint8Array(n);b.set(this.data.subarray(0,n));this.data=b}
 read(b,{at}){const v=this.data.subarray(at,at+b.length);b.set(v);return v.length}
 write(b,{at}){if(at+b.length>this.data.length)this.truncate(at+b.length);this.data.set(b,at);return b.length}
}
test("Go HTTP native stream → WASM validator → byte-backed Snoot event",async()=>{
 const codec=new Codec(await WebAssembly.instantiate(module)),defs=new Map(),bindings=new Map(),rows=[];
 const stream=new PILStream(codec,defs,()=>{},b=>bindings.set(b.stream,compile(defs.get(b.digest),b.tap)),async b=>{
  for(let i=0;i<b.rows.length;i+=3)rows.push(new PILEvent(new Uint8Array(b.bytes,b.rows[i+1],b.rows[i+2]-b.rows[i+1]),bindings.get(b.rows[i])));
 });
 const bytes=load("collect.pil");for(let i=0;i<bytes.length;i+=17)await stream.feed(bytes.subarray(i,i+17));stream.finish();
 assert.equal(rows.length,1);assert.deepEqual(rows[0].encoded(),original);
 assert.equal(valueAt(rows[0],"id"),42);assert.equal(valueAt(rows[0],"revision_id"),1);
 assert.equal(valueAt(rows[0],"duration"),11);assert.equal(valueAt(rows[0],"state"),"authorized");assert.equal(valueAt(rows[0],"amount"),999);
 assert.equal(rows[0].dictionary.revisionIdentity?.name,"revision_id");
 assert.equal(rows[0].orderedKeys().length,7);stream.close();
});
test("native browser persistence keeps PIL bytes, including integer widths",async()=>{
 const mod=await WebAssembly.compile(readFileSync(new URL("../assets/trunk-store.wasm",import.meta.url)));
 const files=[new MemoryFile(),new MemoryFile()];const day=Math.floor(Date.parse("2026-09-01T12:00:00Z")/86400000);
 const store=await StoreCodec.create(mod);store.files=files;const id=store.open(day,pil,pil);
 store.upload(original);store.put(id,0,original.length);const row=store.commit(id);assert.equal(row.length,42);
 assert.deepEqual(store.get(id,row),original);
 assert.deepEqual(files[1].data.subarray(16),original); // CRC block wraps PIL; no CBOR projection.
 const next=await StoreCodec.create(mod);next.files=files;const reopened=next.open(day,pil,pil);assert.deepEqual(next.get(reopened,row),original);
});
test("signed values and selectors crossing a byte retain their declared logical type",async()=>{
 const definition={pil:1,definition:"mixed",version:1,adaptive_integers:"16",fields:[{name:"a",type:"I16"},...Array.from({length:4},(_,i)=>({name:`b${i}`,type:"I64"})),{name:"text",type:"STRING"}]};
 // Five DIRECT values (9 selector bits), then an OFFSET string. All signed -1.
 const row=Uint8Array.of(0xaa,0xb0,0,0,255,255,255,255,255,1,0,120);
 const c=new Codec(await WebAssembly.instantiate(module));const h=c.define(cb(definition));c.upload(row);assert.equal(c.scan(h,0,row.length),row.length);
 for(let i=0;i<5;i++)assert.equal(read(row,definition,i),-1);assert.equal(read(row,definition,5),"x");
});

test("native typed arrays decode only when their field is read",()=>{
 const definition={pil:0,definition:"arrays",version:1,fields:[{name:"tags",type:"ARRAY<STRING>"}]};
 // OFFSET mask, parent end=9, count=2, element ends=1,3, values='abc'.
 assert.deepEqual(read(Uint8Array.of(0xc0,9,0,2,0,1,0,3,0,97,98,99),definition,0),["a","bc"]);
});
