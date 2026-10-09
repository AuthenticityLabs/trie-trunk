use pil_codec::{
    ABSENT, Array, Definition, Error, Field, FrameIndex, NULL, Scalar, Type, Value, decode_exact,
    encode,
};
#[test]
fn borrowed_index_matches_codec_and_rejects_every_truncation() {
    let d = Definition::new(
        "index",
        1,
        vec![
            Field::new("missing", Scalar::String),
            Field::new("null", Scalar::U16),
            Field::mapped("service", ["vault", "billing"]),
            Field::new("id", Scalar::Uuid),
            Field::new("text", Scalar::String),
            Field::new("bytes", Scalar::Bytes),
            Field::new("array", Type::Array(Scalar::U16)),
            Field::new("object", Scalar::Cbor),
        ],
    )
    .unwrap();
    let data = encode(
        &d,
        &[
            Value::Absent,
            Value::Null,
            Value::Mapped(1),
            Value::Uuid([7; 16]),
            Value::String("aλ"),
            Value::Bytes(&[]),
            Value::Array(Array::Items(&[Value::U16(4), Value::U16(6)])),
            Value::Cbor(&[0xa1, 0x00, 0x18, 0x2a]),
        ],
    )
    .unwrap();
    let mut index = FrameIndex::default();
    for n in 0..data.len() {
        assert_eq!(index.scan(&d, &data[..n], 1024), Err(Error::Incomplete));
    }
    assert_eq!(
        index.scan(&d, &data, 1024).unwrap(),
        decode_exact(&d, &data).unwrap().consumed()
    );
    assert_eq!(index.spans()[0], [ABSENT, ABSENT]);
    assert_eq!(index.spans()[1], [NULL, NULL]);
    let text = index.spans()[4];
    assert_eq!(&data[text[0] as usize..text[1] as usize], "aλ".as_bytes());
    assert!(index.scan(&d, &data, data.len() - 1).is_err());
    let mut bad = data.clone();
    bad[2] = 3;
    assert!(index.scan(&d, &bad, 1024).is_err());
}
#[test]
fn extended_offsets_empty_arrays_and_cbormaps_are_validated() {
    let d = Definition::new(
        "long",
        1,
        vec![
            Field::new("payload", Scalar::Bytes),
            Field::new("end", Scalar::Cbor),
        ],
    )
    .unwrap();
    let payload = vec![42; 65535];
    let data = encode(&d, &[Value::Bytes(&payload), Value::Cbor(&[0xf6])]).unwrap();
    let mut index = FrameIndex::default();
    assert_eq!(index.scan(&d, &data, 1 << 20).unwrap(), data.len());
    let mut corrupt = data.clone();
    *corrupt.last_mut().unwrap() = 0xff;
    assert!(index.scan(&d, &corrupt, 1 << 20).is_err());
}
