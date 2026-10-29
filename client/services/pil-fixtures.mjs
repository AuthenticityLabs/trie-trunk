import { encodeObject } from "pill-packer/object";
import { createHash } from "node:crypto";
export const bytes = (...parts) =>
  Uint8Array.from(parts.flatMap((p) => [...p]));
export function pilObject(value) {
 const native=v=>{
  if(Array.isArray(v))return v.map(native);
  if(v instanceof Uint8Array||v===null||typeof v!=='object')return v;
  const entries=Object.entries(v).map(([k,x])=>[/^\d+$/.test(k)?Number(k):k,native(x)]);
  return entries.some(([k])=>typeof k==='number')?new Map(entries):Object.fromEntries(entries);
 };return encodeObject(native(value));
}
export const le = (n, width) => {
  const b = new Uint8Array(width);
  let v = BigInt(n);
  for (let i = 0; i < width; i++) {
    b[i] = Number(v & 255n);
    v >>= 8n;
  }
  return b;
};
export const extended = (n) =>
  n < 65535 ? le(n, 2) : bytes([255, 255], le(n, 4));
export function frame(states, values) {
  const mask = new Uint8Array(Math.ceil(states.length / 4)),
    direct = [],
    variable = [],
    offsets = [];
  let end = 0;
  states.forEach((s, i) => {
    mask[i >> 2] |= s << (6 - 2 * (i % 4));
    if (s === 2) direct.push(values[i]);
    if (s === 3) {
      end += values[i].length;
      offsets.push(extended(end));
      variable.push(values[i]);
    }
  });
  return bytes(mask, ...direct, ...offsets, ...variable);
}
export const magic = Uint8Array.of(84, 80, 73, 76, 2);
export function fixture(version = 42, message = "unauthorized λ") {
  const store = pilObject({
    0: version,
    1: {
      3: { 0: "level", 2: { 5: "ERROR" } },
      4: { 0: "service", 1: "service" },
      8: { 0: "id", 6: "uuidv7" },
      12: { 0: "duration", 5: "us" },
      15: { 0: "message" },
      16: { 0: "error", 3: { 0: { 0: "code", 2: { 1: "unauthorized" } } } },
    },
    3: { service: { 2: "vault", 9: "billing" } },
    4: "UTC",
    5: [4, 3],
  });
  const digest = createHash("sha256").update(store).digest("hex");
  const definition = {
    pil: 0,
    definition: `trunk:${digest}`,
    version,
    fields: [
      { name: "level", type: "STRING", values: ["ERROR"] },
      { name: "service", type: "STRING", values: ["vault", "billing"] },
      { name: "id", type: "UUID" },
      { name: "duration", type: "U64" },
      { name: "message", type: "OBJECT" },
      { name: "error", type: "OBJECT" },
      { name: "trunk_lineage", type: "ARRAY<U16>" },
      { name: "trunk_fallback", type: "OBJECT" },
    ],
  };
  const pil = encodeObject(definition),
    hash = Uint8Array.from(Buffer.from(digest, "hex"));
  const define = bytes([1], frame([3, 3, 3], [hash, store, pil]));
  const bind = (tap = "test", stream = 0) =>
    bytes(
      [2],
      frame([2, 3, 3], [le(stream, 4), hash, new TextEncoder().encode(tap)]),
    );
  const uuid = Uint8Array.from(
    Buffer.from("019f0234660070008000000000000001", "hex"),
  );
  const row = (text = message) =>
    frame(
      [2, 2, 2, 2, 3, 3, 3, 0],
      [
        le(0, 1),
        le(0, 1),
        uuid,
        le(2237, 8),
        pilObject(text),
        pilObject({ 0: 1 }),
        bytes(extended(2), le(4, 2), le(3, 2)),
        [],
      ],
    );
  const event = bytes([3], le(0, 4), row());
  return {
    store,
    pil,
    digest,
    definition,
    define,
    bind,
    event,
    row,
    uuid,
    schemas: bytes(magic, define, bind(), [255, 254]),
    cached: bytes(magic, bind(), event, [255, 254]),
    cold: bytes(magic, define, bind(), [255], magic, bind(), event, [255, 254]),
  };
}
export function response(body, version = "a".repeat(64), stride = 65536) {
  let at = 0;
  return new Response(
    new ReadableStream({
      pull(controller) {
        if (at === body.length) {
          controller.close();
          return;
        }
        controller.enqueue(body.slice(at, at + stride));
        at = Math.min(body.length, at + stride);
      },
    }),
    {
      headers: {
        "Content-Type": "application/pil",
        "PIL-Version": "2",
        "Definition-Version": version,
        "Trunk-Completion": "body",
      },
    },
  );
}
