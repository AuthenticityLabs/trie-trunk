//! Codec-only, cached-definition comparison. No file/network I/O in timed loops.
//! Both readers produce the same reusable sparse list of borrowed typed values.
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
    Reference(u16),
}
impl Atom {
    fn value(&self, bits: u8) -> Value<'_> {
        match self {
            Self::Scalar(v) => *v,
            Self::Text(v) => Value::String(v),
            Self::Bytes(v) => Value::Bytes(v),
            Self::Reference(v) => reference_value(*v, bits),
        }
    }
}
fn reference_value(value: u16, bits: u8) -> Value<'static> {
    match bits {
        16 => Value::Ref16(value),
        32 => Value::U32(u32::from(value)),
        _ => panic!("reference width must be 16 or 32"),
    }
}
type Record = Vec<(usize, Atom)>;
type Output<'a> = Vec<(usize, Value<'a>)>;

fn definition(count: usize, bits: u8) -> Definition {
    let fields = (0..count)
        .map(|i| {
            let name = format!("field_{i}");
            if i == 2 {
                return Field::mapped(name, ["trace", "debug", "info", "warn", "error"]);
            }
            let kind = match i {
                0 => Scalar::Uuid,
                1 => {
                    if bits == 32 {
                        Scalar::U32
                    } else {
                        "REF16".parse().unwrap()
                    }
                }
                _ => match i % 7 {
                    0 => Scalar::String,
                    1 => Scalar::U64,
                    2 => Scalar::I64,
                    3 => Scalar::Bool,
                    4 => Scalar::Bytes,
                    5 => Scalar::F64,
                    _ => Scalar::U16,
                },
            };
            Field::new(name, kind)
        })
        .collect();
    Definition::new("codec-benchmark", 1, fields).unwrap()
}
fn records(def: &Definition, present: usize, n: usize) -> Vec<Record> {
    (0..n).map(|row| {
        let count = def.fields().len();
        let mut keys = vec![0, 1, 2];
        let mut k = 3 + row % (count - 3);
        while keys.len() < present {
            if !keys.contains(&k) { keys.push(k); }
            k = 3 + (k - 3 + 17) % (count - 3);
        }
        keys.sort_unstable();
        keys.into_iter().map(|key| {
            let f = &def.fields()[key];
            let seed = (row as u64).wrapping_mul(7919).wrapping_add(key as u64 * 104729);
            let a = if key == 1 { Atom::Reference((row % 65536) as u16) }
            else if key > 2 && seed % 19 == 0 { Atom::Scalar(Value::Null) }
            else if !f.values.is_empty() {
                if row % 13 == 0 { Atom::Text("custom-level".to_owned()) }
                else { Atom::Scalar(Value::Mapped(row as u64 % f.values.len() as u64)) }
            } else { match f.kind {
                Type::Scalar(Scalar::Uuid) => {
                    let mut id = [0; 16];
                    id[..6].copy_from_slice(&(1_790_000_000_000 + row as u64).to_be_bytes()[2..]);
                    id[6] = 0x70; id[8] = 0x80;
                    id[9..].copy_from_slice(&seed.to_be_bytes()[1..]);
                    Atom::Scalar(Value::Uuid(id))
                },
                Type::Scalar(Scalar::String) => Atom::Text(format!("Request completed for item {row:010}; field {key:04}; response recorded with trace {seed:016x}")),
                Type::Scalar(Scalar::U64) => Atom::Scalar(Value::U64(seed % 1_000_000)),
                Type::Scalar(Scalar::U16) => Atom::Scalar(Value::U16((seed % 600) as u16)),
                Type::Scalar(Scalar::I64) => Atom::Scalar(Value::I64(seed as i64 % 100_000 - 50_000)),
                Type::Scalar(Scalar::F64) => Atom::Scalar(Value::F64(seed as f64 / 7919.3)),
                Type::Scalar(Scalar::Bool) => Atom::Scalar(Value::Bool(seed & 1 != 0)),
                Type::Scalar(Scalar::Bytes) => Atom::Bytes(seed.to_be_bytes().repeat(2)),
                _ => unreachable!(),
            }};
            (key, a)
        }).collect()
    }).collect()
}
fn pil_encode<'a>(d: &Definition, r: &Record, bits: u8, e: &'a mut FrameEncoder) -> &'a [u8] {
    let mut fields = r.iter().peekable();
    e.encode_with(d, |key| {
        Ok(if fields.peek().is_some_and(|(k, _)| *k == key) {
            fields.next().unwrap().1.value(bits)
        } else {
            Value::Absent
        })
    })
    .unwrap()
}
fn head(h: Header, out: &mut Vec<u8>) {
    Encoder::from(out).push(h).unwrap();
}
fn cbor_encode(r: &Record, bits: u8, out: &mut Vec<u8>) {
    out.clear();
    head(Header::Map(Some(r.len())), out);
    for (key, atom) in r {
        head(Header::Positive(*key as u64), out);
        match atom.value(bits) {
            Value::Null => head(Header::Simple(22), out),
            Value::Mapped(v) | Value::U64(v) => head(Header::Positive(v), out),
            Value::U16(v) | Value::Ref16(v) => head(Header::Positive(v as u64), out),
            Value::U32(v) => head(Header::Positive(v as u64), out),
            Value::I64(v) if v >= 0 => head(Header::Positive(v as u64), out),
            Value::I64(v) => head(Header::Negative(!v as u64), out),
            Value::F64(v) => head(Header::Float(v), out),
            Value::Bool(v) => head(Header::Simple(20 + u8::from(v)), out),
            Value::Uuid(v) => {
                head(Header::Bytes(Some(16)), out);
                out.extend_from_slice(&v);
            }
            Value::String(v) => {
                head(Header::Text(Some(v.len())), out);
                out.extend_from_slice(v.as_bytes());
            }
            Value::Bytes(v) => {
                head(Header::Bytes(Some(v.len())), out);
                out.extend_from_slice(v);
            }
            _ => unreachable!(),
        }
    }
}
fn take<'a>(input: &mut &'a [u8], n: usize) -> &'a [u8] {
    let (a, b) = input.split_at_checked(n).expect("truncated value");
    *input = b;
    a
}
fn pull(input: &mut &[u8]) -> Header {
    Decoder::from(input).pull().expect("invalid CBOR header")
}
fn cbor_decode<'a>(
    d: &Definition,
    mut input: &'a [u8],
    seen: &mut [u64],
    epoch: u64,
    out: &mut Output<'a>,
) {
    out.clear();
    let Header::Map(Some(n)) = pull(&mut input) else {
        panic!("expected definite map")
    };
    assert!(n <= d.fields().len());
    for _ in 0..n {
        let Header::Positive(k) = pull(&mut input) else {
            panic!("expected numeric field")
        };
        let key = usize::try_from(k).unwrap();
        let f = d.fields().get(key).expect("unknown field");
        assert_ne!(seen[key], epoch, "duplicate field");
        seen[key] = epoch;
        let h = pull(&mut input);
        let v = if h == Header::Simple(22) {
            Value::Null
        } else {
            match (f.kind, h) {
                (Type::Scalar(Scalar::String), Header::Positive(n)) if !f.values.is_empty() => {
                    assert!(n < f.values.len() as u64);
                    Value::Mapped(n)
                }
                (Type::Scalar(Scalar::String), Header::Text(Some(n))) => {
                    Value::String(std::str::from_utf8(take(&mut input, n)).unwrap())
                }
                (Type::Scalar(Scalar::Bytes), Header::Bytes(Some(n))) => {
                    Value::Bytes(take(&mut input, n))
                }
                (Type::Scalar(Scalar::Uuid), Header::Bytes(Some(16))) => {
                    Value::Uuid(take(&mut input, 16).try_into().unwrap())
                }
                (Type::Scalar(Scalar::U16), Header::Positive(n)) => {
                    Value::U16(n.try_into().unwrap())
                }
                (Type::Scalar(Scalar::Ref16), Header::Positive(n)) => {
                    Value::Ref16(n.try_into().unwrap())
                }
                (Type::Scalar(Scalar::U32), Header::Positive(n)) => {
                    Value::U32(n.try_into().unwrap())
                }
                (Type::Scalar(Scalar::U64), Header::Positive(n)) => Value::U64(n),
                (Type::Scalar(Scalar::I64), Header::Positive(n)) => {
                    Value::I64(n.try_into().unwrap())
                }
                (Type::Scalar(Scalar::I64), Header::Negative(n)) => {
                    Value::I64(!i64::try_from(n).unwrap())
                }
                (Type::Scalar(Scalar::F64), Header::Float(n)) => Value::F64(n),
                (Type::Scalar(Scalar::Bool), Header::Simple(n @ 20..=21)) => Value::Bool(n == 21),
                _ => panic!("CBOR value does not match definition"),
            }
        };
        out.push((key, v));
    }
    assert!(input.is_empty(), "trailing data");
}
fn pil_decode<'a>(d: &Definition, input: &'a [u8], index: &mut FrameIndex, out: &mut Output<'a>) {
    assert_eq!(index.scan(d, input, 64 * 1024 * 1024).unwrap(), input.len());
    out.clear();
    for (key, &[a, b]) in index.spans().iter().enumerate() {
        if a == ABSENT {
            continue;
        }
        let v = if a == NULL {
            Value::Null
        } else {
            let f = &d.fields()[key];
            let bytes = &input[a as usize..b as usize];
            if !f.values.is_empty() && (input[key / 4] >> (6 - 2 * (key % 4))) & 3 == 2 {
                let mut n = [0; 8];
                n[..bytes.len()].copy_from_slice(bytes);
                Value::Mapped(u64::from_le_bytes(n))
            } else {
                match f.kind {
                    Type::Scalar(Scalar::String) => {
                        Value::String(std::str::from_utf8(bytes).unwrap())
                    }
                    Type::Scalar(Scalar::Bytes) => Value::Bytes(bytes),
                    Type::Scalar(Scalar::Uuid) => Value::Uuid(bytes.try_into().unwrap()),
                    Type::Scalar(Scalar::U16) => {
                        Value::U16(u16::from_le_bytes(bytes.try_into().unwrap()))
                    }
                    Type::Scalar(Scalar::Ref16) => {
                        Value::Ref16(u16::from_le_bytes(bytes.try_into().unwrap()))
                    }
                    Type::Scalar(Scalar::U32) => {
                        Value::U32(u32::from_le_bytes(bytes.try_into().unwrap()))
                    }
                    Type::Scalar(Scalar::U64) => {
                        Value::U64(u64::from_le_bytes(bytes.try_into().unwrap()))
                    }
                    Type::Scalar(Scalar::I64) => {
                        Value::I64(i64::from_le_bytes(bytes.try_into().unwrap()))
                    }
                    Type::Scalar(Scalar::F64) => {
                        Value::F64(f64::from_le_bytes(bytes.try_into().unwrap()))
                    }
                    Type::Scalar(Scalar::Bool) => Value::Bool(bytes[0] != 0),
                    _ => unreachable!(),
                }
            }
        };
        out.push((key, v));
    }
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
        self.ends.push(self.bytes.len());
    }
    fn frame(&self, i: usize) -> &[u8] {
        &self.bytes[self.ends[i]..self.ends[i + 1]]
    }
}
fn measure(mut run: impl FnMut(usize), count: usize) -> Vec<f64> {
    run(1); // capacities and instruction/data paths warm before all samples
    let start = Instant::now();
    run(1);
    let repeats = ((Duration::from_millis(50).as_nanos() / start.elapsed().as_nanos().max(1))
        as usize)
        .clamp(1, 500);
    (0..7)
        .map(|_| {
            let start = Instant::now();
            run(repeats);
            start.elapsed().as_nanos() as f64 / repeats as f64 / count as f64
        })
        .collect()
}
fn report(
    shape: &str,
    bits: u8,
    count: usize,
    operation: &str,
    codec: &str,
    bytes: usize,
    mut times: Vec<f64>,
) {
    let samples = times
        .iter()
        .map(|v| format!("{v:.3}"))
        .collect::<Vec<_>>()
        .join(";");
    times.sort_by(f64::total_cmp);
    println!(
        "{shape},{bits},{count},{operation},{codec},{bytes},{:.3},{:.3},{:.3},{samples}",
        times[3], times[0], times[6]
    );
}
fn main() {
    let args: Vec<_> = std::env::args().collect();
    let flag = |name: &str| {
        args.iter()
            .position(|v| v == name)
            .map(|i| args[i + 1].as_str())
    };
    let bits = flag("--ref-bits").unwrap_or("32").parse::<u8>().unwrap();
    let maximum = flag("--max").unwrap_or("1000000").parse::<usize>().unwrap();
    let only = flag("--shape");
    println!(
        "shape,reference_bits,events,operation,codec,encoded_bytes,median_ns_per_event,min_ns,max_ns,samples_ns"
    );
    for (shape, width, present) in [
        ("dense16", 16, 16),
        ("partial64", 64, 8),
        ("sparse512", 512, 8),
    ] {
        if only.is_some_and(|v| v != shape) {
            continue;
        }
        eprintln!(
            "Preparing {shape}: {maximum} events, {bits}-bit reference; definitions outside timings"
        );
        let d = definition(width, bits);
        let records = records(&d, present, maximum);
        let (mut pil, mut cbor) = (Encoded::new(), Encoded::new());
        let mut pe = FrameEncoder::default();
        let mut ce = Vec::new();
        let mut index = FrameIndex::default();
        let mut seen = vec![0; width];
        let (mut p, mut c) = (Output::new(), Output::new());
        for r in &records {
            pil.push(pil_encode(&d, r, bits, &mut pe));
            cbor_encode(r, bits, &mut ce);
            cbor.push(&ce);
        }
        for (i, r) in records.iter().enumerate() {
            if i < 1024 || i % 10007 == 0 || i + 1 == maximum {
                pil_decode(&d, pil.frame(i), &mut index, &mut p);
                cbor_decode(&d, cbor.frame(i), &mut seen, i as u64 + 1, &mut c);
                assert_eq!(p, c);
                let expected: Output<'_> = r.iter().map(|(k, v)| (*k, v.value(bits))).collect();
                assert_eq!(p, expected);
                let frame = pil_codec::decode_exact(&d, pil.frame(i)).unwrap();
                for &(key, value) in &p {
                    assert_eq!(frame.values()[key], value);
                }
                let value: ciborium::Value = ciborium::from_reader(cbor.frame(i)).unwrap();
                assert_eq!(value.as_map().unwrap().len(), r.len());
            }
        }
        let mut sizes: Vec<_> = [1000, 10000, 50000, 100000, 1000000]
            .into_iter()
            .filter(|n| *n <= maximum)
            .collect();
        if !sizes.contains(&maximum) {
            sizes.push(maximum);
        }
        for count in sizes {
            eprintln!("Measuring {shape}, {count} events");
            let times = measure(
                |repeats| {
                    for _ in 0..repeats {
                        for r in &records[..count] {
                            black_box(pil_encode(black_box(&d), black_box(r), bits, &mut pe));
                        }
                    }
                },
                count,
            );
            report(shape, bits, count, "encode", "PIL", pil.ends[count], times);
            let times = measure(
                |repeats| {
                    for _ in 0..repeats {
                        for r in &records[..count] {
                            cbor_encode(black_box(r), bits, &mut ce);
                            black_box(ce.as_slice());
                        }
                    }
                },
                count,
            );
            report(
                shape,
                bits,
                count,
                "encode",
                "CBOR",
                cbor.ends[count],
                times,
            );
            let mut po = Output::with_capacity(present);
            let times = measure(
                |repeats| {
                    for _ in 0..repeats {
                        for i in 0..count {
                            pil_decode(black_box(&d), black_box(pil.frame(i)), &mut index, &mut po);
                            black_box(po.as_slice());
                        }
                    }
                },
                count,
            );
            report(shape, bits, count, "decode", "PIL", pil.ends[count], times);
            let mut co = Output::with_capacity(present);
            seen.fill(0);
            let mut epoch = 0;
            let times = measure(
                |repeats| {
                    for _ in 0..repeats {
                        for i in 0..count {
                            epoch += 1;
                            cbor_decode(
                                black_box(&d),
                                black_box(cbor.frame(i)),
                                &mut seen,
                                epoch,
                                &mut co,
                            );
                            black_box(co.as_slice());
                        }
                    }
                },
                count,
            );
            report(
                shape,
                bits,
                count,
                "decode",
                "CBOR",
                cbor.ends[count],
                times,
            );
        }
    }
}
