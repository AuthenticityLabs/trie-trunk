use pil_codec::{Array, Definition, Field, FrameIndex, Scalar, Type, Value, decode_exact, encode};

#[test]
fn references_keep_all_sixteen_bits_and_presence_states() {
    let definition =
        Definition::new("shared", 1, vec![Field::new("reference", Scalar::Ref16)]).unwrap();
    assert_eq!(
        Definition::from_cbor(&definition.to_cbor().unwrap()).unwrap(),
        definition
    );
    for (value, bytes) in [
        (Value::Absent, vec![0]),
        (Value::Null, vec![0x40]),
        (Value::Ref16(0), vec![0x80, 0, 0]),
        (Value::Ref16(65535), vec![0x80, 255, 255]),
        (Value::Ref16(0x1234), vec![0x80, 0x34, 0x12]),
    ] {
        assert_eq!(encode(&definition, &[value]).unwrap(), bytes);
        assert_eq!(
            decode_exact(&definition, &bytes).unwrap().values(),
            &[value]
        );
        assert_eq!(
            FrameIndex::default()
                .scan(&definition, &bytes, 1024)
                .unwrap(),
            bytes.len()
        );
    }
    // A reference is semantically distinct from a literal integer. There is no
    // truncating conversion and OFFSET cannot be used for this fixed-width type.
    assert!(encode(&definition, &[Value::U16(42)]).is_err());
    assert!(encode(&definition, &[Value::U32(65536)]).is_err());
    for bytes in [&[0x80, 1][..], &[0xc0, 2, 0, 1, 0], &[0x80, 1, 0, 0]] {
        assert!(decode_exact(&definition, bytes).is_err());
    }
}

#[test]
fn reference_arrays_are_fixed_width_and_nonnullable() {
    let d = Definition::new(
        "references",
        1,
        vec![Field::new("items", Type::Array(Scalar::Ref16))],
    )
    .unwrap();
    let values = [Value::Ref16(0), Value::Ref16(65535)];
    let bytes = encode(&d, &[Value::Array(Array::Items(&values))]).unwrap();
    let frame = decode_exact(&d, &bytes).unwrap();
    let Value::Array(Array::Encoded(items)) = frame.values()[0] else {
        panic!()
    };
    assert_eq!(items.iter().collect::<Result<Vec<_>, _>>().unwrap(), values);
    assert!(encode(&d, &[Value::Array(Array::Items(&[Value::Null]))]).is_err());
}
