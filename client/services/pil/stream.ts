import { compressedMarker, envelopeLength, expandEvent } from "./compression.ts";
import { nativePAC, pacPlacement } from "./pac-native.ts";
import { pacHeader, expandPAC } from "./pac.ts";
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
  | { kind: "pac"; binding: Binding; bytes: Uint8Array; bounds: Uint32Array }
  | { kind: "manifest"; binding: Binding; manifest: Record<string, unknown> }
  | { kind: "chunk"; binding: Binding; file: "map" | "trie" | "state" | "index" | "population"; start: number; digest: string; bytes: Uint8Array }
  | { kind: "package-end"; binding: Binding };
const nativeControls: PILDefinition[] = [
  { pil: 0, definition: "trunk-native-control", version: 1, fields: [{name:"stream",type:"U32"},{name:"manifest",type:"CBOR"}] },
  { pil: 0, definition: "trunk-native-control", version: 2, fields: [{name:"stream",type:"U32"},{name:"kind",type:"U8"},{name:"start",type:"U64"},{name:"digest",type:"BYTES"},{name:"data",type:"BYTES"}] },
  { pil: 0, definition: "trunk-native-control", version: 3, fields: [{name:"stream",type:"U32"}] },
];
const hex = (bytes: Uint8Array) =>
  Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
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
    this.codec.upload(bytes);
    let codecDirty=false;
    const ensureUpload=()=>{if(codecDirty){this.codec.upload(bytes);codecDirty=false;}};
    while (pos < bytes.length) {
      const kind = bytes[pos];
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
        ensureUpload();
        const n = this.codec.scan(
          this.handles[kind - 1],
          pos + 1,
          bytes.length - pos - 1,
        );
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
          compile(metadata, "validation");
          const prior = this.definitions.get(hash);
          if (!prior) {
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
        this.codec.upload(bytes);
        continue;
      }
      if (kind >= 4 && kind <= 6) {
        this.hasEvents = true;
        if (!this.onNative) throw Error("Native ranges were not negotiated");
        ensureUpload();
        const control = nativeControls[kind - 4];
        const n = this.codec.scan(this.handles[kind - 2], pos + 1, bytes.length - pos - 1);
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
          // Own small identity byte strings before crossing the storage-worker
          // boundary. A subarray otherwise clones its entire network buffer for
          // each hourly manifest, even though only 16–32 bytes are referenced.
          const owned = Object.fromEntries(Object.entries(manifest).map(([key,value]) =>
            [key, value instanceof Uint8Array ? value.slice() : value]));
          await this.onNative({kind:"manifest", binding, manifest:owned});
        } else {
          if (!this.nativePackages.has(stream)) throw Error("Native control outside package");
          if (kind === 6) {
            this.nativePackages.delete(stream);
            await this.onNative({kind:"package-end", binding});
          } else {
            const kinds = ["map", "trie", "state", "index", "population"] as const;
            const type = read(frame, control, 1), start = read(frame, control, 2);
            const digest = read(frame, control, 3), data = read(frame, control, 4);
            if (typeof type !== "number" || !kinds[type] || typeof start !== "number" || !Number.isSafeInteger(start) || start < 0 ||
              !(digest instanceof Uint8Array) || digest.length !== 32 || !(data instanceof Uint8Array)) throw Error("Invalid native chunk");
            const hash = hex(new Uint8Array(await crypto.subtle.digest("SHA-256", data.slice().buffer)));
            if (hash !== hex(digest)) throw Error("Native chunk checksum mismatch");
            await this.onNative({kind:"chunk", binding, file:kinds[type], start, digest:hash, bytes:data.slice()});
          }
        }
        pos += 1 + n;
        this.codec.upload(bytes);
        continue;
      }
      if(kind===8){
        ensureUpload();
        if(!this.onView||this.viewHandle===undefined)throw Error("View response was not negotiated");
        const n=this.codec.scan(this.viewHandle,pos+1,bytes.length-pos-1);if(!n)break;
        const raw=read(bytes.subarray(pos+1,pos+1+n),viewControl,0);
        if(!(raw instanceof Uint8Array))throw Error("Invalid view metadata");
        this.onView(JSON.parse(new TextDecoder("utf-8",{fatal:true}).decode(raw)));
        pos+=1+n;continue;
      }
      if(kind===7){
        if(!this.onView&&!this.onNative)throw Error("PAC arrays were not negotiated");
        const unpacked=await expandPAC(bytes,pos+1,0xffffffff,(b,n)=>this.codec.inflate(b,n));if(!unpacked)break;
        codecDirty=true;
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
        ensureUpload();
        const bounds=this.codec.pac(definition.handle,pos+1,pac.end-pos-1,pac.count,!!this.onNative);
        if(this.onNative){
          if(!this.nativePackages.has(binding.stream))throw Error("Native PAC outside package");
          pacPlacement(pac);
          await this.onNative({kind:"pac",binding,bytes:bytes.slice(pos+1,pac.end),bounds});
          codecDirty=true;this.hasEvents=true;pos=pac.end;continue;
        }
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
        const validate=(raw:Uint8Array)=>{this.codec.upload(raw);if(this.codec.scan(d.handle,0,raw.length)!==raw.length)throw Error("Invalid expanded PIL frame");};
        const full=await expandEvent(candidate.subarray(0,length),d.definition,validate,(b,n)=>this.codec.inflate(b,n));
        expanded.set(rows.length,full);rows.push(id,0,full.length);expandedBytes+=full.length;
        if(expandedBytes>=8*1024*1024)await flushExpanded();
        codecDirty=true;this.hasEvents=true;pos+=5+length;continue;
      }
      ensureUpload();
      const n = this.codec.scan(d.handle, pos + 5, bytes.length - pos - 5);
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
    if (this.pending.length || !this.completed)
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
