use pil_codec::{
    Array, CborMap, CborRef, Definition, Field, FrameEncoder, Scalar, Type, Value, decode_exact,
    encode,
};

fn owned_accepts(bytes: &[u8]) -> bool {
    let mut input = bytes;
    ciborium::de::from_reader_with_recursion_limit::<ciborium::Value, _>(&mut input, 64).is_ok()
        && input.is_empty()
}

#[test]
fn borrowed_validation_matches_owned_decoder() {
    let valid: &[&[u8]] = &[
        &[0],
        &[0x1b, 255, 255, 255, 255, 255, 255, 255, 255],
        &[0xf7],
        b"\xa2\x01\x65hello\x02\x83\xf6\x40\xa0",
        b"\x9f\x01\xa0\xff",
        b"\xbf\x01\x02\xff",
        b"\x5f\x42ab\x41c\xff",
        b"\x7f\x62ab\x61c\xff",
        b"\xc0\x61a",
        b"\xc2\x42\x01\x00",
        b"\xfa\x7f\xc0\x00\x00",
    ];
    for &bytes in valid {
        assert!(owned_accepts(bytes), "{bytes:x?}");
        assert_eq!(CborRef::parse(bytes).unwrap().as_bytes(), bytes);
        for size in 0..bytes.len() {
            assert!(
                CborRef::parse(&bytes[..size]).is_err(),
                "truncated {bytes:x?} at {size}"
            );
        }
        let mut extra = bytes.to_vec();
        extra.push(0);
        assert!(CborRef::parse(&extra).is_err());
    }
    // Small malformed inputs exercise every header and random boundary/count
    // combinations against the previously used decoder, without a new fuzzer.
    for byte in 0..=255 {
        assert_eq!(
            CborRef::parse(&[byte]).is_ok(),
            owned_accepts(&[byte]),
            "{byte:x}"
        );
    }
    let mut seed = 728391u64;
    for size in 1..=64 {
        for _ in 0..100 {
            let bytes: Vec<_> = (0..size)
                .map(|_| {
                    seed ^= seed << 13;
                    seed ^= seed >> 7;
                    seed ^= seed << 17;
                    seed as u8
                })
                .collect();
            assert_eq!(
                CborRef::parse(&bytes).is_ok(),
                owned_accepts(&bytes),
                "{bytes:x?}"
            );
        }
    }
}

#[test]
fn borrowed_maps_check_keys_values_and_complete_boundary() {
    for bytes in [
        b"\xa2\x01\x65hello\x02\xf6".as_slice(),
        b"\xbf\x01\x65hello\x02\xf6\xff",
    ] {
        let pairs = CborMap::new(bytes, 32)
            .unwrap()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        assert_eq!(pairs.len(), 2);
        assert_eq!(pairs[0].0.unsigned(), Some(1));
        assert_eq!(pairs[0].1.text(), Some("hello"));
        assert!(pairs[1].1.is_null());
        let text = pairs[0].1.text().unwrap();
        assert!(
            text.as_ptr() >= bytes.as_ptr()
                && text.as_ptr() < bytes.as_ptr().wrapping_add(bytes.len())
        );
    }
    for bytes in [
        b"\xa1\x01".as_slice(),
        b"\xbf\x01\xff",
        b"\xbf\x01\x02",
        b"\xa0\x00",
        b"\xa1\x01\x61\xff",
        b"\xa1\x01\x7f\x41a\xff",
    ] {
        assert!(
            CborMap::new(bytes, 32)
                .and_then(|map| map.collect::<Result<Vec<_>, _>>())
                .is_err(),
            "{bytes:x?}"
        );
    }
    let mut deep = vec![0xa1, 1];
    deep.extend([0x81; 32]);
    deep.push(0);
    assert!(
        CborMap::new(&deep, 32)
            .unwrap()
            .collect::<Result<Vec<_>, _>>()
            .is_err()
    );
    assert!(
        CborMap::new(&deep, 33)
            .unwrap()
            .collect::<Result<Vec<_>, _>>()
            .is_ok()
    );
}

#[test]
fn reusable_encoder_matches_wire_and_recovers_after_error() {
    let definition = Definition::new(
        "reused",
        1,
        vec![
            Field::new("text", Scalar::String),
            Field::mapped("level", ["INFO", "ERROR"]),
            Field::new("data", Scalar::Cbor),
            Field::new("array", Type::Array(Scalar::U16)),
        ],
    )
    .unwrap();
    let mut encoder = FrameEncoder::default();
    for size in [0, 70000, 1, 65534, 255, 0] {
        let text = "x".repeat(size);
        let array = [Value::U16(3), Value::U16(5)];
        let values = [
            Value::String(&text),
            Value::Mapped(1),
            Value::Cbor(b"\xa1\x01\x02"),
            Value::Array(Array::Items(&array)),
        ];
        let expected = encode(&definition, &values).unwrap();
        let mut direct = values;
        direct[2] = Value::ValidatedCbor(CborRef::parse(b"\xa1\x01\x02").unwrap());
        assert_eq!(
            encoder.encode_with(&definition, |i| Ok(direct[i])).unwrap(),
            expected
        );
        assert!(decode_exact(&definition, &expected).is_ok());
        let mut invalid = direct;
        invalid[1] = Value::Mapped(9);
        assert!(encoder.encode(&definition, &invalid).is_err());
        assert_eq!(encoder.encode(&definition, &direct).unwrap(), expected);
        assert!(encoder.encode(&definition, &direct[..3]).is_err());
        assert_eq!(
            encoder.encode(&definition, &[Value::Absent; 4]).unwrap(),
            [0]
        );
    }
}

#[test]
fn validated_cbor_still_observes_array_and_dynamic_field_rules() {
    let array =
        Definition::new("array", 1, vec![Field::new("v", Type::Array(Scalar::Cbor))]).unwrap();
    let values = [Value::ValidatedCbor(CborRef::parse(&[0xf6]).unwrap())];
    assert!(encode(&array, &[Value::Array(Array::Items(&values))]).is_err());
    let fields = Definition::new("fields", 1, vec![Field::new("@fields", Scalar::Cbor)]).unwrap();
    assert!(
        encode(
            &fields,
            &[Value::ValidatedCbor(CborRef::parse(&[0]).unwrap())]
        )
        .is_err()
    );
}
