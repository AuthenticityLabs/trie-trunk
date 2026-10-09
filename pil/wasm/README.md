# PIL browser reader

Build with Rust's `wasm32-unknown-unknown` target:

```sh
rustup target add wasm32-unknown-unknown
cargo build --release --locked --target wasm32-unknown-unknown --manifest-path pil/wasm/Cargo.toml
```

From the repository root, `npm --prefix client run wasm` copies the resulting
module to `client/assets/pil.wasm`. Client dev, build and test scripts run that
step automatically. Vite gives the WASM asset and worker content-hashed URLs.
No wasm-bindgen, JavaScript event serialization, or shared-memory headers are
required. The current browser ABI exposes definition loading and frame reading;
encoding remains available in the native Rust crate.

Exports are synchronous and belong to one instance:

- `input(length)` reserves reusable scratch space; copy the incoming bytes to
  the returned address. Zero means an error (maximum input is 64 MiB).
- `definition()` compiles the input CBOR Definition and returns a handle.
- `scan(handle, start, length)` validates the frame in the specified input range.
  The result is its byte length, zero for incomplete input, or `0xffffffff` for
  invalid data. It validates mask states, dictionary references, scalars, arrays,
  UTF-8, cumulative offsets and CBOR boundaries.
- `spans()` points at two `u32`s per field, relative to the scanned frame.
  `0xffffffff` is ABSENT and `0xfffffffe` is NULL. These spans are scratch space,
  valid until the next scan. Snoot retains only whole-frame boundaries per batch.
- `release(handle)` drops a compiled Definition. `error_ptr()` / `error_len()`
  describe the latest failure as UTF-8.

Reacquire views after calls that can grow WASM memory. No raw pointers or borrowed
views leave the worker. Network batches are copied into reusable WASM memory for
validation, then their original ArrayBuffers are transferred to the tab. This is
bounded staging, not a claim of end-to-end zero-copy transport.

The table's small TypeScript view adapter reads validated masks, positional
scalars and individual field slices directly. It opens CBOR only for a requested
field (or a definition), preserving integer dictionary references. It does not
create a decoded values array or expanded event object on arrival. Full event
expansion is reserved for inspection and explicit JSONL export.

Definitions are compiled once into owned metadata. Semantic validation of rare
tagged CBOR values, `@fields`, or CBOR arrays may still use the codec’s owned
validation path; ordinary search fields use the borrowed validator.
