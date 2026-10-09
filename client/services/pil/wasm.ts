export type Wasm = WebAssembly.Exports & {
  memory: WebAssembly.Memory;
  input(n: number): number;
  definition(): number;
  scan(handle: number, offset: number, length: number): number;
  release(handle: number): void;
  error_ptr(): number;
  error_len(): number;
};
export class Codec {
  private api: Wasm;
  constructor(instance: WebAssembly.Instance) {
    this.api = instance.exports as Wasm;
  }
  upload(bytes: Uint8Array) {
    const ptr = this.api.input(bytes.length);
    if (!ptr) throw this.error();
    new Uint8Array(this.api.memory.buffer, ptr, bytes.length).set(bytes);
  }
  private error() {
    return Error(
      new TextDecoder().decode(
        new Uint8Array(
          this.api.memory.buffer,
          this.api.error_ptr(),
          this.api.error_len(),
        ),
      ),
    );
  }
  define(bytes: Uint8Array) {
    this.upload(bytes);
    const id = this.api.definition() >>> 0;
    if (id === 0xffffffff) throw this.error();
    return id;
  }
  scan(id: number, offset: number, length: number) {
    const n = this.api.scan(id, offset, length) >>> 0;
    if (n === 0xffffffff) throw this.error();
    return n;
  }
  release(id: number) {
    this.api.release(id);
  }
}
