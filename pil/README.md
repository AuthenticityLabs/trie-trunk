# PIL codec prototype

A standalone Rust library for the PIL positional frame layout. It implements the
supplied [draft](PIL-DRAFT.md) and [array addendum](ARRAY-ADDENDUM.md), with the
prototype conventions below. The wire format is not frozen. This crate is
a standalone library, now used by trie-trunk search for server encoding and CLI
decoding. See [the search transport binding](../rust/PIL-TRANSPORT.md).

## Run

From the trie-trunk directory, with Rust 1.88 or newer:

```sh
cargo test --manifest-path pil/Cargo.toml
cargo run --manifest-path pil/Cargo.toml --example roundtrip
cargo clippy --manifest-path pil/Cargo.toml --all-targets -- -D warnings
```

The example exchanges a Definition once, then encodes and reads two consecutive
frames. It includes a dictionary reference, a UUID, a microsecond timestamp, a
typed array, and a partial message with absent and null fields.

## API

```rust
use pil_codec::{Definition, Field, Scalar, Value, encode, decode_exact};

fn main() -> Result<(), pil_codec::Error> {
    let definition = Definition::new("event", 1, vec![
        Field::mapped("service", ["vault", "billing"]),
        Field::new("message", Scalar::String),
    ])?;
    let bytes = encode(&definition, &[
        Value::Mapped(0),
        Value::String("Request completed"),
    ])?;
    let frame = decode_exact(&definition, &bytes)?;
    assert_eq!(frame.values()[0], Value::Mapped(0));
    assert_eq!(frame.string(0), Some("vault"));
    Ok(())
}
```

- `Definition::to_cbor` / `from_cbor` serialize the identity, version, ordered
  fields, and dictionaries as one artifact. Definitions are immutable after
  construction. The prototype requires a nonempty identity and field list.
- `encode` requires one `Value` per Definition field, including `Absent` or `Null`.
  `Mapped(id)` explicitly writes a reference; `String(text)` explicitly writes
  UTF-8 through OFFSET, even if that text also exists in the dictionary.
- `FrameEncoder::encode` reuses its frame/payload buffers across records.
  `encode_with` accepts a closure supplying each field in Definition order,
  avoiding a temporary vector of values. Both produce the same bytes as `encode`.
  The returned slice is borrowed until the next encode; a failed encode does not
  prevent reuse for the next record.
- `CborRef::parse` validates a borrowed CBOR item; `Value::ValidatedCbor` copies
  that item into an OFFSET field without parsing it again. Ordinary CBOR is
  checked without constructing a value tree. Tagged values retain the original
  decoder's semantic validation. Dynamic `@fields` collision checks still apply.
- `CborMap` walks borrowed key/value pairs with a caller-selected nesting limit.
  Exhaust the iterator to validate the complete map and its final boundary.
- `decode` reads one frame and reports `consumed()`. `decode_exact` additionally
  rejects trailing bytes. `Frames` iterates a concatenated slice and stops after
  the first error. There is no extra frame-length prefix.
- `read_frame_bytes` reads one self-delimiting frame from a `Read` source with a
  caller-supplied byte limit. It returns owned bytes; call `decode_exact` to
  validate and interpret their contents. Clean EOF returns `None`, while a
  truncated frame returns an I/O error.
- `Error::Incomplete` indicates that the caller needs more input to finish the
  frame. Invalid contents within a complete field boundary are errors, including
  truncated arrays or malformed CBOR inside that boundary.
- `Frame::values()` retains mapped IDs. `Frame::string(index)` resolves either a
  mapped ID or an inline string on demand, without copying the string.
- `Array::Items` accepts a homogeneous slice for encoding. Decoded arrays use
  `Array::Encoded(ArrayView)`; `view.iter()` reads elements from borrowed bytes.
- `@fields` accepts a CBOR map with unique text keys that do not collide with
  Definition fields or the reserved `@` namespace. Its contents remain CBOR;
  materialization and application state updates belong to the caller.

The caller must bind input bytes to the exact Definition identity and version.
Frames do not carry that identity; the codec cannot detect a wrongly bound
Definition that happens to accept the same bytes.

## Prototype wire conventions

These choices fill gaps in the supplied draft; the draft snapshot is preserved.

| Item                   | Encoding                                                                               |
| ---------------------- | -------------------------------------------------------------------------------------- |
| Field mask             | First field in bits 7–6, next in 5–4, then 3–2 and 1–0; unused low bits zero           |
| Field states           | `00` ABSENT, `01` NULL, `10` DIRECT, `11` OFFSET                                       |
| Numeric primitives     | Little-endian; signed integers two's complement; IEEE-754 floats                       |
| Shared references      | `REF16`: opaque 0–65,535 reference, two little-endian bytes, DIRECT                      |
| UUID                   | Exactly 16 bytes in standard RFC 9562 byte order, not mixed-endian GUID order          |
| Timestamps             | `TIMESTAMP_MS`, `TIMESTAMP_US`, `TIMESTAMP_NS`: signed i64 Unix time in the named unit |
| Dictionary references  | U8/U16/U32/U64 according to the largest zero-based dictionary ID                       |
| Counts and End Offsets | Canonical extended unsigned integers: 2, 6, or 14 bytes                                |
| End Offsets            | Nondecreasing cumulative byte ends; equal ends represent empty values                  |

For [UUIDv7](https://www.rfc-editor.org/rfc/rfc9562.html#section-5.7), the first
six bytes contain the Unix millisecond timestamp. UUID values are copied as
16 bytes, without text conversion or generation. Other UUID versions are also
accepted; the UUID type does not enforce a particular version.

All timestamp types occupy eight bytes. Nanoseconds cover approximately ±292
years around 1970; microseconds approximately ±292,000 years. The Definition
selects the unit; no per-value tag or automatic unit conversion is introduced.
Bare `TIMESTAMP` is rejected rather than assigned an implicit unit.

Dictionary capacities of 1–256 use U8; 257–65,536 use U16; up to 2^32 use U32;
larger dictionaries use U64. References are ordinary fixed-width unsigned
integers: their maximum values are usable IDs, not extension markers. An empty
`values` list provides no DIRECT encoding; that STRING uses OFFSET.

`REF16` names a shared-value reference without requiring trie storage. Its target
namespace, definition, and generation must be bound by the application before
frames are read; the scalar does not negotiate or transmit the referenced data.
All 65,536 bit patterns are usable, including zero. ABSENT and NULL use the field
mask and consume no value bytes. `ARRAY<REF16>` uses contiguous two-byte elements.
The trunk index uses node zero for its root/population fallback; this is a trunk
binding convention, not a PIL reservation. The current collection transport still
expands trie fields; adding this primitive does not enable trie replication by itself.

## Arrays and nullability

The array **field** supports all three distinct states:

| Logical state | Field mask | Array payload            |
| ------------- | ---------- | ------------------------ |
| Not supplied  | ABSENT     | None                     |
| Explicit null | NULL       | None                     |
| Empty array   | OFFSET     | Encoded count zero       |
| Present array | OFFSET     | Count and element values |

Elements in a present native array must have the declared scalar type; they
cannot be ABSENT or NULL. No element validity bitmap is included in this version.
`ARRAY<ARRAY<T>>` is not implemented. `ARRAY<CBOR>` permits complex objects and
nested CBOR arrays, and null values within those objects; a top-level CBOR null
is rejected as a null array element.

Fixed-width arrays contain a count followed by contiguous values. STRING, BYTES,
and CBOR arrays contain a count, cumulative element End Offsets, and contiguous
value bytes. Element offsets are relative to the element-value section, not the
parent frame. The parent field boundary must match the complete array size.

## Implementation limits

String, byte, CBOR, and array payloads borrow the input buffer. The decoder does
allocate field-value and offset metadata, and validates every array element.
CBOR validation currently uses `ciborium` to build and discard a temporary value
tree, with a nesting limit of 64. This prototype is not an allocation-free codec
or a performance-tuned parser. It retains the original CBOR bytes after validation.

Definition and `@fields` maps enforce their key constraints. Generic CBOR fields
are treated as opaque data items; application-level key semantics are not imposed
on their contents. Supported CBOR values follow the `ciborium` value model.

Callers should bound transport buffers and accepted message sizes. HTTP bindings,
Definition negotiation/cache policy, application patch semantics,
and datastore integration are outside this codec. Trunk implements its transfer
binding in the search adapter.

Tests include independent byte vectors, extended integer boundaries, UUID order,
all timestamp units, dictionary width changes, nullable array fields, empty
values, borrowed payloads, malformed input, and concatenated frames.

## Browser reader

`pil/wasm` builds a small WASM reader around this same codec. `FrameIndex` validates
one encoded frame into reusable byte spans, without a decoded event vector or
owned CBOR object tree. `Error::Incomplete` permits fragmented stream retries.
See [wasm/README.md](wasm/README.md) for the ABI and browser integration.

## Codec-only benchmarks

The shared-reference comparison replaces supplied common fields with one REF16
into a cached trie. Its baseline has **no REF field**: common values are already
individually dictionary-coded. Both paths use the same compiled codec.

```sh
cargo bench --manifest-path pil/Cargo.toml --bench shared_refs
cargo bench --manifest-path pil/Cargo.toml --bench shared_refs -- --reverse
```

Fixtures use 8- and 16-field object definitions, complete objects, and changes
supplying exactly 1 or 2 fields. Each runs at 1,000, 10,000, 50,000, 100,000 and
1,000,000 events. Definition/dictionary/trie initialization, file I/O, and JSON
are excluded. Fully expanded typed decoding and deferred view decoding are
reported separately. The benchmark binding compiles shared slots into REF16;
it is a prototype, not a change to the live collection wire protocol.

See the [corrected shared-reference results](benchmarks/2026-10-02-shared-ref16/report.html),
with raw samples, byte counts, partial-message semantics, and limitations.

The older `codecs` benchmark compares scalar widths using a synthetic U32
reference. It does **not** measure replacing a shared group with REF16. Its
[archived report](benchmarks/2026-10-02/report.html) is retained for auditability;
its 64/512-field scenarios are not representative of the intended object sizes.
