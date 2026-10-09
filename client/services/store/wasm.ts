export interface FileIO {
  getSize(): number;
  read(bytes: Uint8Array, options: { at: number }): number;
  write(bytes: Uint8Array, options: { at: number }): number;
  truncate(size: number): void;
  flush(): void;
  close(): void;
}
type API = WebAssembly.Exports & {
  memory: WebAssembly.Memory;
  input(n: number): number;
  open(day: number, schema: number): number;
  refresh(handle: number): number;
  put(handle: number, start: number, length: number): number;
  commit(handle: number): number;
  get(handle: number): number;
  lineage(handle: number, leaf: number): number;
  close(handle: number): void;
  checksum(): number;
  output(): number;
  error_ptr(): number;
  error_len(): number;
};
export class StoreCodec {
  private api!: API;
  files: FileIO[] = [];
  private ioError?: unknown;
  static async create(module: WebAssembly.Module) {
    const codec = new StoreCodec();
    const io = (action: () => void) => {
      try {
        action();
        return 0;
      } catch (error) {
        codec.ioError = error;
        return -1;
      }
    };
    const instance = await WebAssembly.instantiate(module, {
      files: {
        size: (id: number) => {
          try {
            return codec.files[id].getSize();
          } catch (error) {
            codec.ioError = error;
            return -1;
          }
        },
        read: (id: number, at: number, ptr: number, size: number) =>
          io(() => {
            const b = new Uint8Array(codec.api.memory.buffer, ptr, size);
            let n = 0;
            while (n < size) {
              const count = codec.files[id].read(b.subarray(n), { at: at + n });
              if (count <= 0) throw Error("Unexpected end of local store");
              n += count;
            }
          }),
        write: (id: number, at: number, ptr: number, size: number) =>
          io(() => {
            const b = new Uint8Array(codec.api.memory.buffer, ptr, size);
            let n = 0;
            while (n < size) {
              const count = codec.files[id].write(b.subarray(n), {
                at: at + n,
              });
              if (count <= 0) throw Error("Could not write local store");
              n += count;
            }
          }),
        truncate: (id: number, size: number) =>
          io(() => {
            if (codec.files[id].getSize() !== size)
              codec.files[id].truncate(size);
          }),
        flush: (id: number) => io(() => codec.files[id].flush()),
      },
    });
    codec.api = instance.exports as API;
    return codec;
  }
  private check(n: number) {
    if (n >>> 0 === 0xffffffff) {
      const reason = this.ioError;
      this.ioError = undefined;
      throw (
        reason ??
        Error(
          new TextDecoder().decode(
            new Uint8Array(
              this.api.memory.buffer,
              this.api.error_ptr(),
              this.api.error_len(),
            ),
          ),
        )
      );
    }
    return n >>> 0;
  }
  upload(b: Uint8Array) {
    const p = this.api.input(b.length);
    if (!p) throw Error("Local store batch too large");
    new Uint8Array(this.api.memory.buffer, p, b.length).set(b);
  }
  open(day: number, schema: Uint8Array, pil: Uint8Array) {
    const b = new Uint8Array(schema.length + pil.length);
    b.set(schema);
    b.set(pil, schema.length);
    this.upload(b);
    return this.check(this.api.open(day, schema.length));
  }
  refresh(id: number) {
    this.check(this.api.refresh(id));
  }
  put(id: number, from: number, to: number) {
    this.check(this.api.put(id, from, to - from));
  }
  private output(n: number) {
    return new Uint8Array(
      this.api.memory.buffer,
      this.api.output(),
      this.check(n),
    ).slice();
  }
  commit(id: number) {
    return this.output(this.api.commit(id));
  }
  get(id: number, row: Uint8Array) {
    this.upload(row);
    return this.output(this.api.get(id));
  }
  lineage(id: number, leaf: number) {
    return this.output(this.api.lineage(id, leaf));
  }
  close(id: number) {
    this.api.close(id);
  }
  checksum(b: Uint8Array) {
    this.upload(b);
    return this.api.checksum() >>> 0;
  }
}
