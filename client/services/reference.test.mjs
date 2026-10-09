import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Codec } from "./pil/wasm.ts";
import { read } from "./pil/layout.ts";
import { row } from "./store/index.ts";
import { cb } from "./pil-fixtures.mjs";

test("REF16 uses two bytes in WASM and the view, preserving absence and null", async () => {
  const module = await WebAssembly.compile(readFileSync(new URL("../assets/pil.wasm", import.meta.url)));
  const codec = new Codec(await WebAssembly.instantiate(module));
  const definition = { pil: 0, definition: "reference-test", version: 1, fields: [{ name: "shared", type: "REF16" }] };
  const handle = codec.define(cb(definition));
  for (const [data, expected] of [[[0], undefined], [[64], null], [[128, 0, 0], 0], [[128, 255, 255], 65535]]) {
    const bytes = new Uint8Array(data);
    codec.upload(bytes);
    assert.equal(codec.scan(handle, 0, bytes.length), bytes.length);
    assert.equal(read(bytes, definition, 0), expected);
  }
  codec.upload(new Uint8Array([192, 2, 0, 0, 0]));
  assert.throws(() => codec.scan(handle, 0, 5), /OFFSET/);
});

test("IndexedDB reconstructs both pointer widths without padding new references", () => {
  for (const size of [26, 28]) {
    const pointer = new Uint8Array(size).fill(7);
    const packed = row({ key: ["demo", new ArrayBuffer(16)], value: { catalog: "c", pointer } });
    assert.equal(packed.length, 16 + size);
    assert.deepEqual(packed.subarray(16), pointer);
  }
  assert.throws(() => row({ key: ["demo", new ArrayBuffer(16)], value: { catalog: "c", pointer: new Uint8Array(27) } }), /pointer/);
});
