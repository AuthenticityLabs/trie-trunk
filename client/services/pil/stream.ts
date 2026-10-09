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
export type Batch = { bytes: ArrayBuffer; rows: Uint32Array };
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
  constructor(
    codec: Codec,
    definitions: Map<string, StoredDefinition>,
    onDefinition: (m: Metadata) => void,
    onBinding: (b: Binding) => void,
    onBatch: (b: Batch) => Promise<void>,
  ) {
    this.codec = codec;
    this.definitions = definitions;
    this.onDefinition = onDefinition;
    this.onBinding = onBinding;
    this.onBatch = onBatch;
    this.handles = [
      codec.define(controlCBOR(define)),
      codec.define(controlCBOR(bind)),
    ];
  }
  close() {
    this.handles.forEach((h) => this.codec.release(h));
  }
  async feed(chunk: Uint8Array) {
    chunk = new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
    if (this.pending.length + chunk.length > maximum)
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
    this.codec.upload(bytes);
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
        this.ended = false;
        pos += 5;
        continue;
      }
      if (kind === 255) {
        this.ended = true;
        this.segments++;
        pos++;
        continue;
      }
      if (kind === 1 || kind === 2) {
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
            !/^[a-z0-9][a-z0-9._-]{0,63}$/.test(tap)
          )
            throw Error("Invalid PIL binding");
          const d = this.definitions.get(hex(digest));
          if (!d || this.streams.has(stream))
            throw Error("Unknown schema or duplicate PIL binding");
          this.streams.set(stream, d);
          this.onBinding({ stream, digest: d.digest, tap });
        }
        pos += 1 + n;
        this.codec.upload(bytes);
        continue;
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
