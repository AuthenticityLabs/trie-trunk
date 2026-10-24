use pil_codec::{
    AdaptiveIntegers as A, Definition, Field, FrameIndex, Scalar as S, Value as V, decode_exact,
    encode, read_frame_bytes,
};
#[test]
fn golden_exhaustive_signed_unsigned_and_streams() {
    let d = Definition::new(
        "test",
        1,
        vec![
            Field::new("event_id", S::Uuid),
            Field::new("u", S::U16),
            Field::new("i", S::I16),
        ],
    )
    .unwrap()
    .with_adaptive_integers(A::From16);
    assert_eq!(Definition::from_cbor(&d.to_cbor().unwrap()).unwrap(), d);
    let mut index = FrameIndex::default();
    for u in 0..=u16::MAX {
        let values = [V::Absent, V::U16(u), V::I16(u as i16)];
        let raw = encode(&d, &values).unwrap();
        let uc = if u > 255 { 1 } else { 0 };
        let ic = if (-128..=127).contains(&(u as i16)) {
            0
        } else {
            1
        };
        assert_eq!(&raw[..2], &[0x28, (uc << 7) | (ic << 6)]);
        assert_eq!(decode_exact(&d, &raw).unwrap().values(), values);
        assert_eq!(index.scan(&d, &raw, 100).unwrap(), raw.len());
        let twice = [raw.as_slice(), raw.as_slice()].concat();
        let mut stream = twice.as_slice();
        assert_eq!(
            read_frame_bytes(&d, &mut stream, 100).unwrap().unwrap(),
            raw
        );
        assert_eq!(
            read_frame_bytes(&d, &mut stream, 100).unwrap().unwrap(),
            raw
        );
        assert!(read_frame_bytes(&d, &mut stream, 100).unwrap().is_none());
    }
}
#[test]
fn sparse_mixed_selectors_and_strict_fields() {
    for mode in [A::None, A::From16, A::From32] {
        let d = Definition::new(
            "test",
            2,
            vec![
                Field::new("u16", S::U16),
                Field::new("i32", S::I32),
                Field::new("u64", S::U64),
                Field::new("i64", S::I64),
                Field::new("x", S::String),
                Field::new("t", S::TimestampMs),
                Field::new("ref", S::Ref16),
            ],
        )
        .unwrap()
        .with_adaptive_integers(mode);
        for n in [
            0,
            127,
            128,
            255,
            256,
            32767,
            32768,
            i32::MAX as u64,
            u32::MAX as u64,
            u64::MAX,
        ] {
            for sparse in [false, true] {
                let v = [
                    if sparse { V::Null } else { V::U16(n as u16) },
                    V::I32(n as i32),
                    V::U64(n),
                    V::I64(n as i64),
                    V::String("λ"),
                    V::TimestampMs(42),
                    V::Ref16(65535),
                ];
                let raw = encode(&d, &v).unwrap();
                assert_eq!(decode_exact(&d, &raw).unwrap().values(), v);
                let mut idx = FrameIndex::default();
                assert_eq!(idx.scan(&d, &raw, 1000).unwrap(), raw.len());
                for i in 0..raw.len() {
                    assert!(idx.scan(&d, &raw[..i], 1000).is_err())
                }
            }
        }
    }
}
#[test]
fn reject_noncanonical_and_bad_widths() {
    let d = Definition::new(
        "test",
        1,
        vec![Field::new("event_id", S::Uuid), Field::new("u", S::U16)],
    )
    .unwrap()
    .with_adaptive_integers(A::From16);
    assert_eq!(
        encode(&d, &[V::Absent, V::U16(7)]).unwrap(),
        vec![0x20, 0, 7]
    );
    for b in [&[0x20, 1, 7][..], &[0x20, 0x80, 7, 0], &[0x30, 0, 7]] {
        assert!(decode_exact(&d, b).is_err());
        assert!(FrameIndex::default().scan(&d, b, 100).is_err());
    }
}
