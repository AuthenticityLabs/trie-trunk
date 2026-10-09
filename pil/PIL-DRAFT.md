# PIL — Positional Interchange Layout

**Status:** Draft
**Version:** 0.x

## 1. Abstract

Positional Interchange Layout (PIL) is a compact binary interchange format for structured state exchanged under a shared definition.

PIL removes information already known by participants:

* field identity is represented by position;
* common field values are represented by mapped references;
* fixed-width primitives are encoded directly;
* variable values are represented by cumulative end offsets into a contiguous value section.

A PIL Definition is exchanged independently and reused across frames. PIL is therefore intended for environments where definitions can be negotiated, cached, and reused across repeated exchanges.

PIL defines the **Definition and binary frame layout**. Transport, connection lifecycle, persistence, caching, authorization, and codec implementation are outside scope.

---

## 2. PIL Definition

A **PIL Definition** is the immutable description required to interpret frames for one resource type.

A Definition contains:

```text
PIL version
Definition identity
Definition version
Ordered fields
Field types
Optional mapped values
```

Example, expressed as JSON for readability:

```json
{
  "pil": 0,
  "definition": "user",
  "version": 42,
  "fields": [
    { "name": "id", "type": "UUID" },
    {
      "name": "status",
      "type": "STRING",
      "values": ["active", "pending", "disabled"]
    },
    { "name": "firstName", "type": "STRING" },
    { "name": "lastName", "type": "STRING" },
    {
      "name": "country",
      "type": "STRING",
      "values": ["US", "CA", "GB"]
    },
    { "name": "createdAt", "type": "TIMESTAMP" },
    { "name": "preferences", "type": "CBOR" },
    { "name": "tags", "type": "ARRAY<STRING>" },
    { "name": "@fields", "type": "CBOR" }
  ]
}
```

Field array position is field identity.

Mapped-value array position is its numeric reference:

```text
status:
0 → active
1 → pending
2 → disabled
```

The Definition and its mapped values form one versioned artifact. They are not negotiated separately.

A published Definition MUST be immutable. Any change affecting frame interpretation MUST create a new Definition version.

The canonical binary representation of a Definition SHOULD use CBOR with ordered arrays. JSON MAY be used for authoring, inspection, or debugging.

---

## 3. Definition Ordering

Field order has wire-semantic meaning because position identifies a field.

The author MAY choose any useful field ordering when creating a Definition. Human-readable ordering is encouraged unless measurements justify another strategy.

Once published, field order MUST NOT change within that Definition version.

Schema serialization MUST preserve field order explicitly. Unordered map iteration MUST NOT determine field position.

---

## 4. PIL Types

Types and their physical representations are registered by the PIL version rather than redefined by individual Definitions.

PIL 0.x defines at least:

```text
BOOL

I8
I16
I32
I64

U8
U16
U32
U64

F32
F64

UUID
TIMESTAMP

STRING
BYTES
CBOR

ARRAY<T>
```

Fixed-width primitives have protocol-defined widths.

`STRING` contains UTF-8.

`BYTES` contains arbitrary bytes.

`CBOR` contains exactly one CBOR data item.

`ARRAY<T>` is a homogeneous array whose element type is defined by the Definition.

PIL 0.x MAY use:

```text
ARRAY<CBOR>
```

for arrays of complex values.

PIL 0.x does not define recursively positional child objects. Complex child structures use CBOR.

---

## 5. Binary Conventions

PIL 0.x uses little-endian encoding for multi-byte numeric primitives.

Signed integers use two's-complement representation.

`F32` and `F64` use IEEE-754 binary32 and binary64 representations.

`BOOL` MUST encode:

```text
0x00 = false
0x01 = true
```

Other BOOL values are invalid.

UUID values occupy 16 bytes. The exact UUID byte-order convention MUST be fixed before PIL 0.x wire compatibility is declared stable.

TIMESTAMP representation MUST likewise be fixed before PIL 0.x is declared stable.

---

## 6. Field Mask

Every field receives exactly two state bits in Definition order:

```text
00  ABSENT
01  NULL
10  DIRECT
11  OFFSET
```

For `N` fields, the mask occupies:

```text
ceil(N × 2 / 8)
```

bytes.

Unused bits in the final mask byte MUST be zero.

The bit ordering within each byte MUST be fixed by PIL 0.x before wire compatibility is declared stable.

### ABSENT

No value is supplied.

For partial state, ABSENT means unchanged.

For complete state, ABSENT means no value is present.

### NULL

The field explicitly contains null.

NULL is distinct from ABSENT.

### DIRECT

The field's value is present directly in the Direct Value Section.

### OFFSET

The field's value resides in the Variable Value Section. Its corresponding End Offset appears in the End Offset Section.

---

## 7. Type and Mask Interpretation

The PIL version determines which mask states are valid for each type.

Typical PIL 0.x behavior is:

| Type                         | DIRECT           | OFFSET        |
| ---------------------------- | ---------------- | ------------- |
| Fixed-width primitive        | literal value    | invalid       |
| STRING without mapped values | invalid          | UTF-8         |
| STRING with mapped values    | mapped reference | UTF-8         |
| BYTES                        | invalid          | byte sequence |
| CBOR                         | invalid          | CBOR item     |
| ARRAY<T>                     | invalid          | encoded array |

Thus a mapped STRING can represent either:

```text
DIRECT → known mapped value
OFFSET → arbitrary UTF-8 value
```

This provides extensible-enumeration behavior without a separate field type.

Invalid type/mask combinations MUST be rejected.

---

## 8. Mapped Values

A field MAY define an ordered set of common values.

Example:

```text
status STRING

0 active
1 pending
2 disabled
```

A DIRECT value for that field is the numeric position in its mapped-value array.

Mapped references SHOULD use the smallest protocol-defined representation capable of representing the field's mapped-value set.

No mapped-value strings are transmitted in ordinary frames.

Mapped values are part of the immutable PIL Definition. Modifying their meaning or ordering requires a new Definition version.

---

## 9. Frame Layout

A PIL frame consists of:

```text
┌──────────────────────────────┐
│ Field Mask                   │
├──────────────────────────────┤
│ Direct Value Section         │
├──────────────────────────────┤
│ End Offset Section           │
├──────────────────────────────┤
│ Variable Value Section       │
└──────────────────────────────┘
```

ABSENT and NULL fields consume no bytes outside the Field Mask.

DIRECT values MUST appear in Definition order for fields marked DIRECT.

End Offsets MUST appear in Definition order for fields marked OFFSET.

Variable values MUST appear contiguously in the same order as their corresponding OFFSET fields.

---

## 10. Direct Value Section

The Direct Value Section contains only values for fields marked DIRECT.

Each value's width and interpretation are determined by its Definition type and PIL version.

Example:

```text
Definition:

0 id      UUID
1 age     U16
2 status  STRING(values=[active,pending,disabled])
```

A frame marking all three DIRECT contains:

```text
[UUID:16]
[age:2]
[status mapped-reference]
```

No field identifiers or type identifiers appear in this section.

---

## 11. Variable Value Section

The Variable Value Section contains only values for fields marked OFFSET.

Values:

* appear in Definition order;
* are contiguous;
* contain no inter-value padding;
* do not overlap.

The value's representation is determined by its PIL type.

The Variable Value Section itself contains no field identifiers.

---

## 12. End Offsets

Each OFFSET field has one **End Offset**.

An End Offset is the cumulative byte position immediately following that field's value, measured from the beginning of the Variable Value Section.

For:

```text
firstName = "Jason"
lastName  = "Smith"
email     = "j@example.com"
```

the variable bytes are conceptually:

```text
JasonSmithj@example.com
0    5    10           23
```

and the End Offsets are:

```text
5
10
23
```

Therefore:

```text
start(first) = 0

start(n) =
  end(n - 1)

length(n) =
  end(n) - start(n)
```

The final End Offset is also the total Variable Value Section length.

No separate length prefix is required for ordinary OFFSET field values.

End Offsets MUST be monotonically increasing.

---

## 13. Extended Unsigned Integer

End Offsets use an extended unsigned integer optimized for small values.

### 16-bit form

Values:

```text
0x0000 – 0xFFFE
```

are encoded directly as `uint16`.

`0xFFFF` is reserved as an extension marker.

### 32-bit form

A value requiring 32 bits is encoded:

```text
FFFF
<uint32>
```

Within the 32-bit extension:

```text
0xFFFFFFFF
```

is reserved as the next extension marker.

### 64-bit form

A value requiring 64 bits is encoded:

```text
FFFF
FFFFFFFF
<uint64>
```

Thus:

```text
42
→ uint16(42)

65534
→ uint16(65534)

65535
→ FFFF + uint32(65535)

100000
→ FFFF + uint32(100000)

value beyond uint32 direct capacity
→ FFFF + FFFFFFFF + uint64(value)
```

All integers use PIL byte order.

Encoders MUST use the shortest valid representation.

Extended encoding of a value representable by a shorter form is non-canonical.

---

## 14. Frame Boundary

PIL frames do not require an explicit frame-length field.

The Definition and Field Mask determine:

* mask length;
* number and width of DIRECT values;
* number of End Offsets.

These determine the beginning of the Variable Value Section.

If no OFFSET fields exist:

```text
frame end = end of Direct Value Section
```

If OFFSET fields exist:

```text
frame end =
  beginning of Variable Value Section
  + final End Offset
```

The final End Offset therefore provides both:

* the final variable-value boundary;
* the PIL frame boundary.

This permits consecutive PIL frames to be decoded from a byte stream without adding a frame-length prefix.

---

## 15. Arrays

Arrays are variable values and MUST use OFFSET.

An array is homogeneous and its element type is defined by:

```text
ARRAY<T>
```

The encoded array begins with an element count represented using PIL's extended unsigned integer.

### Fixed-width elements

Example:

```text
ARRAY<U16>

[count]
[u16]
[u16]
[u16]
...
```

Element lengths are implicit from the protocol type.

### Variable-width elements

Variable-width element encodings MUST provide sufficient boundary information to decode the declared element count.

For example, `ARRAY<STRING>` may use element end offsets followed by contiguous UTF-8 element bytes.

The exact array sub-layout MUST be defined by PIL 0.x before wire compatibility is frozen.

### Complex elements

PIL 0.x supports:

```text
ARRAY<CBOR>
```

for arrays containing complex values.

Each element MUST contain exactly one CBOR item.

PIL 0.x does not recursively encode arbitrary child objects using PIL Definitions.

---

## 16. CBOR

CBOR is PIL 0.x's escape mechanism for complex structure.

A CBOR field contains exactly one complete CBOR data item within the byte range established by its PIL End Offset.

CBOR values MUST fit entirely within their assigned range.

For `@fields`, the CBOR value MUST be a map with:

* text keys;
* unique keys;
* no key colliding with an explicitly defined PIL field.

Duplicate keys MUST be rejected.

Implementations SHOULD enforce reasonable limits on CBOR nesting depth and size.

---

## 17. Reserved `@` Namespace

Field names beginning with:

```text
@
```

are reserved for PIL-defined semantics.

Application Definitions MUST NOT create unregistered `@` fields.

PIL 0.x defines:

```text
@fields
```

---

## 18. `@fields`

`@fields` is a virtual extension field allowing attributes not present in the PIL Definition.

It MUST have type:

```text
CBOR
```

and MUST contain a CBOR map when present.

Example:

```text
@fields:

{
  "favoriteColor": "blue",
  "shoeSize": 11
}
```

When materialized, these entries are added to the root logical object:

```json
{
  "favoriteColor": "blue",
  "shoeSize": 11
}
```

`@fields` itself MUST NOT appear as an application-level property.

A key in `@fields` MUST NOT duplicate an explicitly defined field.

Collisions MUST be rejected. PIL defines no overwrite precedence.

For partial state:

```text
ABSENT
→ preserve existing dynamic attributes

OFFSET containing {}
→ replace dynamic attributes with an empty set

OFFSET containing {...}
→ replace the complete dynamic-attribute set
```

`NULL` is invalid for `@fields` in PIL 0.x.

A Definition without `@fields` does not permit dynamic root attributes.

---

## 19. Complete and Partial State

PIL uses the same frame representation for complete and partial state.

Example Definition:

```text
0 id
1 status
2 firstName
3 lastName
```

Changing only `status`:

```text
MASK

00 10 00 00

DIRECT SECTION

[status-reference]
```

Changing only `firstName`:

```text
MASK

00 00 11 00

END OFFSETS

[5]

VARIABLE SECTION

Jason
```

Nullifying `lastName`:

```text
MASK

00 00 00 01
```

No placeholder value bytes are emitted for ABSENT or NULL fields.

Whether a frame represents complete state or partial state is determined by the enclosing application or transfer profile.

PIL does not define deletion semantics.

NULL means null, not delete.

---

## 20. Multiple Frames

PIL does not assign application meaning to sequences of frames.

An enclosing transfer MAY contain:

```text
[PIL frame]
[PIL frame]
[PIL frame]
...
```

Because frames are self-delimiting, they can be decoded sequentially.

An enclosing application may interpret such a sequence as:

* collection members;
* successive state changes;
* replay data;
* another application-defined sequence.

Those semantics are outside PIL.

Arrays contained *within* a PIL resource use `ARRAY<T>` rather than multiple top-level frames.

---

## 21. Definition Binding

A PIL frame MUST NOT be interpreted without:

```text
compatible PIL version
+
exact PIL Definition version
```

The Definition MUST be available before decoding begins.

A transfer binding MUST associate the byte stream with an exact Definition identity/version.

The Definition need not be repeated with every frame.

Definition changes MUST occur at explicit transfer boundaries. A frame MUST NOT begin under one Definition and finish under another.

---

## 22. HTTP Binding Guidance

PIL itself does not define HTTP behavior.

When PIL is carried over HTTP, implementations SHOULD communicate:

* supported PIL version;
* Definition identity;
* Definition version;

through HTTP headers or equivalent transfer metadata.

A client SHOULD be able to advertise its cached Definition version when requesting PIL content.

Conceptually:

```text
GET /users/123
Accept: application/pil
PIL-Version: 0
PIL-Definition-Version: 41
```

If version 41 remains current, the server may immediately return PIL frames interpreted under that Definition.

If the client Definition is absent or stale, the current Definition MUST be made available before frames using it are interpreted.

Definitions are immutable and therefore suitable for client and intermediary caching.

Exact header names, negotiation behavior, and HTTP/3 streaming semantics belong to a separate PIL/HTTP binding specification.

---

## 23. Validation

A conforming implementation MUST reject:

* incompatible PIL versions;
* unknown Definition versions;
* malformed Field Masks;
* invalid type/state combinations;
* invalid mapped-value references;
* non-canonical extended integers where canonical encoding is required;
* decreasing End Offsets;
* End Offsets outside available data;
* malformed arrays;
* malformed CBOR;
* duplicate `@fields` keys;
* `@fields` collisions with defined fields;
* invalid reserved `@` fields.

All size, offset, and arithmetic operations MUST be checked for overflow before memory access.

---

## 24. Scope

PIL defines:

```text
Definitions
types
field masks
mapped values
direct values
end offsets
variable values
arrays
frame boundaries
reserved extension semantics
```

PIL does not define:

```text
HTTP methods
paths
status codes
connection lifecycle
subscription behavior
persistence
event logging
cache policy
authorization
database behavior
business semantics
```

Those concerns belong to enclosing application and transfer profiles.

---

## 25. Core Principle

PIL trades repeated self-description for shared interpretation.

```text
field identity
    → Definition position

common enumerated value
    → mapped reference

fixed unique value
    → DIRECT

variable unique value
    → OFFSET + contiguous bytes

complex value
    → CBOR

dynamic root attributes
    → @fields
```

The Definition communicates shared structure and vocabulary.

The frame communicates state.

> **Share structure and vocabulary once; interchange only state.**
