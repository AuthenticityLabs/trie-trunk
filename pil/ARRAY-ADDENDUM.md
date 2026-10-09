# PIL Array Encoding Addendum

PIL supports homogeneous `ARRAY<T>` fields. Arrays are stored in the variable
section and bounded by the parent field's End Offset.

### Fixed-width elements

For protocol-defined fixed-width types:

```text
[count][value][value]...
```

Element boundaries are implied by type width.

### Variable-width elements

For `STRING`, `BYTES`, and `CBOR`:

```text
[count][end₁][end₂]...[endₙ][value₁][value₂]...[valueₙ]
```

End Offsets are cumulative byte offsets relative to the beginning of the
element-value section.

Example:

```text
["Jason", "Smith", "Alexander"]

[3][5][10][19][JasonSmithAlexander]
```

Counts and End Offsets use PIL's extended unsigned integer encoding
(`u16 → u32 → u64` via reserved maximum values).

The parent field mask determines array state:

```text
00 ABSENT
01 NULL
11 OFFSET
```

`10 DIRECT` is invalid for arrays.

PIL 0.x does not support null array elements. An empty array contains only
`count = 0`, with no End Offsets or element bytes.

`ARRAY<CBOR>` provides arrays of complex values without introducing recursively
encoded PIL objects.
