import { compressedMarker, envelopeLength, expandEvent } from "./compression.ts";
import { nativePAC, pacPlacement } from "./pac-native.ts";
import { pacHeader, expandPAC, indexedPACBounds } from "./pac.ts";
import { validSource } from "../source.ts";
import { Codec } from "./wasm.ts";
import { cbor, controlCBOR } from "./cbor.ts";
import { read, type PILDefinition } from "./layout.ts";
import { compile, type Metadata, type Binding } from "./metadata.ts";
export const pilType = "application/pil";
export const maximum = 64 * 1024 * 1024;
export type StoredDefinition = Metadata & {
  handle: number;
  definition: PILDefinition;
};
const viewControl: PILDefinition = {pil:0,definition:"trunk-view-control",version:1,fields:[{name:"metadata",type:"BYTES"}]};
export type Batch = { bytes: ArrayBuffer; rows: Uint32Array; common?: (Uint8Array | undefined)[] };
const define: PILDefinition = {
  pil: 0,
  definition: "trunk-pil-control",
  version: 1,
  fields: [
    { name: "digest", type: "BYTES" },
    { name: "store", type: "BYTES" },
    { name: "pil", type: "BYTES" },
  ],
};
const bind: PILDefinition = {
  pil: 0,
  definition: "trunk-pil-control",
  version: 2,
  fields: [
    { name: "stream", type: "U32" },
    { name: "digest", type: "BYTES" },
    { name: "tap", type: "STRING" },
  ],
};
// Opt-in native storage controls. These carry server bytes and coordinates;
// they are not event objects or an alternate population encoding.
export type NativeRecord =
  | { kind: "pac"; binding: Binding; bytes: Uint8Array; bounds: Uint32Array; integrityVerified?:true }
  | { kind: "manifest"; binding: Binding; manifest: Record<string, unknown> }
  | { kind: "chunk"; binding: Binding; file: "map" | "trie" | "state" | "index" | "population"; start: number; digest: string; bytes: Uint8Array; integrityVerified?:true }
  | { kind: "package-end"; binding: Binding };
const nativeControls: PILDefinition[] = [
  { pil: 0, definition: "trunk-native-control", version: 1, fields: [{name:"stream",type:"U32"},{name:"manifest",type:"CBOR"}] },
  { pil: 0, definition: "trunk-native-control", version: 2, fields: [{name:"stream",type:"U32"},{name:"kind",type:"U8"},{name:"start",type:"U64"},{name:"digest",type:"BYTES"},{name:"data",type:"BYTES"}] },
  { pil: 0, definition: "trunk-native-control", version: 3, fields: [{name:"stream",type:"U32"}] },
];
const hex = (bytes: Uint8Array) =>
  Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
function ownControlBytes(value:unknown):unknown {
  if(value instanceof Uint8Array)return value.slice();
  if(Array.isArray(value))return value.map(ownControlBytes);
  if(value&&typeof value==='object')return Object.fromEntries(Object.entries(value).map(([key,v])=>[key,ownControlBytes(v)]));
  return value;
}
export class PILStream {
  private codec: Codec;
  private definitions: Map<string, StoredDefinition>;
  private onDefinition: (m: Metadata) => void;
  private onBinding: (b: Binding) => void;
  private onBatch: (batch: Batch) => Promise<void>;
  private handles: number[];
  private pending: Uint8Array = new Uint8Array(0);
  private segments = 0;
  private hasEvents = false;
  private started = false;
  private ended = false;
  private completed = false;
  private streams = new Map<number, StoredDefinition>();
  private bindings = new Map<number, Binding>();
  private nativePackages = new Set<number>();
  private nativeRange?: {stream:number;remaining:number};
  private nativeBlocks = new Map<number,Map<number,Record<string,any>>>();
  private nativeIndexes = new Map<number,Map<number,Uint8Array>>();
  private viewHandle?: number;
  private onView?: (value:unknown)=>void;
  private onNative?: (record: NativeRecord) => Promise<void>;
  constructor(
    codec: Codec,
    definitions: Map<string, StoredDefinition>,
    onDefinition: (m: Metadata) => void,
    onBinding: (b: Binding) => void,
    onBatch: (b: Batch) => Promise<void>,
    onNative?: (record: NativeRecord) => Promise<void>,
    onView?: (value:unknown)=>void,
  ) {
    this.codec = codec;
    this.onView=onView;
    this.definitions = definitions;
    this.onDefinition = onDefinition;
    this.onBinding = onBinding;
    this.onBatch = onBatch;
    this.onNative = onNative;
    this.handles = [
      codec.define(controlCBOR(define)),
      codec.define(controlCBOR(bind)),
      ...(onNative ? nativeControls.map(d => codec.define(controlCBOR(d))) : []),
    ];
    if(onView){this.viewHandle=codec.define(controlCBOR(viewControl));this.handles.push(this.viewHandle)}
  }
    close() {
    this.handles.forEach((h) => this.codec.release(h));
  }
  async feed(chunk: Uint8Array) {
    chunk = new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
    if (this.pending.length + chunk.length > 0xffffffff)
      throw Error("PIL frame exceeds 64 MiB");
    let bytes: Uint8Array;
    if (!this.pending.length) bytes = chunk;
    else {
      bytes = new Uint8Array(this.pending.length + chunk.length);
      bytes.set(this.pending);
      bytes.set(chunk, this.pending.length);
    }
    let pos = 0;
    if (!this.started) {
      if (bytes.length < 5) {
        this.pending = bytes;
        return;
      }
      if (![84, 80, 73, 76, 2].every((b, i) => bytes[i] === b))
        throw Error("Unsupported PIL stream version");
      this.started = true;
      pos = 5;
    }
    const rows: number[] = [];
    const common: (Uint8Array | undefined)[] = [];
    const expanded = new Map<number, Uint8Array>();
    let expandedBytes=0;
    const flushExpanded=async()=>{
      let size=0;for(let i=0;i<rows.length;i+=3)size+=expanded.get(i)?.length??rows[i+2]-rows[i+1];
      const payload=new Uint8Array(size);let at=0;
      for(let i=0;i<rows.length;i+=3){const frame=expanded.get(i)??bytes.subarray(rows[i+1],rows[i+2]);payload.set(frame,at);rows[i+1]=at;at+=frame.length;rows[i+2]=at;}
      await this.onBatch({bytes:payload.buffer,rows:Uint32Array.from(rows),common:common.slice()});rows.length=0;common.length=0;expanded.clear();expandedBytes=0;
    };
    // Scan a bounded window through pill-packer. Awaited callbacks may reuse
    // the codec's input memory, so invalidate the window at each such boundary.
    // Never upload the entire response again for every tiny control record.
    let uploadedStart=-1,uploadedEnd=-1;
    const invalidate=()=>{uploadedStart=uploadedEnd=-1;};
    const scanAt=(handle:number,start:number)=>{
      let width=4096;
      for(;;){
        if(uploadedStart<0||start<uploadedStart||start>=uploadedEnd){
          uploadedStart=start;uploadedEnd=Math.min(bytes.length,start+width);
          this.codec.upload(bytes.subarray(uploadedStart,uploadedEnd));
        }
        const n=this.codec.scanStructural(handle,start-uploadedStart,uploadedEnd-start);
        if(n||uploadedEnd===bytes.length)return n;
        width=Math.max(width*2,(uploadedEnd-start)*2);invalidate();
      }
    };
    while (pos < bytes.length) {
      const kind = this.nativeRange ? 7 : bytes[pos];
      if (this.completed) throw Error("Data after PIL completion");
      if (this.ended) {
        if (kind === 254) {
          this.completed = true;
          pos++;
          continue;
        }
        if (bytes.length - pos < 5) break;
        if (
          this.hasEvents ||
          this.segments >= 2 ||
          ![84, 80, 73, 76, 2].every((b, i) => bytes[pos + i] === b)
        )
          throw Error("Missing PIL success marker");
        this.streams.clear();
        this.bindings.clear();
        this.ended = false;
        pos += 5;
        continue;
      }
      if (kind === 255) {
        if (this.nativePackages.size) throw Error("Unfinished native package");
        this.ended = true;
        this.segments++;
        pos++;
        continue;
      }
      if (kind === 1 || kind === 2) {
        const n = scanAt(this.handles[kind - 1],pos+1);
        if (!n) break;
        const frame = bytes.subarray(pos + 1, pos + 1 + n);
        if (kind === 1) {
          const digest = read(frame, define, 0),
            store = read(frame, define, 1),
            pil = read(frame, define, 2);
          if (
            !(digest instanceof Uint8Array) ||
            digest.length !== 32 ||
            !(store instanceof Uint8Array) ||
            !(pil instanceof Uint8Array)
          )
            throw Error("Invalid PIL definition control");
          const hash = hex(
            new Uint8Array(
              await crypto.subtle.digest("SHA-256", store.slice().buffer),
            ),
          );
          if (hash !== hex(digest)) throw Error("PIL schema digest mismatch");
          const metadata = {
            digest: hash,
            store: store.slice(),
            pil: pil.slice(),
          };
          const prior = this.definitions.get(hash);
          if (!prior) {
            compile(metadata, "validation");
            if (this.definitions.size >= 1024)
              throw Error("Too many PIL definitions in this session");
            this.definitions.set(hash, {
              ...metadata,
              handle: this.codec.define(pil),
              definition: cbor(pil) as PILDefinition,
            });
          } else if (hex(prior.pil) !== hex(pil))
            throw Error("Immutable PIL definition changed");
          this.onDefinition(metadata);
        } else {
          const stream = read(frame, bind, 0),
            digest = read(frame, bind, 1),
            tap = read(frame, bind, 2);
          if (
            typeof stream !== "number" ||
            !(digest instanceof Uint8Array) ||
            digest.length !== 32 ||
            typeof tap !== "string" ||
            !validSource(tap)
          )
            throw Error("Invalid PIL binding");
          const d = this.definitions.get(hex(digest));
          if (!d || this.streams.has(stream))
            throw Error("Unknown schema or duplicate PIL binding");
          this.streams.set(stream, d);
          const binding = { stream, digest: d.digest, tap };
          this.bindings.set(stream, binding);
          this.onBinding(binding);
        }
        pos += 1 + n;
        invalidate();
        continue;
      }
      if (kind >= 4 && kind <= 6) {
        this.hasEvents = true;
        if (!this.onNative) throw Error("Native ranges were not negotiated");
        const control = nativeControls[kind - 4];
        const n = scanAt(this.handles[kind - 2],pos+1);
        if (!n) break;
        const frame = bytes.subarray(pos + 1, pos + 1 + n);
        const stream = read(frame, control, 0);
        if (typeof stream !== "number" || !this.bindings.has(stream)) throw Error("Unbound native control");
        const binding = this.bindings.get(stream)!;
        if (kind === 4) {
          if (this.nativePackages.has(stream)) throw Error("Overlapping native packages");
          const manifest = read(frame, control, 1) as Record<string, unknown>;
          if (!manifest || typeof manifest !== "object" || Array.isArray(manifest)) throw Error("Invalid native manifest");
          const digest = manifest.Definition;
          if (!(digest instanceof Uint8Array) || hex(digest) !== binding.digest || manifest.RowWidth !== 16)
            throw Error("Native manifest binding mismatch");
          this.nativePackages.add(stream);
          // Own every nested byte string, including optional block metadata.
          // Cloning even a 16-byte subarray otherwise copies its entire network
          // buffer across workers (or again during storage validation).
          const owned=ownControlBytes(manifest) as Record<string,unknown>;
          this.nativeIndexes.set(stream,new Map());
          this.nativeBlocks.set(stream,new Map((Array.isArray(owned.Blocks)?owned.Blocks:[]).map(b=>[b["0"],b])));
          await this.onNative({kind:"manifest", binding, manifest:owned});
        } else {
          if (!this.nativePackages.has(stream)) throw Error("Native control outside package");
          if (kind === 6) {
            this.nativePackages.delete(stream);
            this.nativeIndexes.delete(stream);this.nativeBlocks.delete(stream);
            await this.onNative({kind:"package-end", binding});
          } else {
            const kinds = ["map", "trie", "state", "index", "population"] as const;
            const type = read(frame, control, 1), start = read(frame, control, 2);
            const digest = read(frame, control, 3), data = read(frame, control, 4);
            if (typeof type !== "number" || !kinds[type] || typeof start !== "number" || !Number.isSafeInteger(start) || start < 0 ||
              !(digest instanceof Uint8Array) || digest.length !== 32 || !(data instanceof Uint8Array)) throw Error("Invalid native chunk");
            const hash = hex(new Uint8Array(await crypto.subtle.digest("SHA-256", data.slice().buffer)));
            if (hash !== hex(digest)) throw Error("Native chunk checksum mismatch");
            const owned=data.slice();
            if(type===3)this.nativeIndexes.get(stream)!.set(start,owned);
            await this.onNative({kind:"chunk", binding, file:kinds[type], start, digest:hash, bytes:owned,integrityVerified:true});
          }
        }
        pos += 1 + n;
        invalidate();
        continue;
      }
      if(kind===8){
        if(!this.onView||this.viewHandle===undefined)throw Error("View response was not negotiated");
        const n=scanAt(this.viewHandle,pos+1);if(!n)break;
        const raw=read(bytes.subarray(pos+1,pos+1+n),viewControl,0);
        if(!(raw instanceof Uint8Array))throw Error("Invalid view metadata");
        this.onView(JSON.parse(new TextDecoder("utf-8",{fatal:true}).decode(raw)));
        pos+=1+n;invalidate();continue;
      }
      if(kind===9){
        if(!this.onNative)throw Error("Native PAC range was not negotiated");
        if(bytes.length-pos<13)break;
        const header=new DataView(bytes.buffer,bytes.byteOffset+pos,13),stream=header.getUint32(1,true),length=header.getBigUint64(5,true);
        if(!this.nativePackages.has(stream)||length===0n||length>BigInt(Number.MAX_SAFE_INTEGER))throw Error("Invalid native PAC range");
        this.nativeRange={stream,remaining:Number(length)};pos+=13;continue;
      }
      if(kind===7){
        if(!this.onView&&!this.onNative)throw Error("PAC arrays were not negotiated");
        const pacStart=pos+(this.nativeRange?0:1);
        const rangeEnd=this.nativeRange?Math.min(bytes.length,pacStart+this.nativeRange.remaining):bytes.length;
        const unpacked=await expandPAC(bytes.subarray(0,rangeEnd),pacStart,0xffffffff,(b,n)=>this.codec.inflate(b,n));if(!unpacked){if(this.nativeRange&&bytes.length-pacStart>=this.nativeRange.remaining)throw Error("Truncated PAC in native range");break;}
        invalidate();
        if(this.onNative){
          // Native storage consumes one complete hourly PAC. Keep its decoded
          // buffer separate; do not rebuild the surrounding network response.
          const raw=unpacked.bytes,pac=pacHeader(raw,0,0xffffffff,new Set([1,nativePAC]));
          if(!pac)throw Error("Incomplete expanded native PAC");
          const binding=[...this.bindings.values()].find(b=>b.digest===pac.digest&&b.tap===pac.source);
          if(!binding)throw Error("Unbound PAC definition/source");
          if(!this.nativePackages.has(binding.stream))throw Error("Native PAC outside package");
          const placement=pacPlacement(pac),first=Number(placement.firstRow);
          if(this.nativeRange&&binding.stream!==this.nativeRange.stream)throw Error("PAC range source mismatch");
          const block=this.nativeBlocks.get(binding.stream)?.get(first);
          const expected=block?.["7"],wire=bytes.subarray(pacStart,unpacked.end);
          if(!block||block["1"]-first!==pac.count||block["3"]-block["2"]!==wire.length||!Number.isInteger(expected)||expected<0||expected>0xffffffff)throw Error("Missing native PAC checksum binding");
          this.codec.verifyCRC32C(wire,expected);
          const indexes=this.nativeIndexes.get(binding.stream)!,index=indexes.get(first);
          if(!index||placement.memberBytes!==BigInt(pac.end-pac.start))throw Error("Missing native PAC member directory");
          const bounds=indexedPACBounds(pac,index,16,12);
          await this.onNative({kind:"pac",binding,bytes:raw.slice(),bounds,integrityVerified:true});
          indexes.delete(first);
          if(this.nativeRange){this.nativeRange.remaining-=unpacked.end-pacStart;if(!this.nativeRange.remaining)this.nativeRange=undefined;}
          invalidate();this.hasEvents=true;pos=unpacked.end;continue;
        }
        if(unpacked.bytes.buffer!==bytes.buffer){
          // Flush prior row views before replacing their backing network buffer.
          if(rows.length)await flushExpanded();
          const next=new Uint8Array(pos+1+unpacked.bytes.length+bytes.length-unpacked.end);
          next.set(bytes.subarray(0,pos+1));next.set(unpacked.bytes,pos+1);next.set(bytes.subarray(unpacked.end),pos+1+unpacked.bytes.length);bytes=next;
        }
        const pac=pacHeader(bytes,pos+1,0xffffffff,new Set(this.onNative?[1,nativePAC]:[1]));if(!pac)break;
        const binding=[...this.bindings.values()].find(b=>b.digest===pac.digest&&b.tap===pac.source);
        if(!binding)throw Error("Unbound PAC definition/source");
        const definition=this.streams.get(binding.stream)!;
        this.codec.upload(bytes.subarray(pos+1,pac.end));
        const bounds=this.codec.pacStructural(definition.handle,0,pac.end-pos-1,pac.count);
        invalidate();
        const prefix=pac.common.length?pac.common.slice():undefined;
        for(let i=0;i<pac.count;i++){
          common[rows.length/3]=prefix;
          rows.push(binding.stream,pos+1+bounds[i*2],pos+1+bounds[i*2+1]);
        }
        this.hasEvents=true;pos=pac.end;continue;
      }
      if (kind !== 3) throw Error("Unknown PIL record");
      if (bytes.length - pos < 5) break;
      const id = new DataView(
        bytes.buffer,
        bytes.byteOffset + pos + 1,
        4,
      ).getUint32(0, true);
      const d = this.streams.get(id);
      if (!d) throw Error("Unbound PIL event");
      const candidate=bytes.subarray(pos+5);
      if(compressedMarker(candidate,d.definition)){
        const length=envelopeLength(candidate,d.definition);if(length===undefined||candidate.length<length)break;
        const validate=(raw:Uint8Array)=>{this.codec.upload(raw);if(this.codec.scanStructural(d.handle,0,raw.length)!==raw.length)throw Error("Invalid expanded PIL frame");};
        const full=await expandEvent(candidate.subarray(0,length),d.definition,validate,(b,n)=>this.codec.inflate(b,n));
        expanded.set(rows.length,full);rows.push(id,0,full.length);expandedBytes+=full.length;
        if(expandedBytes>=8*1024*1024)await flushExpanded();
        invalidate();this.hasEvents=true;pos+=5+length;continue;
      }
      const n = scanAt(d.handle,pos+5);
      if (!n) break;
      this.hasEvents = true;
      rows.push(id, pos + 5, pos + 5 + n);
      pos += 5 + n;
    }
    // Keep only the unfinished suffix. Transfer the completed network batch;
    // the worker does not retain a second working set or decoded event objects.
    this.pending = bytes.slice(pos);
    if (rows.length) {
      if(expanded.size){await flushExpanded();return;}
      const payload =
        pos === bytes.length &&
        bytes.byteOffset === 0 &&
        bytes.byteLength === bytes.buffer.byteLength &&
        bytes.buffer instanceof ArrayBuffer
          ? bytes.buffer
          : bytes.slice(0, pos).buffer;
      await this.onBatch({
        bytes: payload as ArrayBuffer,
        rows: Uint32Array.from(rows),
        common,
      });
    }
  }
  finish() {
    if (this.pending.length || this.nativeRange || !this.completed)
      throw Error(
        "PIL stream ended before successful completion. Results may be partial.",
      );
    return true;
  }
}
export async function consume(
  response: Response,
  stream: PILStream,
  signal: AbortSignal,
) {
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  let complete = false;
  try {
    await checkResponse(response);
    if (
      response.headers.get("Content-Type")?.toLowerCase() !== pilType ||
      response.headers.get("PIL-Version") !== "2" ||
      response.headers.get("Trunk-Completion") !== "body" ||
      !response.body
    )
      throw Error(
        "This server does not provide the browser PIL transport. Rebuild and restart trie-trunk.",
      );
    reader = response.body.getReader();
    while (true) {
      signal.throwIfAborted();
      const { done, value } = await reader.read();
      if (done) {
        const result = stream.finish();
        complete = true;
        return result;
      }
      await stream.feed(value);
    }
  } finally {
    if (!complete)
      await (reader ? reader.cancel() : response.body?.cancel())?.catch(
        () => {},
      );
    reader?.releaseLock();
    stream.close();
  }
}
export async function checkResponse(response: Response) {
  if (response.ok) return;
  if (response.status === 401)
    throw Error(
      "Authentication required. Add your bearer token in Connection settings.",
    );
  if (response.status === 503)
    throw Error(
      "The server is busy. Stop another collection or try again shortly.",
    );
  throw Error(
    (await response.text()).trim() || `Request failed (${response.status}).`,
  );
}
