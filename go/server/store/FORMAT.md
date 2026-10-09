# Storage Format

Format 2 remains supported. New partitioned storage is documented in [FORMAT-3.md](FORMAT-3.md).

This specifies the files currently written by `go/server/store`. **Storage Format** describes the byte layouts; the **schema version** selects the assigned field
and value meanings in `log.map`. They are separate version numbers.

Format 2 replaces the prototype's daily trie and inline index payloads. It is not
an in-place migration of format 1; use a fresh tap for sandboxing.

## Reading the examples

- All fixed-width integers are **unsigned, big-endian**, with no alignment padding.
  `u16`, `u32` and `u64` occupy exactly 2, 4 and 8 bytes. CBOR integers use CBOR's
  variable-width encoding instead.
- Offsets are zero-based. Tables use decimal offsets unless stated otherwise.
  Ranges use `[start, end)`: the start is included and the end is excluded.
- Hex listings use hexadecimal file offsets in the left column. Spaces, comments
  and line breaks make the examples readable; none are stored delimiters.
- Decoded CBOR examples use diagnostic notation: integer keys are unquoted, text
  is quoted. They are illustrations, not JSON stored on disk.
- The complete hex examples below come from `logs/sandbox/42`, using two synthetic
  events on **2026-09-23**, UTC day **266**. Their field numbers are example schema
  assignments, not permanent global constants.

## Ownership, paths and file roles

A `Manager` owns one tap under `Config.Root`, locked with `.writer.lock`.
The low-level `Config.Tap` defaults to `0`; network ingestion always supplies the
explicit name from `/<tap>`. Sandbox examples use `sandbox`. The lock is an OS
coordination file, not part of the event data format.

```text
logs/<tap>/<schema-version-uint32>/
    log.map                 assigned definition for this schema version
    log.tri                 shared lineage for all days in this version
    log.y26                 2026 total
    2026/
        log.w52             ISO week 52 total
        log.m12             December total
        log.365             separate daily totals for this year
        365/
            log.idx         UUIDv7 rows pointing into log.tri and log.pop
            log.pop         per-event CBOR data
            log.sum         five-minute aggregate contributions
```

The schema directory is the decimal representation of the uint32 version, e.g.
`3145770` for packed `3.42` (`3 << 20 | 42`, using 12 major and 20 minor bits).
All files inherit that directory's map. Index, trie, population and aggregate
headers contain no schema version or map digest.

Daily paths use UTC year and three-digit day-of-year (`001`–`365`, or `366` in a
leap year). ISO weekly files use the ISO week-year, which can differ from the
calendar year near New Year; week 53 is supported. `log.365` remains the filename
in leap years. Yearly files use the agreed two-digit suffix, e.g. `log.y26`.

| File | Scope | Magic | Contents after the file header |
| --- | --- | --- | --- |
| `log.map` | Schema version | None | One CBOR definition; **no file header or blocks** |
| `log.tri` | Schema version | `TTRI` | One trie node per block |
| `log.idx` | UTC day | `TIDX` | One sorted run of fixed rows per block |
| `log.pop` | UTC day | `TPOP` | One concatenated CBOR population batch per index run |
| `log.sum` | UTC day | `TSUM` | One five-minute contribution per block |
| `log.365` | UTC year | `TAGG` | One final daily total per published day |
| `log.wWW` | ISO week | `TAGG` | One final weekly total |
| `log.mMM` | UTC month | `TAGG` | One final monthly total |
| `log.yYY` | UTC year | `TAGG` | One final yearly total |

## Common file header and block framing

Every file **except `log.map`** begins with this 8-byte header:

| File offset | Width | Encoding | Meaning |
| --- | --- | --- | --- |
| 0 | 4 | ASCII bytes | Magic from the table above |
| 4 | 4 | `u32` | Storage format number: `2` |

Zero or more blocks follow. The first block starts at file offset 8. If a block
starts at offset `B`, its layout is:

| Offset relative to `B` | Width | Encoding | Meaning |
| --- | --- | --- | --- |
| 0 | 4 | `u32` | `L`, payload length, excluding this block header |
| 4 | 4 | `u32` | CRC32C of the next `L` payload bytes only |
| 8 | `L` | File-specific | Payload |

```text
file:   [magic:4][format:4] [length:4][crc:4][payload:L] [next block ...]
        <---- 8 bytes ---> <-------- 8 + L bytes ----->

payload_start = B + 8
next_block    = B + 8 + L
```

`L` must be between 1 and 16,777,216 inclusive (16 MiB). There is no padding,
terminator, CR/LF, end marker or footer. A header-only file is 8 bytes long and
contains no blocks. The transport's two-byte CBOR length prefix is **not** copied
into these disk files.

CRC32C uses Castagnoli, exactly Go's
`crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli))`: reflected polynomial
`0x82f63b78`, initial register `0xffffffff`, final XOR `0xffffffff`. The check value
for ASCII `123456789` is `0xe3069283`. The resulting checksum is stored big-endian.
Magic, format, block length and the checksum field itself are outside CRC coverage.

## log.map — assigned dictionary and storage plan

The entire file is one deterministic CBOR `codec.Definition`, encoded using
`cbor.CoreDetEncOptions()`. Its size is currently limited to 1–65,535 bytes. There
is no magic, length prefix, checksum trailer, timestamp or `Revision` wrapper.
The definition contains its version once; it must match the enclosing directory.
The negotiation digest is SHA-256 of these exact bytes and is not stored in the
other files.

### Definition map

These are **CBOR integer keys**, not fixed-width byte offsets:

| Key | Name | CBOR value | Presence |
| --- | --- | --- | --- |
| 0 | `version` | Unsigned integer, nonzero uint32 | Required |
| 1 | `fields` | Map: uint16 field number → field-definition map | Required, nonempty |
| 2 | `dictionary` | Text string naming the dictionary | Omitted when empty |
| 3 | `domains` | Map: domain text → map of uint16 value number → text label | Omitted when empty |
| 4 | `timezone` | Text string | Store requires explicit `"UTC"` |
| 5 | `path` | Array of uint16 field numbers, in trie insertion order | Omitted when empty |
| 6 | `aggregations` | Array of text strings | Store requires the list shown below |

Each field definition is another CBOR map:

| Key | Name | CBOR value | Presence |
| --- | --- | --- | --- |
| 0 | `name` | Text string | Always encoded |
| 1 | `domain` | Text string referring to a named domain | Omitted when empty |
| 2 | `values` | Map: uint16 value number → text label | Omitted when empty |
| 3 | `object` | Nested field-number → field-definition map | Omitted when empty |
| 4 | `array` | Field-number → field-definition map for object elements | Omitted when empty |
| 5 | `unit` | Text string, e.g. `"us"` | Omitted when empty |
| 6 | `encoding` | Text string, e.g. `"uuidv7"` | Omitted when empty |

The store requires one field named `id` with encoding `uuidv7`, and one named
`duration` with unit `us`. A path contains at most 32 distinct, defined field
numbers and cannot include either of those fields.

### Complete sandbox definition, decoded

```text
{
  0: 42,
  1: {
     3: {0: "level",       2: {5: "ERROR"}},
     4: {0: "service",     2: {1: "vault"}},
     8: {0: "id",          6: "uuidv7"},
    12: {0: "duration",    5: "us"},
    14: {0: "http_status"},
    15: {0: "message"},
    21: {0: "fields"}
  },
  4: "UTC",
  5: [3, 14, 4],
  6: ["count", "duration.count", "duration.sum", "duration.min", "duration.max"]
}
```

Field `3`'s value `5` means `ERROR`; field `4`'s value `1` means `vault`. Field
`14` has no value dictionary, so `401` remains the number 401. The `path` array
orders the lineage as `level → http_status → service`; numeric map-key order does
not choose trie order.

The complete 182-byte file follows. For example, `a5` opens a five-pair CBOR map,
`00` is definition key 0, and `18 2a` encodes version 42 in two bytes.

```text
# logs/sandbox/42/log.map
0000  a5 00 18 2a 01 a7 03 a2 00 65 6c 65 76 65 6c 02
0010  a1 05 65 45 52 52 4f 52 04 a2 00 67 73 65 72 76
0020  69 63 65 02 a1 01 65 76 61 75 6c 74 08 a2 00 62
0030  69 64 06 66 75 75 69 64 76 37 0c a2 00 68 64 75
0040  72 61 74 69 6f 6e 05 62 75 73 0e a1 00 6b 68 74
0050  74 70 5f 73 74 61 74 75 73 0f a1 00 67 6d 65 73
0060  73 61 67 65 15 a1 00 66 66 69 65 6c 64 73 04 63
0070  55 54 43 05 83 03 0e 04 06 85 65 63 6f 75 6e 74
0080  6e 64 75 72 61 74 69 6f 6e 2e 63 6f 75 6e 74 6c
0090  64 75 72 61 74 69 6f 6e 2e 73 75 6d 6c 64 75 72
00a0  61 74 69 6f 6e 2e 6d 69 6e 6c 64 75 72 61 74 69
00b0  6f 6e 2e 6d 61 78
```

Installation is immutable: identical bytes at the same version are accepted;
different bytes are rejected. This is the implemented **assigned** definition,
not the future client semantic-registration format. Generic type, sensitivity,
default/omission and indexing-policy metadata have no assigned keys here yet.
Server assignment and statistical schema revision policy are separate work.

## log.tri — shared lineage for a whole schema version

Each block stores exactly one node:

| Payload offset | Width | Encoding | Meaning |
| --- | --- | --- | --- |
| 0 | 4 | `u32` | Parent node reference |
| 4 | 2 | `u16` | Assigned field number |
| 6 | 1, 2, 3, 5 or 9 | CBOR unsigned integer | Dictionary reference or numeric field value |

The value uses the shortest CBOR unsigned encoding. Thus payload lengths are
7, 8, 9, 11 or 15 bytes, and framed node sizes are 15, 16, 17, 19 or 23 bytes.
For example, value `5` is `05`, value `24` is `18 18`, and value `401` is
`19 01 91`. The field number itself is always a fixed two-byte value in this file.

Node references are **ordinals**, starting at 1 in file order; the ordinal is not
stored in the node. Reference 0 is the implicit empty/root lineage. References
are not byte offsets. A node's parent must precede it, its field must exist in the
map, and a `(parent, field, encoded value)` edge is unique. References never change.
Nodes contain no entry UUID, duration, population tail or per-event count.

### Sandbox lineage

```text
0  root
└─ 1  field 3  = 5    level = ERROR
   └─ 2  field 14 = 401  http_status = 401
      └─ 3  field 4  = 1    service = vault
```

Both example index rows hold leaf reference `3`. Reconstruction follows parents
`3 → 2 → 1 → 0`. All days under version `42` reuse these nodes.

| Node reference | Block byte range | Parent | Field | Value bytes | Decoded value |
| --- | --- | --- | --- | --- | --- |
| 1 | `[8, 23)` | 0 | 3 | `05` | 5 / `ERROR` |
| 2 | `[23, 40)` | 1 | 14 | `19 01 91` | 401 |
| 3 | `[40, 55)` | 2 | 4 | `01` | 1 / `vault` |

Complete 55-byte file, grouped by header and node:

```text
# logs/sandbox/42/log.tri
0000  54 54 52 49 00 00 00 02                 # TTRI, format 2
0008  00 00 00 07 ba 28 d6 e8                 # node 1: length 7, CRC32C
0010  00 00 00 00 00 03 05                    # parent 0, field 3, value 5
0017  00 00 00 09 f8 07 44 7c                 # node 2: length 9, CRC32C
001f  00 00 00 01 00 0e 19 01 91              # parent 1, field 14, value 401
0028  00 00 00 07 b8 b8 ab 33                 # node 3: length 7, CRC32C
0030  00 00 00 02 00 04 01                    # parent 2, field 4, value 1
```

Only explicitly planned unsigned numeric path fields enter the trie. Missing
fields are skipped; literal text stays in `log.pop`. The schema planner must keep
high-cardinality and sensitive fields out of the path; the file format does not
classify them automatically. One bounded edge cache is shared across open days
of a version. `MaxNodes` includes the root. If adding a path would exceed capacity,
the event retains all its path fields in `log.pop` and uses leaf 0. Existing
complete paths remain usable.

## log.idx — fixed rows in immutable sorted runs

Each block is one run:

| Payload offset | Width | Encoding | Meaning |
| --- | --- | --- | --- |
| 0 | 4 | `u32` | Row count `N`, at least 1 |
| 4 | `36 × N` | Rows below | Strictly increasing UUID order within this run |

Each row occupies **exactly 36 bytes**, with no CBOR wrapping:

| Row-relative offset | Width | Encoding | Meaning |
| --- | --- | --- | --- |
| 0 | 16 | Raw UUIDv7 bytes | Entry identifier and transaction start time |
| 16 | 4 | `u32` | Leaf ordinal in the version's `log.tri`; 0 means root |
| 20 | 8 | `u64` | Absolute start offset in this day's `log.pop` |
| 28 | 8 | `u64` | Absolute end offset in `log.pop`, exclusive |

```text
row: [uuidv7:16][leaf:4][pop_start:8][pop_end:8]
     <---------------- 36 bytes ------------->

run_payload_bytes = 4 + 36 × N
run_block_bytes   = 8 + 4 + 36 × N
row_i_file_offset = block_start + 8 + 4 + 36 × i     (i starts at 0)
```

UUIDs use their canonical 16-byte binary order, not textual UUID characters or
platform-specific GUID byte swapping. The first six bytes encode Unix epoch
milliseconds, most significant byte first; version and variant bits must identify
UUIDv7. The UTC day comes from that timestamp. All 16 bytes participate in sorting
and duplicate detection; different UUIDs within the same millisecond remain
different events. There is no separate start/end timestamp or duration in the row.

Runs append without rewriting existing rows. Runs can overlap in time, so global
ordering requires a merge. Replay checks inspect pending rows and candidate
committed runs without decoding `log.pop`. Deduplication scope is
tap/version/day; there is no whole-history UUID map.

### Sandbox index

One run contains these two rows:

| Row | Row file offset | UUIDv7 | Leaf | Population range | Population bytes |
| --- | --- | --- | --- | --- | --- |
| 0 | 20 | `01a0d002-8ce1-74be-a349-235d6a6848c3` | 3 | `[16, 69)` | 53 |
| 1 | 56 | `01a0d002-8ce1-7c39-bd98-e18815139a71` | 3 | `[69, 122)` | 53 |

Both timestamps decode to `2026-09-23T20:43:43.201Z`. The run payload is
`4 + 2 × 36 = 76` bytes. With both headers, the file is `8 + 8 + 76 = 92` bytes.

```text
# logs/sandbox/42/2026/266/log.idx
0000  54 49 44 58 00 00 00 02                 # TIDX, format 2
0008  00 00 00 4c 50 81 36 83                 # payload 76, CRC32C
0010  00 00 00 02                             # row count 2
0014  01 a0 d0 02 8c e1 74 be a3 49 23 5d 6a 68 48 c3 # row 0 UUID
0024  00 00 00 03                             # leaf 3
0028  00 00 00 00 00 00 00 10                 # pop_start 16
0030  00 00 00 00 00 00 00 45                 # pop_end 69
0038  01 a0 d0 02 8c e1 7c 39 bd 98 e1 88 15 13 9a 71 # row 1 UUID
0048  00 00 00 03                             # leaf 3
004c  00 00 00 00 00 00 00 45                 # pop_start 69
0054  00 00 00 00 00 00 00 7a                 # pop_end 122
```

## log.pop — populate the dehydrated entry

A population block holds the concatenation of the per-event CBOR maps for exactly
one index run, in that run's UUID order:

```text
[file header:8]
[batch length:4][batch CRC:4][CBOR map for row 0][CBOR map for row 1]...
                            ^ pop_start        ^ next pop_start
```

There is no outer CBOR array, per-event length prefix or delimiter. Index ranges
address the map bytes only, excluding the block header. The first map starts at
file offset 16. Within a run, each `pop_end` equals the next row's `pop_start`.
Between runs, the next start is the preceding end plus 8 for the new block header.

A row's range must be nonempty, at most 65,535 bytes, and end at or below
`2^63 - 1` (the implementation's signed file-offset limit). The block length is
`last_row.pop_end - first_row.pop_start`. One CRC covers that entire batch;
verification of a batch checks the whole batch, not just an individual row's map.

The population map uses the assigned numeric field keys. It retains duration in
microseconds, literal values, nested objects and other non-shared data. It omits
the UUID and whichever fields were extracted into the lineage. Outer map keys
are deterministically encoded; nested raw CBOR values are preserved, so readers
must not assume all nested values have been re-encoded canonically.

### Sandbox population, decoded

```text
[16, 69):
{
  12: 2237,
  15: "unauthorized",
  21: {"name": "Synthetic Customer", "sample": 3}
}

[69, 122):
{
  12: 2236,
  15: "unauthorized",
  21: {"name": "Synthetic Customer", "sample": 2}
}
```

The example `fields` object has no nested assigned schema, so its keys remain
full CBOR text. `name` is synthetic per-event data and never appears in the trie
or value dictionary. Each map is 53 bytes; the batch is 106 bytes; the complete
file is 122 bytes.

```text
# logs/sandbox/42/2026/266/log.pop
0000  54 50 4f 50 00 00 00 02                 # TPOP, format 2
0008  00 00 00 6a 37 c3 62 a5                 # payload 106, CRC32C
0010  a3 0c 19 08 bd                          # map of 3 pairs; field 12 = 2237
0015  0f 6c 75 6e 61 75 74 68 6f 72 69 7a 65 64 # field 15 = "unauthorized"
0023  15 a2                                   # field 21 = map of 2 pairs
0025  64 6e 61 6d 65                          # "name"
002a  72 53 79 6e 74 68 65 74 69 63 20 43 75 73 74 6f 6d 65 72 # "Synthetic Customer"
003d  66 73 61 6d 70 6c 65 03                 # "sample" = 3
0045  a3 0c 19 08 bc                          # second map; field 12 = 2236
004a  0f 6c 75 6e 61 75 74 68 6f 72 69 7a 65 64 # field 15 = "unauthorized"
0058  15 a2                                   # field 21 = map of 2 pairs
005a  64 6e 61 6d 65                          # "name"
005f  72 53 79 6e 74 68 65 74 69 63 20 43 75 73 74 6f 6d 65 72 # "Synthetic Customer"
0072  66 73 61 6d 70 6c 65 02                 # "sample" = 2
```

## Shared aggregate group layout

Both five-minute contributions and higher totals use this fixed 36-byte group:

| Group-relative offset | Width | Encoding | Meaning |
| --- | --- | --- | --- |
| 0 | 4 | `u32` | Trie leaf ordinal |
| 4 | 8 | `u64` | Event count |
| 12 | 8 | `u64` | Sum of durations, microseconds |
| 20 | 8 | `u64` | Minimum duration, microseconds |
| 28 | 8 | `u64` | Maximum duration, microseconds |

Every accepted event has a duration, so `count` and `duration.count` share the
single count field. An average is derived from `duration_sum / count`; it is not
stored. Use fractional division when a fractional microsecond average is wanted.
Groups are written in ascending leaf order, without duplicate leaf references.
Every stored group has a positive count and `min <= max`. Leaf 0 is valid and
groups events whose lineage is empty or fell back to population storage.

## log.sum — five-minute contributions

Each `TSUM` block contains:

| Payload offset | Width | Encoding | Meaning |
| --- | --- | --- | --- |
| 0 | 2 | `u16` | Five-minute bucket, 0–287 |
| 2 | 8 | `u64` | `through_index_offset`, exclusive committed run end in this day's `log.idx` |
| 10 | 4 | `u32` | Group count `G` |
| 14 | `36 × G` | Aggregate groups | Layout above |

Payload length is exactly `14 + 36 × G`; framed block length is `22 + 36 × G`.
Bucket `b` covers `[UTC midnight + b × 300 seconds, UTC midnight + (b+1) × 300
seconds)`, selected from the event UUID timestamp. No date or timestamp is stored
in the block; the day directory and bucket supply them.

At current bucket `n`, `n-2` and earlier are eligible for publication. A published
block is an immutable **contribution**, not a replacement total. Late events can
append another block for the same bucket. Combine counts and sums by addition,
minimums with `min`, and maximums with `max`. Never sum averages or average averages.

The watermark is a **byte offset**, not a row count or timestamp. It advances
strictly for successive contributions to the same bucket. It records how far
that bucket's aggregation has processed committed index runs and prevents their
reapplication during recovery. Different buckets may carry the same watermark.

### Sandbox five-minute contribution

```text
bucket:               248 = [2026-09-23T20:40:00Z, 2026-09-23T20:45:00Z)
through_index_offset:  92  = end of the two-row index run
Groups:
  leaf 3: count=2, duration_sum=4473 us, min=2236 us, max=2237 us
```

With one group, payload length is `14 + 36 = 50` and file size is 66 bytes.

```text
# logs/sandbox/42/2026/266/log.sum
0000  54 53 55 4d 00 00 00 02                 # TSUM, format 2
0008  00 00 00 32 14 bc b6 ad                 # payload 50, CRC32C
0010  00 f8                                   # bucket 248
0012  00 00 00 00 00 00 00 5c                 # through index byte 92
001a  00 00 00 01                             # group count 1
001e  00 00 00 03                             # leaf 3
0022  00 00 00 00 00 00 00 02                 # count 2
002a  00 00 00 00 00 00 11 79                 # sum 4473 us
0032  00 00 00 00 00 00 08 bc                 # min 2236 us
003a  00 00 00 00 00 00 08 bd                 # max 2237 us
```

For a later 3000 us event in bucket 248 on the same leaf, a new contribution would
contain `count=1, sum=3000, min=3000, max=3000` and a later index watermark. The
combined result would be `count=3, sum=7473, min=2236, max=3000`. The existing
66 bytes would remain unchanged. This late-event example is illustrative; it is
not present in the sandbox file.

## log.365, log.wWW, log.mMM, log.yYY — final period totals

All four use magic `TAGG` and exactly the same block payload:

| Payload offset | Width | Encoding | Meaning |
| --- | --- | --- | --- |
| 0 | 2 | `u16` | Period key, interpreted by the filename as below |
| 2 | 4 | `u32` | Group count `G` |
| 6 | `36 × G` | Aggregate groups | Same 36-byte group layout as `log.sum` |

Payload length is `6 + 36 × G`; framed block length is `14 + 36 × G`. There is no
index watermark, explicit period start/end or schema reference. A published empty
period may have `G=0`: its payload is 6 bytes and its framed block is 14 bytes.

| Filename and placement | Period key | Exact period selection |
| --- | --- | --- |
| `<yyyy>/log.365` | Day-of-year 1–365/366 | UTC midnight to next UTC midnight for that day |
| `<ISO-yyyy>/log.wWW` | 0 | ISO Monday 00:00 UTC to following Monday; `WW` is 01–52/53 |
| `<yyyy>/log.mMM` | 0 | First of month 00:00 UTC to first of next month; `MM` is 01–12 |
| `log.yYY` | 0 | January 1 00:00 UTC to next January 1 for the named year |

`log.365` holds multiple daily blocks, one per published day, at most 366. Their
append order is publication order, not necessarily calendar order. Weekly,
monthly and yearly filenames each select one period, so each holds at most one
published total block. No additional on-disk aggregate index is present: the
current reader scans blocks to find the period key.

### Sandbox daily total

```text
file:       logs/sandbox/42/2026/log.365
period_key: 266 = [2026-09-23T00:00:00Z, 2026-09-24T00:00:00Z)
group_count: 1
leaf 3: count=2, duration_sum=4473 us, min=2236 us, max=2237 us
```

One group gives a 42-byte payload and a 58-byte file:

```text
# logs/sandbox/42/2026/log.365
0000  54 41 47 47 00 00 00 02                 # TAGG, format 2
0008  00 00 00 2a 2d 5f 7e f8                 # payload 42, CRC32C
0010  01 0a                                   # period key 266
0012  00 00 00 01                             # group count 1
0016  00 00 00 03                             # leaf 3
001a  00 00 00 00 00 00 00 02                 # count 2
0022  00 00 00 00 00 00 11 79                 # sum 4473 us
002a  00 00 00 00 00 00 08 bc                 # min 2236 us
0032  00 00 00 00 00 00 08 bd                 # max 2237 us
```

### Weekly, monthly and yearly examples

The following are **illustrative**, not additional sandbox files. Assume the two
events are the only events in each completed period. All three totals would have
`period_key=0, group_count=1, leaf=3, count=2, sum=4473, min=2236, max=2237`:

| File under `logs/sandbox/42/` | Period, start inclusive / end exclusive | File size |
| --- | --- | --- |
| `2026/log.w39` | 2026-09-21 00:00 UTC / 2026-09-28 00:00 UTC | 58 bytes |
| `2026/log.m09` | 2026-09-01 00:00 UTC / 2026-10-01 00:00 UTC | 58 bytes |
| `log.y26` | 2026-01-01 00:00 UTC / 2027-01-01 00:00 UTC | 58 bytes |

Their bytes would match the daily layout except for `period_key=00 00` and the
recomputed CRC32C. Their identical contents are intentional: location supplies the
period and resolution. Moving such a file changes its interpretation.

`PublishRollup` accepts prepared totals; it does not compute higher totals from
`log.sum`. A period must have ended, and the caller decides when late events have
settled before publishing. Republishing identical content is idempotent; a
changed total for an already published period is rejected. Unlike `log.sum`,
these files cannot append another contribution to adjust an existing period.

Aggregate references remain local to the schema directory. A viewer/exporter
must resolve field and value meanings before combining totals across schema
versions. Groups represent complete trie leaves; a service-level or other
projection may require combining multiple leaves.

## Reconstructing one event

For row 0 of the sandbox index:

```text
log.map                    meanings of fields 3, 4, 8, 12, 14, 15 and 21
     │
log.idx row 0 ── UUID ───── entry.id
     │
     ├─ leaf 3 ─────────── log.tri: 3 → 2 → 1 → root
     │                     service=vault, http_status=401, level=ERROR
     │
     └─ pop [16, 69) ───── log.pop: duration, message, fields
```

A readable projection is:

```json
{
  "id": "01a0d002-8ce1-74be-a349-235d6a6848c3",
  "level": "ERROR",
  "service": "vault",
  "http_status": 401,
  "duration": 2237,
  "message": "unauthorized",
  "fields": {"name": "Synthetic Customer", "sample": 3},
  "time": "2026-09-23T20:43:43.201Z"
}
```

The search renderer derives `time` from the UUID as a UTC RFC3339 string in pretty
and JSONL output. Adding the stored duration (2237 microseconds here) gives the
transaction end, `2026-09-23T20:43:43.203237Z`; no separate end field is generated.
The UUID contains millisecond precision, so this does not restore any submillisecond
start-time precision. The derived `time` is not written to the storage files and
does not alter their binary layouts.

The six demonstrated files total `182 + 55 + 92 + 122 + 66 + 58 = 575` bytes.
The 182-byte map and 55-byte trie are shared with other days, not repeated per day.
This is a two-event layout illustration, not a representative compression ratio.

## Commit, recovery and current boundaries

1. Append new trie nodes, then sync `log.tri`.
2. Append the population batch, then sync `log.pop`.
3. Append its index run, then sync `log.idx`. The run publishes the references.
4. Publish eligible five-minute contributions and sync `log.sum`.

`Flush` and `Close` are durability barriers; `Append` alone is not. Committed
records are append-only, without in-place sorted insertion. Files can be sealed
externally after the allowed lateness window; this manager does not enforce a
sealing policy. Installing `log.map` uses a temporary file, file sync, rename and
directory sync. Higher rollups also sync referenced trie data before publication.

On recovery:

- Reject an incompatible or incomplete file header. For scanned trie, index and
  summary files, truncate an incomplete final block at its start. A complete block
  with an invalid length, checksum or payload is an error, not a truncation target.
  Higher aggregate files use the same scanner when opened through their APIs.
- Require each committed index run to reference one complete, checksummed
  population block, with contiguous ranges and matching length. Missing, truncated
  or corrupt referenced population data fails recovery; the index is not silently
  discarded to conceal it.
- Discard population bytes beyond the last complete index run. An empty index
  leaves only the 8-byte population file header. Unreferenced complete trie nodes
  may remain because the trie spans multiple days; do not truncate it from one
  day's index alone.
- Require summary watermarks to increase within each bucket and not exceed the
  committed index end. Rebuild unfinished accumulators from committed population
  durations, skipping runs already covered by that bucket's watermark.

An I/O failure requires closing and reopening the manager. Recovery and manager
`Scan` callbacks are serialized under the manager. The separate read-only
Rust search path used by `trunk search` captures each selected index's length,
validates its committed runs and resolves map/trie/population data. Rust selects
and merges the matching ranges and writes JSON directly to stdout.
It can run alongside ingestion without taking the writer lock or repairing files.
An incomplete final block is ignored, while a complete corrupt block is an error.
These are per-file observations, not one atomic snapshot of all taps and days;
a newly visible complete index run may not yet have completed its fsync.

The current store accepts an already assigned schema, at most 64 top-level event
fields, valid UUIDv7, and unsigned duration up to seven days in microseconds. The
population format can hold literal CBOR, but ingestion currently rejects unknown
numeric field/value references. Declared enum fields accept literal text fallback;
undeclared semantic-field fallback still needs the future registration design.
Sensitivity rules, PII transforms and 30-day statistical schema planning are not
implemented by this file format. These boundaries should not be mistaken for
completed parts of the broader design.

## Implementation and validation

The layouts are defined by [files.go](files.go), [trie.go](trie.go),
[partition.go](partition.go), [accumulator.go](accumulator.go),
[rollup.go](rollup.go) and [layout.go](layout.go). The CBOR definition is in
[codec/definition.go](../../codec/definition.go) and
[codec/codec.go](../../codec/codec.go).

To inspect the illustrated bytes from the repository root:

```sh
xxd logs/sandbox/42/log.map
xxd logs/sandbox/42/log.tri
xxd logs/sandbox/42/2026/266/log.idx
xxd logs/sandbox/42/2026/266/log.pop
xxd logs/sandbox/42/2026/266/log.sum
xxd logs/sandbox/42/2026/log.365
```

To validate the implementation from `go/`:

```sh
go test -race ./server/...
go vet ./server/...
```

Tests cover cross-day trie sharing, fixed index ranges, population checksums and
orphan cleanup, interrupted index publication, recovery of summary accumulators,
late corrections, replay, tap locks/isolation, limits, leap days, ISO week-year,
immutable rollup publication, and real WebSocket/HTTP3 ingestion.
