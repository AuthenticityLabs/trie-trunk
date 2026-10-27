import { Codec as PortableCodec } from "pill-packer";

// The storage extension is a Trie-Trunk policy, never a portable codec default.
const TRUNK_NATIVE_EXTENSION = 0x5454524b;
export class Codec extends PortableCodec {
  constructor(instance: WebAssembly.Instance) {
    super(instance);
  }
  pac(id: number, offset: number, length: number, count: number, native: boolean | number = false): Uint32Array {
    return super.pac(id, offset, length, count, typeof native === "number" ? native : native ? TRUNK_NATIVE_EXTENSION : 0);
  }
}
