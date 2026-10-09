// CBOR is opened only for definitions and individual requested field values.
// Events themselves remain PIL byte ranges. Integer precision is never rounded.
const decoder = new TextDecoder("utf-8", { fatal: true });
export function cbor(bytes: Uint8Array): unknown {
  let at = 0;
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const take = (n: number) => {
    const start = at;
    at += n;
    if (at > bytes.length) throw Error("Incomplete CBOR");
    return bytes.subarray(start, at);
  };
  const number = (n: bigint) =>
    n >= BigInt(Number.MIN_SAFE_INTEGER) && n <= BigInt(Number.MAX_SAFE_INTEGER)
      ? Number(n)
      : n;
  const argument = (a: number): number | bigint => {
    if (a < 24) return a;
    const start = at;
    if (a === 24) return take(1)[0];
    if (a === 25) {
      take(2);
      return view.getUint16(start);
    }
    if (a === 26) {
      take(4);
      return view.getUint32(start);
    }
    if (a === 27) {
      take(8);
      return number(view.getBigUint64(start));
    }
    throw Error("Invalid CBOR argument");
  };
  const length = (n: number | bigint) => {
    if (typeof n !== "number" || n > bytes.length)
      throw Error("CBOR length exceeds field");
    return n;
  };
  function item(depth: number): unknown {
    if (depth > 32) throw Error("CBOR nesting exceeds 32");
    const head = take(1)[0],
      major = head >> 5,
      info = head & 31;
    if (major === 7) {
      if (info === 20 || info === 21) return info === 21;
      if (info === 22) return null;
      if (info === 23) return undefined;
      const start = at;
      if (info === 25) {
        const h = Number(argument(25));
        const e = (h >> 10) & 31;
        return (
          (h >> 15 ? -1 : 1) *
          (e === 31
            ? h & 1023
              ? NaN
              : Infinity
            : (e ? 1 + (h & 1023) / 1024 : (h & 1023) / 1024) *
              2 ** (e ? e - 15 : -14))
        );
      }
      if (info === 26) {
        take(4);
        return view.getFloat32(start);
      }
      if (info === 27) {
        take(8);
        return view.getFloat64(start);
      }
      throw Error("Unsupported CBOR simple value");
    }
    if (info === 31) {
      if (major < 2 || major > 5) throw Error("Invalid indefinite CBOR");
      const items: unknown[] = [];
      while (bytes[at] !== 255) items.push(item(depth + 1));
      take(1);
      if (major === 2) {
        if (!items.every((x) => x instanceof Uint8Array))
          throw Error("Invalid CBOR byte chunks");
        const parts = items as Uint8Array[];
        const result = new Uint8Array(parts.reduce((n, b) => n + b.length, 0));
        let p = 0;
        for (const b of parts) {
          result.set(b, p);
          p += b.length;
        }
        return result;
      }
      if (major === 3) {
        if (!items.every((x) => typeof x === "string"))
          throw Error("Invalid CBOR text chunks");
        return items.join("");
      }
      if (major === 4) return items;
      if (items.length % 2) throw Error("Invalid CBOR map");
      return map(items);
    }
    const n = argument(info);
    if (major === 0) return n;
    if (major === 1) return number(-1n - BigInt(n));
    if (major === 2) return take(length(n));
    if (major === 3) return decoder.decode(take(length(n)));
    if (major === 4)
      return Array.from({ length: length(n) }, () => item(depth + 1));
    if (major === 5)
      return map(Array.from({ length: length(n) * 2 }, () => item(depth + 1)));
    if (major === 6) return item(depth + 1);
    throw Error("Invalid CBOR");
  }
  function map(items: unknown[]) {
    const result: Record<string, unknown> = Object.create(null);
    for (let i = 0; i < items.length; i += 2) {
      const key = items[i];
      if (typeof key !== "string" && typeof key !== "number")
        throw Error("Unsupported CBOR map key");
      if (Object.hasOwn(result, String(key)))
        throw Error("Duplicate CBOR map key");
      result[String(key)] = items[i + 1];
    }
    return result;
  }
  const result = item(0);
  if (at !== bytes.length) throw Error("Trailing CBOR data");
  return result;
}

// Only used to install the two small, fixed transport control definitions.
export function controlCBOR(value: unknown): Uint8Array {
  const out: number[] = [];
  const head = (major: number, n: number) => {
    if (n < 24) out.push(major * 32 + n);
    else if (n < 256) out.push(major * 32 + 24, n);
    else out.push(major * 32 + 25, n >> 8, n & 255);
  };
  function write(v: unknown) {
    if (typeof v === "number") head(0, v);
    else if (typeof v === "string") {
      const b = new TextEncoder().encode(v);
      head(3, b.length);
      out.push(...b);
    } else if (Array.isArray(v)) {
      head(4, v.length);
      v.forEach(write);
    } else if (v && typeof v === "object") {
      const pairs = Object.entries(v);
      head(5, pairs.length);
      for (const [k, x] of pairs) {
        write(k);
        write(x);
      }
    } else throw Error("Invalid control definition");
  }
  write(value);
  return Uint8Array.from(out);
}
