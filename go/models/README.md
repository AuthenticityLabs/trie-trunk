# models — shared models

Currently implements structured errors using the shared `codec` vocabulary. This package does
not import the logger or server.

```go
import (
    "errors"
    "trie-trunk/go/models"
    "trie-trunk/go/codec"
)

failure := models.New(models.INVALID_INPUT).
    WithSource(codec.Text("customers")).
    WithCause(originalError)

errors.Is(failure, originalError) // true
payload, err := failure.Encode(codec.CBOR, nil)
```

`New` uses predefined definitions. Fluent `With...` methods return copies;
published errors are immutable. `FromDefinition` supports explicit application
definitions; `WithCodeText`, `WithTypeText`, and literal messages are escape paths.
Changing a code with `WithCode` changes only the code; use `New` or
`FromDefinition` when you also want that definition's type, status, and message.

The built-in codes reproduce the configured Java vocabulary:
`UNEXPECTED`, `REQUEST_TIMEOUT`, `UNSUPPORTED_MEDIA_TYPE`,
`MEDIA_TYPE_NOT_ACCEPTABLE`, `MISSING_PATH_VARIABLE`,
`MISSING_REQUEST_PARAMETER`, `INVALID_INPUT`, `BAD_REQUEST`,
`METHOD_NOT_ALLOWED`, `RESOURCE_NOT_FOUND`; `CANCELED` is added for Go contexts.
Types are `SYSTEM`, `SERVICE`, and `VALIDATION`.

`Normalize` finds wrapped structured errors with `errors.As`, recognizes wrapped
context cancellation/deadline errors with `errors.Is`, and maps unknown errors to
`UNEXPECTED` / `SYSTEM` / 500. Deadline errors retain the Java mapping to 503;
cancellation maps to 408. A transport adapter can retain an already-committed
actual status independently of this intended error status.

`WriteResponse` writes the readable JSON `{"error": ...}` envelope with the
chosen HTTP status. The body excludes status, matching the Java model. It never
includes the underlying cause. Messages supplied by the application are **public
messages**; internal diagnostics belong in causes. The caller owns response
commit tracking. Error response headers containing arbitrary messages are not
reproduced.

## Dictionary and encoding contract

- The nested error schema is version 1: `0=code`, `1=type`, `2=source`,
  `3=status`, `4=message`. Logging negotiates the enclosing schema/catalog once
  per connection; it no longer embeds metadata in each record.
  Standalone compact errors require this schema and catalog out of band.
- Core error codes are 1–11; types are 1–3. Default messages use the corresponding
  code number in the **error_message** domain. Standard text is not repeated.
- Application `error_code`, `error_type`, `error_message`, and logging `message`
  references start at 128; 1–127 are reserved. Source uses the `service` domain.
- Application catalogs are constructed once with `codec.NewDictionary`.
  Input maps are copied, catalogs are immutable, and names must identify an
  immutable version such as `customers/v1`. Never change an assigned number's
  meaning or reuse a retired number.
- `codec.Ref(1)` is a numeric reference; `codec.Text("1")` is literal text. The
  wire type disambiguates them. Unknown references remain numeric on expansion.
- Readable JSON expands known names. Compact JSON uses decimal string keys and
  numeric references. CBOR uses integer keys and references: numbers 0–23 need
  one byte, 24–255 two bytes, and 256–65,535 three bytes.
- Numeric zero is unspecified for symbols. An explicit empty literal is present.
- `codec.Decode` plus `codec.Expand` supports offline inspection. Unknown
  top-level fields and enum numbers are retained; provide the exact catalog
  bound to the originating connection. Decoding is not on the producer path.

`codec.Define`/`Definition.Prepare` build the initialization payload and a revision
containing the schema version plus a SHA-256 digest of the complete deterministic
CBOR definition. The digest detects dictionary changes even at the same numeric
version. The negotiated definition includes nested field schemas, units, built-in
enums, and application domains. `ParseDefinition` validates a received definition
against that revision. None of this work runs per log entry.

`codec.Value` provides typed extension constructors: `String`, `Int`, `Uint`,
`Bool`, `Float`, `List`, and `Object`. Containers are copied on construction;
arbitrary Go objects and cycles are unsupported. Non-finite floats become null.
Logs sanitize extension values separately from public error responses.

## Intentional omissions and costs

`codec.UUIDv7` supplies the compact 16-byte entry identifier. Generation follows
[RFC 9562](https://www.rfc-editor.org/rfc/rfc9562.html#section-5.7): a 48-bit Unix
millisecond timestamp and 74 random bits with the UUID version/variant bits.
JSON formats it as canonical UUID text; CBOR encodes the byte array directly.
The codec does not expose derived start/end-time APIs; those belong to the
log-view client. Tests verify the RFC's example vector and binary representation.

There is no exception-class discovery, regex/substring matching, reflection over
arbitrary errors, or automatic stack capture. Map external errors explicitly
with `errors.Is` / `errors.As` before returning a structured error. The wrappers
and error-only encoding projections allocate; no per-request registry is built.
Compact encoding uses fixed structs. Readable expansion uses maps and is more
expensive.

## Validation

```sh
cd go
go test -race ./...
go vet ./...
```

Go 1.24 or later. The CBOR implementation is the pinned `fxamacker/cbor/v2`
dependency; checksums are committed in `go.sum`.

## Stream framing

`codec.AppendFrame` and `codec.ReadFrame` implement a two-byte unsigned big-endian
payload length followed by CBOR bytes. Payloads must be 1–65,535 bytes; the header
is excluded from the count. Schema uploads and remote log records use this
framing. ReadFrame requires an explicit size limit and handles fragmented reads
with `io.ReadFull`. Invalid lengths are rejected before allocation; truncated
frames return `io.ErrUnexpectedEOF`. These helpers do not parse CBOR.

## Shared logging contract

`logger/client` owns log field keys, typed enums, the complete log schema, and
dictionary definition construction. Server code imports that package for the
same contract. Its nested error schema uses this independent error package.
`codec` owns immutable application dictionaries and wire encoding.
