import {decodeObject as cbor} from "./pil/control.ts";
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { StoreCodec } from "./store/wasm.ts";
import { fixture, bytes, frame, le, pilObject, extended } from "./pil-fixtures.mjs";
const module = await WebAssembly.compile(
  await readFile(new URL("../assets/trunk-store.wasm", import.meta.url)),
);
export class MemoryFile {
  data = new Uint8Array();
  getSize() {
    return this.data.length;
  }
  read(b, { at }) {
    const v = this.data.subarray(at, at + b.length);
    b.set(v);
    return v.length;
  }
  write(b, { at }) {
    if (at + b.length > this.data.length) this.truncate(at + b.length);
    this.data.set(b, at);
    return b.length;
  }
  truncate(n) {
    const b = new Uint8Array(n);
    b.set(this.data.subarray(0, n));
    this.data = b;
  }
  flush() {}
  close() {}
}
const f = fixture(),
  day = Number(
    BigInt("0x" + Buffer.from(f.uuid.subarray(0, 6)).toString("hex")) /
      86400000n,
  );
async function setup(files = [new MemoryFile(), new MemoryFile()]) {
  const c = await StoreCodec.create(module);
  c.files = files;
  const id = c.open(day, f.store, f.pil);
  return { c, id, files };
}
function put(c, id, ...frames) {
  c.upload(bytes(...frames));
  let at = 0;
  for (const b of frames) {
    c.put(id, at, at + b.length);
    at += b.length;
  }
  const refs = c.commit(id);
  return Array.from({ length: refs.length / 42 }, (_, i) =>
    refs.slice(i * 42, (i + 1) * 42),
  );
}
const row = (suffix) => {
  const b = f.row();
  b[19] = suffix;
  return b;
};
test("PIL → native files → PIL retains sparse dictionary codes and field bytes", async () => {
  const { c, id, files } = await setup();
  const [at] = put(c, id, f.row());
  assert.equal(at.length, 42);
  assert.deepEqual(c.get(id, at), f.row());
  assert.equal(Buffer.from(files[0].data.subarray(0, 4)).toString(), "TTRI");
  assert.equal(files[0].data.length, 58);
  assert.equal(cbor(files[0].data.subarray(22,33)),2);
  assert.equal(cbor(files[0].data.subarray(47,58)),5);
  const pointer = new DataView(at.buffer);
  assert.equal(pointer.getUint16(16), 2);
  assert.equal(pointer.getBigUint64(18), 16n);
  assert.equal(pointer.getBigUint64(34), 8n);
  assert.equal(typeof cbor(files[1].data.subarray(16)),"object");
  assert.equal(files.length, 2); // no index file in the browser kernel
  const reopened = await setup(files);
  assert.deepEqual(reopened.c.get(reopened.id, at), f.row());
});
test("the batch returns ordered pointer records; persistent deduplication belongs to the host", async () => {
  const { c, id } = await setup();
  const refs = put(c, id, row(3), row(1), row(2), row(1));
  assert.deepEqual(
    refs.map((r) => r[15]),
    [1, 2, 3],
  );
  for (const r of refs) assert.deepEqual(c.get(id, r), row(r[15]));
});
test("legacy U32 IndexedDB pointers remain readable beside new REF16 pointers", async () => {
  const { c, id, files } = await setup();
  const [current] = put(c, id, row(1));
  const legacy = new Uint8Array(44);
  legacy.set(current.subarray(0, 16));
  new DataView(legacy.buffer).setUint32(16, new DataView(current.buffer).getUint16(16));
  legacy.set(current.subarray(18), 20);
  const reopened = await setup(files);
  assert.deepEqual(reopened.c.get(reopened.id, legacy), row(1));
  assert.deepEqual(reopened.c.get(reopened.id, current), row(1));
});
test("host committed ends recover orphan payload and trie appends", async () => {
  const { c, id, files } = await setup();
  const [ref] = put(c, id, row(1));
  const ends = files.map((f) => f.getSize());
  put(c, id, row(2)); // simulate a crash before IDB publication
  files.forEach((f, i) => f.truncate(ends[i]));
  const next = await setup(files);
  assert.deepEqual(next.c.get(next.id, ref), row(1));
  assert.equal(put(next.c, next.id, row(2)).length, 1);
});
test("payload corruption is checked when its block is read", async () => {
  const { c, id, files } = await setup();
  const [ref] = put(c, id, row(1));
  files[1].data[files[1].data.length - 1] ^= 1;
  const next = await setup(files);
  assert.throws(() => next.c.get(next.id, ref), /checksum/);
});
test("refresh observes another writer's trie and payload appends", async () => {
  const first = await setup();
  put(first.c, first.id, row(1));
  const second = await setup(first.files);
  const [ref] = put(second.c, second.id, row(2));
  first.c.refresh(first.id);
  assert.deepEqual(first.c.get(first.id, ref), row(2));
});
test("fallback, null, absence, literal strings, empty lineage and nested PIL objects survive storage", async () => {
  const { c, id } = await setup();
  const b = frame(
    [0, 3, 2, 1, 0, 3, 3, 3],
    [
      [],
      new TextEncoder().encode("other"),
      f.uuid,
      [],
      [],
      pilObject({ 0: null, 8: [1, "λ"] }),
      extended(0),
      pilObject({ 3: 99, 55: pilObject("bytes") }),
    ],
  );
  const [at] = put(c, id, b);
  assert.deepEqual(c.get(id, at), b);
});
test("invalid mapping and invalid lineage are rejected", async () => {
  const { c, id } = await setup();
  const invalid = f.row();
  invalid[invalid.length - 1] = 0xff;
  c.upload(invalid);
  assert.throws(() => c.put(id, 0, invalid.length));
  const bad = fixture(43);
  assert.throws(() => c.open(day, f.store, bad.pil), /does not match/);
});
test("row references and day boundaries are checked", async () => {
  const { c, id } = await setup();
  put(c, id, f.row());
  assert.throws(() => c.get(id, new Uint8Array(43)));
  c.files = [new MemoryFile(), new MemoryFile()];
  const wrong = c.open(day + 1, f.store, f.pil);
  c.upload(f.row());
  assert.throws(() => c.put(wrong, 0, f.row().length), /wrong day/);
});
test("CRC32C agrees with native Castagnoli check vector", async () => {
  const c = await StoreCodec.create(module);
  assert.equal(c.checksum(new TextEncoder().encode("123456789")), 0xe3069283);
});
test("failed payload writes leave old pointers recoverable", async () => {
  const { c, id, files } = await setup();
  const [ref] = put(c, id, row(1)),
    ends = files.map((f) => f.getSize());
  const write = files[1].write;
  files[1].write = () => {
    throw Error("disk full");
  };
  assert.throws(() => put(c, id, row(2)), /disk full/);
  files[1].write = write;
  files.forEach((f, i) => f.truncate(ends[i]));
  const next = await setup(files);
  assert.deepEqual(next.c.get(next.id, ref), row(1));
});
test("external pointers cannot underflow a payload block", async () => {
  const { c, id } = await setup();
  const [ref] = put(c, id, row(1));
  new DataView(ref.buffer).setBigUint64(18, 0n);
  assert.throws(() => c.get(id, ref), /payload range/);
});
test("restoration reads payload once per run, not once per event", async () => {
  const { c, id, files } = await setup();
  const refs = put(c, id, ...Array.from({ length: 100 }, (_, i) => row(i + 1)));
  let reads = 0;
  for (const file of files) {
    const read = file.read.bind(file);
    file.read = (...args) => {
      reads++;
      return read(...args);
    };
  }
  for (const at of refs) c.get(id, at);
  assert.equal(reads, 2); // one block header + one payload block; no file index
});

test("REF16 reaches 65535 and spills new paths into payload without losing values", async () => {
  const { c, files } = await setup();
  const blocks = [files[0].data];
  const node = (parent, key, value) => {
    const payload = new Uint8Array(6 + pilObject(value).length);
    const v = new DataView(payload.buffer);
    v.setUint32(0, parent); v.setUint16(4, key);
    payload.set(pilObject(value), 6);
    const block = new Uint8Array(8 + payload.length);
    const header = new DataView(block.buffer);
    header.setUint32(0, payload.length); header.setUint32(4, c.checksum(payload));
    block.set(payload, 8); blocks.push(block);
  };
  for (let i = 0; i < 65533; i++) node(0, 4, i + 1000);
  node(0, 4, 2); node(65534, 3, 5);
  files[0].data = new Uint8Array(Buffer.concat(blocks));
  const atCapacity = await setup(files);
  const [max] = put(atCapacity.c, atCapacity.id, row(1));
  assert.equal(new DataView(max.buffer).getUint16(16), 65535);
  assert.deepEqual(atCapacity.c.get(atCapacity.id, max), row(1));
  const before = files[0].getSize();
  const other = row(2); other[3] = 1; // billing, a new service path
  const [spill] = put(atCapacity.c, atCapacity.id, other);
  assert.equal(new DataView(spill.buffer).getUint16(16), 0);
  assert.equal(files[0].getSize(), before);
  const restored = atCapacity.c.get(atCapacity.id, spill);
  const { read } = await import("./pil/layout.ts");
  for (let i = 0; i < 6; i++) assert.deepEqual(read(restored, f.definition, i), read(other, f.definition, i));
  const reopened = await setup(files);
  assert.deepEqual(reopened.c.get(reopened.id, spill), restored);
});
