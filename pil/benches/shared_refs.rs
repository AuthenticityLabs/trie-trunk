//! Expanded, individually dictionary-coded fields versus one cached REF16 group.
//! The baseline contains NO reference field. All timings exclude setup and I/O.
use ciborium_ll::{Decoder, Encoder, Header};
use pil_codec::{ABSENT, Definition, Field, FrameEncoder, FrameIndex, NULL, Scalar, Type, Value};
use std::{
    hint::black_box,
    time::{Duration, Instant},
};

enum Atom {
    Scalar(Value<'static>),
    Text(String),
    Bytes(Vec<u8>),
}
impl Atom {
    fn value(&self) -> Value<'_> {
        match self {
            Self::Scalar(v) => *v,
            Self::Text(v) => Value::String(v),
            Self::Bytes(v) => Value::Bytes(v),
        }
    }
}
struct Record {
    leaf: u16,
    dynamic: Vec<(usize, Atom)>,
}
type Output<'a> = Vec<(usize, Value<'a>)>;
struct Node {
    parent: u16,
    key: usize,
    code: u64,
}
struct Trie {
    nodes: Vec<Node>,
    paths: Vec<Vec<(usize, Value<'static>)>>,
    leaves: Vec<u16>,
    values: Vec<Vec<Value<'static>>>,
}
impl Trie {
    fn new(depth: usize) -> Self {
        let mut t = Self {
            nodes: vec![Node {
                parent: 0,
                key: 0,
                code: 0,
            }],
            paths: vec![vec![]],
            leaves: vec![0],
            values: vec![],
        };
        for key in 1..=depth {
            let mut next = Vec::new();
            for &parent in &t.leaves {
                for code in 0..2 {
                    let id = u16::try_from(t.nodes.len()).unwrap();
                    t.nodes.push(Node { parent, key, code });
                    let mut path = t.paths[parent as usize].clone();
                    path.push((key, Value::Mapped(code)));
                    t.paths.push(path);
                    next.push(id);
                }
            }
            t.leaves = next;
        }
        // Partial objects may skip any earlier field, not merely stop at a prefix.
        for key in [depth - 1, depth] {
            let parent = u16::try_from(t.nodes.len()).unwrap();
            t.nodes.push(Node {
                parent: 0,
                key,
                code: 1,
            });
            t.paths.push(vec![(key, Value::Mapped(1))]);
            if key < depth {
                t.nodes.push(Node {
                    parent,
                    key: depth,
                    code: 0,
                });
                t.paths
                    .push(vec![(key, Value::Mapped(1)), (depth, Value::Mapped(0))]);
            }
        }
        t.values = t
            .paths
            .iter()
            .map(|path| {
                let mut values = vec![Value::Absent; depth];
                for &(key, value) in path {
                    values[key - 1] = value
                }
                values
            })
            .collect();
        t
    }
    fn path(&self, leaf: u16) -> &[(usize, Value<'static>)] {
        self.paths
            .get(usize::from(leaf))
            .expect("unknown shared reference")
    }
    fn check(&self) {
        for (id, expected) in self.paths.iter().enumerate() {
            let mut path = Vec::new();
            let mut at = id;
            while at != 0 {
                let n = &self.nodes[at];
                path.push((n.key, Value::Mapped(n.code)));
                at = usize::from(n.parent);
            }
            path.reverse();
            assert_eq!(&path, expected);
        }
    }
}
struct Layout {
    full: Definition,
    reference: Definition,
    shared: usize,
}
impl Layout {
    fn new(count: usize, shared: usize) -> Self {
        let fields: Vec<_> = (0..count)
            .map(|key| {
                if (1..=shared).contains(&key) {
                    return Field::mapped(format!("shared_{key}"), ["value_a", "value_b"]);
                }
                let kind = if key == 0 {
                    Scalar::Uuid
                } else {
                    match key % 7 {
                        0 => Scalar::String,
                        1 => Scalar::U64,
                        2 => Scalar::I64,
                        3 => Scalar::Bool,
                        4 => Scalar::Bytes,
                        5 => Scalar::F64,
                        _ => Scalar::U16,
                    }
                };
                Field::new(format!("field_{key}"), kind)
            })
            .collect();
        let mut compact = vec![fields[0].clone(), Field::new("shared", Scalar::Ref16)];
        compact.extend_from_slice(&fields[shared + 1..]);
        Self {
            full: Definition::new("expanded-no-ref", 1, fields).unwrap(),
            reference: Definition::new("shared-ref16", 1, compact).unwrap(),
            shared,
        }
    }
    fn definition(&self, refs: bool) -> &Definition {
        if refs { &self.reference } else { &self.full }
    }
    fn logical(&self, wire: usize, refs: bool) -> usize {
        if refs && wire > 1 {
            wire + self.shared - 1
        } else {
            wire
        }
    }
    fn wire(&self, logical: usize, refs: bool) -> usize {
        if refs && logical > self.shared {
            logical - self.shared + 1
        } else {
            logical
        }
    }
}
fn records(
    layout: &Layout,
    trie: &Trie,
    present_shared: usize,
    present_dynamic: usize,
    full: bool,
    count: usize,
) -> Vec<Record> {
    let leaves: Vec<_> = trie
        .paths
        .iter()
        .enumerate()
        .filter(|(_, p)| p.len() == present_shared)
        .map(|(i, _)| i as u16)
        .collect();
    (0..count).map(|row| {
        let first=layout.shared+1; let choices=layout.full.fields().len()-first;
        let mut keys=if full {vec![0]} else {vec![]}; let mut k=first+row%choices;
        while keys.len()<present_dynamic {
            if !keys.contains(&k) { keys.push(k); }
            k=first+(k-first+1)%choices;
        }
        keys.sort_unstable();
        let dynamic=keys.into_iter().map(|key| {
            let seed=(row as u64)*7919+(key as u64)*104729;
            let value=if key>0 && seed.is_multiple_of(19) { Atom::Scalar(Value::Null) } else { match layout.full.fields()[key].kind {
                Type::Scalar(Scalar::Uuid)=> { let mut id=[0;16];id[..6].copy_from_slice(&(1_790_000_000_000+row as u64).to_be_bytes()[2..]);id[6]=0x70;id[8]=0x80;id[9..].copy_from_slice(&seed.to_be_bytes()[1..]);Atom::Scalar(Value::Uuid(id)) },
                Type::Scalar(Scalar::String)=>Atom::Text(format!("Request completed for item {row:010}; field {key:04}; response recorded with trace {seed:016x}")),
                Type::Scalar(Scalar::Bytes)=>Atom::Bytes(seed.to_be_bytes().repeat(2)),
                Type::Scalar(Scalar::U64)=>Atom::Scalar(Value::U64(seed%1_000_000)),
                Type::Scalar(Scalar::U16)=>Atom::Scalar(Value::U16((seed%600) as u16)),
                Type::Scalar(Scalar::I64)=>Atom::Scalar(Value::I64(seed as i64%100_000-50_000)),
                Type::Scalar(Scalar::F64)=>Atom::Scalar(Value::F64(seed as f64/7919.3)),
                Type::Scalar(Scalar::Bool)=>Atom::Scalar(Value::Bool(seed&1!=0)),
                _=>unreachable!(),
            }};
            (key,value)
        }).collect();
        Record { leaf:leaves[row%leaves.len()], dynamic }
    }).collect()
}
fn pil_encode<'a>(
    layout: &Layout,
    trie: &Trie,
    row: &Record,
    refs: bool,
    encoder: &'a mut FrameEncoder,
) -> &'a [u8] {
    let values = trie
        .values
        .get(usize::from(row.leaf))
        .expect("unknown shared reference"); // cached group, not a timed trie walk
    let mut dynamic = row.dynamic.iter().peekable();
    encoder
        .encode_with(layout.definition(refs), |wire| {
            let key = layout.logical(wire, refs);
            Ok(if refs && wire == 1 {
                if row.leaf == 0 {
                    Value::Absent
                } else {
                    Value::Ref16(row.leaf)
                }
            } else if !refs && (1..=layout.shared).contains(&key) {
                values[key - 1]
            } else if dynamic.peek().is_some_and(|(k, _)| *k == key) {
                dynamic.next().unwrap().1.value()
            } else {
                Value::Absent
            })
        })
        .unwrap()
}
fn head(h: Header, out: &mut Vec<u8>) {
    Encoder::from(out).push(h).unwrap();
}
fn cbor_value(value: Value<'_>, out: &mut Vec<u8>) {
    match value {
        Value::Null => head(Header::Simple(22), out),
        Value::Mapped(v) | Value::U64(v) => head(Header::Positive(v), out),
        Value::U16(v) | Value::Ref16(v) => head(Header::Positive(u64::from(v)), out),
        Value::I64(v) if v >= 0 => head(Header::Positive(v as u64), out),
        Value::I64(v) => head(Header::Negative(!v as u64), out),
        Value::F64(v) => head(Header::Float(v), out),
        Value::Bool(v) => head(Header::Simple(20 + u8::from(v)), out),
        Value::Uuid(v) => {
            head(Header::Bytes(Some(16)), out);
            out.extend_from_slice(&v)
        }
        Value::Bytes(v) => {
            head(Header::Bytes(Some(v.len())), out);
            out.extend_from_slice(v)
        }
        Value::String(v) => {
            head(Header::Text(Some(v.len())), out);
            out.extend_from_slice(v.as_bytes())
        }
        _ => unreachable!(),
    }
}
fn cbor_pair(key: usize, v: Value<'_>, out: &mut Vec<u8>) {
    head(Header::Positive(key as u64), out);
    cbor_value(v, out)
}
fn cbor_encode(layout: &Layout, trie: &Trie, row: &Record, refs: bool, out: &mut Vec<u8>) {
    let path = trie.path(row.leaf);
    out.clear();
    head(
        Header::Map(Some(
            row.dynamic.len()
                + if refs {
                    usize::from(row.leaf != 0)
                } else {
                    path.len()
                },
        )),
        out,
    );
    let mut dynamic = row.dynamic.iter().peekable();
    if dynamic.peek().is_some_and(|(key, _)| *key == 0) {
        cbor_pair(0, dynamic.next().unwrap().1.value(), out)
    }
    if refs {
        if row.leaf != 0 {
            cbor_pair(1, Value::Ref16(row.leaf), out)
        }
    } else {
        for &(key, value) in path {
            cbor_pair(key, value, out)
        }
    }
    for (key, value) in dynamic {
        cbor_pair(layout.wire(*key, refs), value.value(), out)
    }
}
fn take<'a>(b: &mut &'a [u8], n: usize) -> &'a [u8] {
    let (a, rest) = b.split_at_checked(n).expect("truncated CBOR");
    *b = rest;
    a
}
fn pull(b: &mut &[u8]) -> Header {
    Decoder::from(b).pull().expect("invalid CBOR header")
}
fn cbor_read<'a>(field: &Field, b: &mut &'a [u8]) -> Value<'a> {
    let h = pull(b);
    if h == Header::Simple(22) {
        return Value::Null;
    }
    match (field.kind, h) {
        (Type::Scalar(Scalar::String), Header::Positive(n)) if !field.values.is_empty() => {
            assert!(n < field.values.len() as u64);
            Value::Mapped(n)
        }
        (Type::Scalar(Scalar::String), Header::Text(Some(n))) => {
            Value::String(std::str::from_utf8(take(b, n)).unwrap())
        }
        (Type::Scalar(Scalar::Bytes), Header::Bytes(Some(n))) => Value::Bytes(take(b, n)),
        (Type::Scalar(Scalar::Uuid), Header::Bytes(Some(16))) => {
            Value::Uuid(take(b, 16).try_into().unwrap())
        }
        (Type::Scalar(Scalar::U64), Header::Positive(n)) => Value::U64(n),
        (Type::Scalar(Scalar::U16), Header::Positive(n)) => Value::U16(n.try_into().unwrap()),
        (Type::Scalar(Scalar::Ref16), Header::Positive(n)) => Value::Ref16(n.try_into().unwrap()),
        (Type::Scalar(Scalar::I64), Header::Positive(n)) => Value::I64(n.try_into().unwrap()),
        (Type::Scalar(Scalar::I64), Header::Negative(n)) => Value::I64(!i64::try_from(n).unwrap()),
        (Type::Scalar(Scalar::F64), Header::Float(n)) => Value::F64(n),
        (Type::Scalar(Scalar::Bool), Header::Simple(n @ 20..=21)) => Value::Bool(n == 21),
        _ => panic!("CBOR type does not match definition"),
    }
}
#[allow(clippy::too_many_arguments)]
fn cbor_decode<'a>(
    layout: &Layout,
    trie: &Trie,
    mut b: &'a [u8],
    refs: bool,
    expand: bool,
    seen: &mut [u64],
    epoch: u64,
    out: &mut Output<'a>,
) -> Option<u16> {
    let d = layout.definition(refs);
    out.clear();
    let mut reference = None;
    let Header::Map(Some(n)) = pull(&mut b) else {
        panic!("expected map")
    };
    assert!(n <= d.fields().len());
    for _ in 0..n {
        let Header::Positive(k) = pull(&mut b) else {
            panic!("expected field key")
        };
        let k = usize::try_from(k).unwrap();
        let f = d.fields().get(k).expect("unknown field");
        assert_ne!(seen[k], epoch, "duplicate field");
        seen[k] = epoch;
        let value = cbor_read(f, &mut b);
        if refs && k == 1 {
            let Value::Ref16(id) = value else {
                panic!("invalid shared reference")
            };
            let path = trie.path(id);
            reference = Some(id);
            if expand {
                out.extend_from_slice(path)
            } else {
                out.push((k, value))
            }
        } else {
            out.push((if expand { layout.logical(k, refs) } else { k }, value))
        }
    }
    assert!(b.is_empty());
    reference
}
fn pil_read<'a>(definition: &Definition, b: &'a [u8], key: usize, span: [u32; 2]) -> Value<'a> {
    let [start, end] = span;
    if start == ABSENT {
        return Value::Absent;
    }
    if start == NULL {
        return Value::Null;
    }
    let f = &definition.fields()[key];
    let bytes = &b[start as usize..end as usize];
    if !f.values.is_empty() && (b[key / 4] >> (6 - 2 * (key % 4))) & 3 == 2 {
        let mut n = [0; 8];
        n[..bytes.len()].copy_from_slice(bytes);
        return Value::Mapped(u64::from_le_bytes(n));
    }
    match f.kind {
        Type::Scalar(Scalar::String) => Value::String(std::str::from_utf8(bytes).unwrap()),
        Type::Scalar(Scalar::Bytes) => Value::Bytes(bytes),
        Type::Scalar(Scalar::Uuid) => Value::Uuid(bytes.try_into().unwrap()),
        Type::Scalar(Scalar::Ref16) => Value::Ref16(u16::from_le_bytes(bytes.try_into().unwrap())),
        Type::Scalar(Scalar::U16) => Value::U16(u16::from_le_bytes(bytes.try_into().unwrap())),
        Type::Scalar(Scalar::U64) => Value::U64(u64::from_le_bytes(bytes.try_into().unwrap())),
        Type::Scalar(Scalar::I64) => Value::I64(i64::from_le_bytes(bytes.try_into().unwrap())),
        Type::Scalar(Scalar::F64) => Value::F64(f64::from_le_bytes(bytes.try_into().unwrap())),
        Type::Scalar(Scalar::Bool) => Value::Bool(bytes[0] != 0),
        _ => unreachable!(),
    }
}
fn pil_decode<'a>(
    layout: &Layout,
    trie: &Trie,
    b: &'a [u8],
    refs: bool,
    expand: bool,
    index: &mut FrameIndex,
    out: &mut Output<'a>,
) -> Option<u16> {
    let d = layout.definition(refs);
    assert_eq!(index.scan(d, b, 64 * 1024 * 1024).unwrap(), b.len());
    let reference = if refs {
        match pil_read(d, b, 1, index.spans()[1]) {
            Value::Absent => None,
            Value::Ref16(id) => {
                black_box(trie.path(id));
                Some(id)
            }
            _ => panic!("invalid shared reference"),
        }
    } else {
        None
    };
    out.clear();
    if !expand {
        return reference;
    }
    for (key, &span) in index.spans().iter().enumerate() {
        if span[0] == ABSENT {
            continue;
        }
        if refs && key == 1 {
            out.extend_from_slice(trie.path(reference.unwrap()));
            continue;
        }
        out.push((layout.logical(key, refs), pil_read(d, b, key, span)));
    }
    reference
}
struct Encoded {
    bytes: Vec<u8>,
    ends: Vec<usize>,
}
impl Encoded {
    fn new() -> Self {
        Self {
            bytes: Vec::new(),
            ends: vec![0],
        }
    }
    fn push(&mut self, b: &[u8]) {
        self.bytes.extend_from_slice(b);
        self.ends.push(self.bytes.len())
    }
    fn frame(&self, i: usize) -> &[u8] {
        &self.bytes[self.ends[i]..self.ends[i + 1]]
    }
}
fn measure(mut run: impl FnMut(usize), n: usize) -> Vec<f64> {
    run(1);
    let start = Instant::now();
    run(1);
    let repeats = (Duration::from_millis(40).as_nanos() / start.elapsed().as_nanos().max(1))
        .clamp(1, 500) as usize;
    (0..7)
        .map(|_| {
            let start = Instant::now();
            run(repeats);
            start.elapsed().as_nanos() as f64 / repeats as f64 / n as f64
        })
        .collect()
}
fn report(
    shape: &str,
    n: usize,
    refs: bool,
    codec: &str,
    operation: &str,
    bytes: usize,
    mut ns: Vec<f64>,
) {
    let samples = ns
        .iter()
        .map(|x| format!("{x:.3}"))
        .collect::<Vec<_>>()
        .join(";");
    ns.sort_by(f64::total_cmp);
    println!(
        "{shape},{},{n},{codec},{operation},{bytes},{:.3},{:.3},{:.3},{samples}",
        if refs { "REF16" } else { "no_REF" },
        ns[3],
        ns[0],
        ns[6]
    );
}
fn verify(layout: &Layout, trie: &Trie, row: &Record, p: &[u8], c: &[u8], refs: bool) {
    let mut expected: Output = row.dynamic.iter().map(|(k, v)| (*k, v.value())).collect();
    expected.extend_from_slice(trie.path(row.leaf));
    expected.sort_unstable_by_key(|(key, _)| *key);
    let mut index = FrameIndex::default();
    let (mut po, mut co) = (vec![], vec![]);
    let mut seen = vec![0; layout.full.fields().len()];
    pil_decode(layout, trie, p, refs, true, &mut index, &mut po);
    cbor_decode(layout, trie, c, refs, true, &mut seen, 1, &mut co);
    assert_eq!(po, expected);
    assert_eq!(co, expected);
    let decoded = pil_codec::decode_exact(layout.definition(refs), p).unwrap();
    if refs {
        assert_eq!(
            decoded.values()[1],
            if row.leaf == 0 {
                Value::Absent
            } else {
                Value::Ref16(row.leaf)
            }
        );
    }
    let generic: ciborium::Value = ciborium::from_reader(c).unwrap();
    assert_eq!(
        generic.as_map().unwrap().len(),
        row.dynamic.len()
            + if refs {
                usize::from(row.leaf != 0)
            } else {
                trie.path(row.leaf).len()
            }
    );
    pil_decode(layout, trie, p, refs, false, &mut index, &mut po);
    for &(key, value) in &expected {
        let observed = if refs && (1..=layout.shared).contains(&key) {
            trie.path(row.leaf)
                .iter()
                .find(|(k, _)| *k == key)
                .map_or(Value::Absent, |(_, v)| *v)
        } else {
            let wire = layout.wire(key, refs);
            pil_read(layout.definition(refs), p, wire, index.spans()[wire])
        };
        assert_eq!(observed, value);
    }
}
fn main() {
    let args: Vec<_> = std::env::args().collect();
    let arg = |s: &str| {
        args.iter()
            .position(|x| x == s)
            .map(|i| args[i + 1].as_str())
    };
    let maximum = arg("--max").unwrap_or("1000000").parse::<usize>().unwrap();
    let reverse = args.iter().any(|x| x == "--reverse");
    println!(
        "shape,representation,events,codec,operation,encoded_bytes,median_ns,min_ns,max_ns,samples_ns"
    );
    for (shape, count, shared, present_shared, present, full) in [
        ("object8_full", 8, 4, 4, 4, true),
        ("object16_full", 16, 8, 8, 8, true),
        ("object8_change1_dynamic", 8, 4, 0, 1, false),
        ("object8_change2_dynamic", 8, 4, 0, 2, false),
        ("object16_change1_dynamic", 16, 8, 0, 1, false),
        ("object16_change2_dynamic", 16, 8, 0, 2, false),
        ("object8_change1_shared", 8, 4, 1, 0, false),
        ("object8_change2_shared", 8, 4, 2, 0, false),
        ("object16_change1_shared", 16, 8, 1, 0, false),
        ("object16_change2_shared", 16, 8, 2, 0, false),
    ] {
        if arg("--shape").is_some_and(|x| x != shape) {
            continue;
        }
        let layout = Layout::new(count, shared);
        let trie = Trie::new(shared);
        trie.check();
        let records = records(&layout, &trie, present_shared, present, full, maximum);
        eprintln!(
            "{shape}: {maximum} events, {present_shared}/{shared} shared fields, {present} supplied dynamic fields, {} trie nodes; definitions and paths cached",
            trie.nodes.len()
        );
        let mut sizes: Vec<_> = [1000, 10000, 50000, 100000, 1000000]
            .into_iter()
            .filter(|n| *n <= maximum)
            .collect();
        if !sizes.contains(&maximum) {
            sizes.push(maximum)
        }
        for refs in if reverse {
            [true, false]
        } else {
            [false, true]
        } {
            let (mut pil, mut cbor) = (Encoded::new(), Encoded::new());
            let mut pe = FrameEncoder::default();
            let mut ce = vec![];
            for (i, row) in records.iter().enumerate() {
                let p = pil_encode(&layout, &trie, row, refs, &mut pe);
                cbor_encode(&layout, &trie, row, refs, &mut ce);
                if i < 1024 || i.is_multiple_of(10007) || i + 1 == maximum {
                    verify(&layout, &trie, row, p, &ce, refs)
                }
                pil.push(p);
                cbor.push(&ce);
            }
            // Root and internal-prefix references preserve shared-field absence.
            for leaf in 0..u16::try_from(trie.nodes.len()).unwrap() {
                let row = Record {
                    leaf,
                    dynamic: vec![
                        (0, Atom::Scalar(Value::Uuid([7; 16]))),
                        (count - 1, Atom::Scalar(Value::Null)),
                    ],
                };
                let p = pil_encode(&layout, &trie, &row, refs, &mut pe);
                cbor_encode(&layout, &trie, &row, refs, &mut ce);
                verify(&layout, &trie, &row, p, &ce, refs);
            }
            for &n in &sizes {
                eprintln!("{shape} {} {n}", if refs { "REF16" } else { "no_REF" });
                for codec in if reverse {
                    ["CBOR", "PIL"]
                } else {
                    ["PIL", "CBOR"]
                } {
                    let b = if codec == "PIL" { &pil } else { &cbor };
                    let times = measure(
                        |repeats| {
                            for _ in 0..repeats {
                                for row in &records[..n] {
                                    if codec == "PIL" {
                                        black_box(pil_encode(
                                            black_box(&layout),
                                            black_box(&trie),
                                            black_box(row),
                                            refs,
                                            &mut pe,
                                        ));
                                    } else {
                                        cbor_encode(
                                            black_box(&layout),
                                            black_box(&trie),
                                            black_box(row),
                                            refs,
                                            &mut ce,
                                        );
                                        black_box(ce.as_slice());
                                    }
                                }
                            }
                        },
                        n,
                    );
                    report(shape, n, refs, codec, "encode", b.ends[n], times);
                    for expand in [false, true] {
                        let mut index = FrameIndex::default();
                        let mut output = Output::with_capacity(count);
                        let mut seen = vec![0; count];
                        let mut epoch = 0;
                        let times = measure(
                            |repeats| {
                                for _ in 0..repeats {
                                    for i in 0..n {
                                        if codec == "PIL" {
                                            black_box(pil_decode(
                                                black_box(&layout),
                                                black_box(&trie),
                                                black_box(b.frame(i)),
                                                refs,
                                                expand,
                                                &mut index,
                                                &mut output,
                                            ));
                                            black_box(index.spans());
                                        } else {
                                            epoch += 1;
                                            black_box(cbor_decode(
                                                black_box(&layout),
                                                black_box(&trie),
                                                black_box(b.frame(i)),
                                                refs,
                                                expand,
                                                &mut seen,
                                                epoch,
                                                &mut output,
                                            ));
                                        }
                                        black_box(output.as_slice());
                                    }
                                }
                            },
                            n,
                        );
                        report(
                            shape,
                            n,
                            refs,
                            codec,
                            if expand {
                                "decode_expanded"
                            } else {
                                "decode_view"
                            },
                            b.ends[n],
                            times,
                        );
                    }
                }
            }
        }
    }
}
