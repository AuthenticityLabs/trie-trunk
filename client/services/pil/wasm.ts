export type Wasm = WebAssembly.Exports & {
  memory: WebAssembly.Memory;
  input(n: number): number;
  definition(): number;
  scan(handle: number, offset: number, length: number): number;
  release(handle: number): void;
  scan_pac(handle:number,offset:number,length:number,native:number):number;
  pac_members():number;
  inflate(length:number):number;
  inflated_ptr():number;
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
  pac(id:number,offset:number,length:number,count:number,native=false):Uint32Array {
    const n=this.api.scan_pac(id,offset,length,native?1:0)>>>0;
    if(n===0xffffffff)throw this.error();if(n!==length)throw Error('Truncated PAC');
    return new Uint32Array(this.api.memory.buffer,this.api.pac_members(),count*2).slice();
  }
  inflate(bytes:Uint8Array,length:number):Uint8Array {
    this.upload(bytes);const n=this.api.inflate(length)>>>0;if(n===0xffffffff)throw this.error();
    return new Uint8Array(this.api.memory.buffer,this.api.inflated_ptr(),n).slice();
  }
  release(id: number) {
    this.api.release(id);
  }
}
