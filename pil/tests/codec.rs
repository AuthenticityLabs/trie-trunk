use pil_codec::{
    Array, ArrayView, Definition, Error, Field, Frames, Scalar, Type, Value, decode, decode_exact,
    encode, encode_extended,
};

fn definition(fields: Vec<Field>) -> Definition {
    Definition::new("test", 1, fields).unwrap()
}

fn single(kind: impl Into<Type>) -> Definition {
    definition(vec![Field::new("value", kind)])
}

fn assert_wire(def: &Definition, values: &[Value<'_>], expected: &[u8]) {
    assert_eq!(encode(def, values).unwrap(), expected);
    let frame = decode_exact(def, expected).unwrap();
    assert_eq!(frame.values(), values);
    assert_eq!(frame.consumed(), expected.len());
}

fn array_values<'a>(frame: &pil_codec::Frame<'a>) -> Vec<Value<'a>> {
    let Value::Array(Array::Encoded(view)) = frame.values()[0] else {
        panic!("expected borrowed array");
    };
    view.iter().collect::<Result<_, _>>().unwrap()
}

#[test]
fn partial_state_vectors_match_draft() {
    let def = definition(vec![
        Field::new("id", Scalar::Uuid),
        Field::mapped("status", ["active", "pending", "disabled"]),
        Field::new("firstName", Scalar::String),
        Field::new("lastName", Scalar::String),
    ]);
    assert_wire(
        &def,
        &[
            Value::Absent,
            Value::Mapped(1),
            Value::Absent,
            Value::Absent,
        ],
        &[0x20, 1],
    );
    assert_wire(
        &def,
        &[
            Value::Absent,
            Value::Absent,
            Value::String("Jason"),
            Value::Absent,
        ],
        b"\x0c\x05\x00Jason",
    );
    assert_wire(
        &def,
        &[Value::Absent, Value::Absent, Value::Absent, Value::Null],
        &[0x01],
    );
}

#[test]
fn cumulative_offsets_match_draft() {
    let def = definition(vec![
        Field::new("first", Scalar::String),
        Field::new("last", Scalar::String),
        Field::new("email", Scalar::String),
    ]);
    assert_wire(
        &def,
        &[
            Value::String("Jason"),
            Value::String("Smith"),
            Value::String("j@example.com"),
        ],
        b"\xfc\x05\x00\x0a\x00\x17\x00JasonSmithj@example.com",
    );
    assert_wire(
        &def,
        &[Value::String(""), Value::String("abc"), Value::String("")],
        b"\xfc\x00\x00\x03\x00\x03\x00abc",
    );
}

#[test]
fn direct_values_precede_all_offsets_and_payloads() {
    let def = definition(vec![
        Field::new("a", Scalar::String),
        Field::new("b", Scalar::U16),
        Field::new("c", Scalar::Bytes),
        Field::new("d", Scalar::Bool),
        Field::new("e", Scalar::U8),
    ]);
    assert_wire(
        &def,
        &[
            Value::String("hi"),
            Value::U16(0x1234),
            Value::Bytes(&[0xff]),
            Value::Bool(true),
            Value::U8(9),
        ],
        &[0xee, 0x80, 0x34, 0x12, 1, 9, 2, 0, 3, 0, b'h', b'i', 0xff],
    );
}

#[test]
fn uuid_keeps_standard_six_byte_timestamp_prefix() {
    let def = single(Scalar::Uuid);
    let uuid = [
        0x01, 0x7f, 0x22, 0xe2, 0x79, 0xb0, 0x7c, 0xc3, 0x98, 0xc4, 0xdc, 0x0c, 0x0c, 0x07, 0x39,
        0x8f,
    ];
    let mut wire = vec![0x80];
    wire.extend_from_slice(&uuid);
    assert_wire(&def, &[Value::Uuid(uuid)], &wire);
    assert_eq!(wire.len(), 17);
    assert_eq!(&wire[1..7], &uuid[..6]);
}

#[test]
fn timestamp_units_are_definition_types_with_same_signed_width() {
    for (kind, value, name) in [
        (Scalar::TimestampMs, Value::TimestampMs(-2), "TIMESTAMP_MS"),
        (Scalar::TimestampUs, Value::TimestampUs(-2), "TIMESTAMP_US"),
        (Scalar::TimestampNs, Value::TimestampNs(-2), "TIMESTAMP_NS"),
    ] {
        assert_wire(
            &single(kind),
            &[value],
            &[0x80, 0xfe, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff],
        );
        assert_eq!(kind.to_string(), name);
        assert_eq!(name.parse::<Scalar>().unwrap(), kind);
    }
    assert!(encode(&single(Scalar::TimestampUs), &[Value::TimestampNs(2)]).is_err());
    assert!("TIMESTAMP".parse::<Type>().is_err());
}

#[test]
fn fixed_scalars_round_trip_boundaries() {
    let values = [
        Value::Bool(false),
        Value::Bool(true),
        Value::I8(i8::MIN),
        Value::I16(i16::MIN),
        Value::I32(i32::MIN),
        Value::I64(i64::MIN),
        Value::U8(u8::MAX),
        Value::U16(u16::MAX),
        Value::U32(u32::MAX),
        Value::U64(u64::MAX),
        Value::F32(-1.25),
        Value::F64(f64::INFINITY),
    ];
    for value in values {
        let def = single(value.scalar().unwrap());
        let wire = encode(&def, &[value]).unwrap();
        assert_eq!(wire.len(), 1 + value.scalar().unwrap().width().unwrap());
        assert_eq!(decode_exact(&def, &wire).unwrap().values(), &[value]);
    }
    assert_wire(
        &single(Scalar::F32),
        &[Value::F32(1.0)],
        &[0x80, 0, 0, 0x80, 0x3f],
    );
    let bits = 0x7ff8_0000_0000_0123;
    let def = single(Scalar::F64);
    let bytes = encode(&def, &[Value::F64(f64::from_bits(bits))]).unwrap();
    let Value::F64(value) = decode_exact(&def, &bytes).unwrap().values()[0] else {
        panic!()
    };
    assert_eq!(value.to_bits(), bits);
}

#[test]
fn dictionary_widths_use_maximum_zero_based_reference() {
    for (count, width, direct) in [
        (1, 1, vec![0]),
        (256, 1, vec![0xff]),
        (257, 2, vec![0, 1]),
        (65536, 2, vec![0xff, 0xff]),
        (65537, 4, vec![0, 0, 1, 0]),
    ] {
        let def = definition(vec![Field::mapped(
            "status",
            (0..count).map(|i| i.to_string()),
        )]);
        assert_eq!(def.fields()[0].reference_width(), Some(width));
        let mut wire = vec![0x80];
        wire.extend_from_slice(&direct);
        assert_wire(&def, &[Value::Mapped(count - 1)], &wire);
        assert!(encode(&def, &[Value::Mapped(count)]).is_err());
        assert_wire(&def, &[Value::String("other")], b"\xc0\x05\x00other");
    }
}

#[test]
fn strings_and_dictionary_values_are_borrowed() {
    let def = definition(vec![
        Field::mapped("status", ["active"]),
        Field::new("name", Scalar::String),
    ]);
    let bytes = encode(&def, &[Value::Mapped(0), Value::String("Jason")]).unwrap();
    let frame = decode_exact(&def, &bytes).unwrap();
    assert_eq!(frame.values()[0], Value::Mapped(0));
    assert_eq!(
        frame.string(0).unwrap().as_ptr(),
        def.fields()[0].values[0].as_ptr()
    );
    assert_eq!(
        frame.string(1).unwrap().as_ptr(),
        bytes[frame.prefix_len()..].as_ptr()
    );
    assert_eq!(frame.get("name"), Some(Value::String("Jason")));
    assert_eq!(frame.get("missing"), None);
    assert_eq!(frame.string(8), None);
    assert_eq!(frame.as_bytes().as_ptr(), bytes.as_ptr());
}

#[test]
fn fixed_array_has_only_count_and_contiguous_values() {
    let def = single(Type::Array(Scalar::U16));
    let items = [Value::U16(100), Value::U16(200), Value::U16(300)];
    let expected = [0xc0, 8, 0, 3, 0, 100, 0, 200, 0, 44, 1];
    assert_eq!(
        encode(&def, &[Value::Array(Array::Items(&items))]).unwrap(),
        expected
    );
    let frame = decode_exact(&def, &expected).unwrap();
    assert_eq!(array_values(&frame), items);
    assert_eq!(encode(&def, frame.values()).unwrap(), expected);
}

#[test]
fn variable_array_matches_addendum_and_borrows_payload() {
    let def = single(Type::Array(Scalar::String));
    let items = [
        Value::String("Jason"),
        Value::String("Smith"),
        Value::String("Alexander"),
    ];
    let expected = b"\xc0\x1b\x00\x03\x00\x05\x00\x0a\x00\x13\x00JasonSmithAlexander";
    assert_eq!(
        encode(&def, &[Value::Array(Array::Items(&items))]).unwrap(),
        expected
    );
    let frame = decode_exact(&def, expected).unwrap();
    assert_eq!(array_values(&frame), items);
    let Value::Array(Array::Encoded(view)) = frame.values()[0] else {
        panic!()
    };
    assert_eq!(view.len(), 3);
    assert_eq!(view.element_type(), Scalar::String);
    assert_eq!(view.as_bytes().as_ptr(), expected[3..].as_ptr());
    let mut iter = view.iter();
    assert_eq!(iter.len(), 3);
    let Value::String(first) = iter.next().unwrap().unwrap() else {
        panic!()
    };
    assert_eq!(first.as_ptr(), expected[11..].as_ptr());
    assert_eq!(iter.len(), 2);
    assert_eq!(encode(&def, frame.values()).unwrap(), expected);
}

#[test]
fn arrays_distinguish_absent_null_empty_and_empty_elements() {
    for kind in [Scalar::U16, Scalar::String, Scalar::Bytes, Scalar::Cbor] {
        let def = single(Type::Array(kind));
        assert_wire(&def, &[Value::Absent], &[0]);
        assert_wire(&def, &[Value::Null], &[0x40]);
        let empty = Value::Array(Array::Items(&[]));
        assert_eq!(encode(&def, &[empty]).unwrap(), [0xc0, 2, 0, 0, 0]);
        assert!(array_values(&decode_exact(&def, &[0xc0, 2, 0, 0, 0]).unwrap()).is_empty());
        for invalid in [Value::Null, Value::Absent] {
            assert!(encode(&def, &[Value::Array(Array::Items(&[invalid]))]).is_err());
        }
    }
    let def = single(Type::Array(Scalar::String));
    let items = [Value::String(""), Value::String("é"), Value::String("")];
    let wire = b"\xc0\x0a\x00\x03\x00\x00\x00\x02\x00\x02\x00\xc3\xa9";
    assert_eq!(
        encode(&def, &[Value::Array(Array::Items(&items))]).unwrap(),
        wire
    );
    assert_eq!(array_values(&decode_exact(&def, wire).unwrap()), items);
}

#[test]
fn arrays_preserve_timestamp_units_and_uuid_bytes() {
    for value in [
        Value::TimestampMs(-1),
        Value::TimestampUs(-1),
        Value::TimestampNs(-1),
        Value::Uuid([0x12; 16]),
    ] {
        let def = single(Type::Array(value.scalar().unwrap()));
        let items = [value, value];
        let bytes = encode(&def, &[Value::Array(Array::Items(&items))]).unwrap();
        assert_eq!(array_values(&decode_exact(&def, &bytes).unwrap()), items);
    }
    let def = single(Type::Array(Scalar::U16));
    assert!(encode(&def, &[Value::Array(Array::Items(&[Value::U8(1)]))]).is_err());
    let view = ArrayView::parse(Scalar::U8, &[1, 0, 7]).unwrap();
    assert!(encode(&def, &[Value::Array(Array::Encoded(view))]).is_err());
}

#[test]
fn cbor_array_allows_complex_values_but_not_null_elements() {
    let def = single(Type::Array(Scalar::Cbor));
    let items = [Value::Cbor(b"\xa1\x61x\xf6"), Value::Cbor(b"\x82\x01\x02")];
    let bytes = encode(&def, &[Value::Array(Array::Items(&items))]).unwrap();
    assert_eq!(array_values(&decode_exact(&def, &bytes).unwrap()), items);
    for invalid in [&b"\xf6"[..], &b"\x01\x02"[..], &b"\xa1"[..]] {
        assert!(encode(&def, &[Value::Array(Array::Items(&[Value::Cbor(invalid)]))]).is_err());
        let mut array = vec![1, 0];
        encode_extended(invalid.len() as u64, &mut array);
        array.extend_from_slice(invalid);
        assert!(ArrayView::parse(Scalar::Cbor, &array).is_err());
    }
}

#[test]
fn array_boundaries_reject_truncation_padding_and_decreasing_offsets() {
    for wire in [&[][..], &[3, 0, 1, 0], &[0, 0, 1], &[1, 0, 0, 0, 0]] {
        assert!(ArrayView::parse(Scalar::U16, wire).is_err(), "{wire:?}");
    }
    for wire in [
        &b"\x00\x00\x00"[..],
        &b"\x02\x00\x02\x00\x01\x00ab"[..],
        &b"\x01\x00\x02\x00a"[..],
        &b"\x01\x00\x00\x00a"[..],
        &b"\x01\x00\x01\x00\xff"[..],
        &b"\x03\x00\x00\x00"[..],
    ] {
        assert!(ArrayView::parse(Scalar::String, wire).is_err(), "{wire:?}");
    }
    let mut enormous = Vec::new();
    encode_extended(u64::MAX, &mut enormous);
    assert_eq!(
        ArrayView::parse(Scalar::U64, &enormous),
        Err(Error::Overflow)
    );
    assert!(ArrayView::parse(Scalar::String, &enormous).is_err());
    assert!(ArrayView::parse(Scalar::Bool, &[1, 0, 2]).is_err());
    assert!(decode_exact(&single(Type::Array(Scalar::U16)), &[0xc0, 4, 0, 2, 0, 1, 0]).is_err());
}

#[test]
fn large_offsets_and_counts_switch_to_extended_forms() {
    for (length, prefix) in [(65534, 3), (65535, 7)] {
        let def = single(Scalar::Bytes);
        let payload = vec![0xab; length];
        let bytes = encode(&def, &[Value::Bytes(&payload)]).unwrap();
        let frame = decode_exact(&def, &bytes).unwrap();
        assert_eq!(frame.prefix_len(), prefix);
        assert_eq!(frame.values(), &[Value::Bytes(&payload)]);
        assert_eq!(frame.consumed(), prefix + length);
    }
    let items = vec![Value::U8(7); 65535];
    let def = single(Type::Array(Scalar::U8));
    let bytes = encode(&def, &[Value::Array(Array::Items(&items))]).unwrap();
    let frame = decode_exact(&def, &bytes).unwrap();
    let Value::Array(Array::Encoded(view)) = frame.values()[0] else {
        panic!()
    };
    assert_eq!(&view.as_bytes()[..6], &[255, 255, 255, 255, 0, 0]);
    assert_eq!(view.len(), 65535);
    assert_eq!(view.iter().last().unwrap().unwrap(), Value::U8(7));
    let payload = vec![1; 65535];
    let items = [Value::Bytes(&payload), Value::Bytes(&[])];
    let def = single(Type::Array(Scalar::Bytes));
    let bytes = encode(&def, &[Value::Array(Array::Items(&items))]).unwrap();
    assert_eq!(array_values(&decode_exact(&def, &bytes).unwrap()), items);
}

#[test]
fn definition_cbor_preserves_order_dictionaries_and_types() {
    let def = Definition::new(
        "users",
        42,
        vec![
            Field::new("z", Scalar::Uuid),
            Field::mapped("status", ["active", "pending"]),
            Field::new("a", Type::Array(Scalar::TimestampNs)),
            Field::new("@fields", Scalar::Cbor),
        ],
    )
    .unwrap();
    let bytes = def.to_cbor().unwrap();
    let decoded = Definition::from_cbor(&bytes).unwrap();
    assert_eq!(decoded, def);
    assert_eq!(decoded.identity(), "users");
    assert_eq!(decoded.version(), 42);
    assert_eq!(decoded.fields()[0].name, "z");
    assert_eq!(decoded.fields()[1].values[1], "pending");
    assert_eq!(decoded.to_cbor().unwrap(), bytes);
    let mut trailing = bytes;
    trailing.push(0);
    assert!(Definition::from_cbor(&trailing).is_err());
}

#[test]
fn invalid_definitions_fail_before_frames_are_decoded() {
    assert!(Definition::new("empty", 0, vec![]).is_err());
    assert!(Definition::new("", 0, vec![Field::new("a", Scalar::U8)]).is_err());
    for fields in [
        vec![Field::new("x", Scalar::U8), Field::new("x", Scalar::U8)],
        vec![Field::new("", Scalar::U8)],
        vec![Field::mapped("x", ["a", "a"])],
        vec![Field::new("@custom", Scalar::U8)],
        vec![Field::new("@fields", Scalar::String)],
        vec![Field {
            name: "x".into(),
            kind: Scalar::U8.into(),
            values: vec!["a".into()],
        }],
    ] {
        assert!(Definition::new("bad", 0, fields).is_err());
    }
    assert!("ARRAY<ARRAY<U16>>".parse::<Type>().is_err());
    assert!("OBJECT".parse::<Type>().is_err());
    assert!(Definition::from_cbor(b"\xa2\x63pil\x00\x63pil\x00").is_err());
}

#[test]
fn dynamic_fields_require_unique_text_keys_without_collisions() {
    let def = definition(vec![
        Field::new("id", Scalar::U8),
        Field::new("@fields", Scalar::Cbor),
    ]);
    assert_wire(&def, &[Value::Absent, Value::Absent], &[0]);
    assert_wire(
        &def,
        &[Value::U8(1), Value::Cbor(b"\xa0")],
        &[0xb0, 1, 1, 0, 0xa0],
    );
    assert_wire(
        &def,
        &[Value::Absent, Value::Cbor(b"\xa1\x61x\x01")],
        b"\x30\x04\x00\xa1\x61x\x01",
    );
    assert!(encode(&def, &[Value::Absent, Value::Null]).is_err());
    assert!(decode_exact(&def, &[0x10]).is_err());
    for invalid in [
        &b"\xf6"[..],
        &b"\x80"[..],
        &b"\xa1\x01\x02"[..],
        &b"\xa1\x62id\x01"[..],
        &b"\xa1\x62@x\x01"[..],
        &b"\xa2\x61x\x01\x61x\x02"[..],
    ] {
        assert!(encode(&def, &[Value::Absent, Value::Cbor(invalid)]).is_err());
        let mut wire = vec![0x30];
        encode_extended(invalid.len() as u64, &mut wire);
        wire.extend_from_slice(invalid);
        assert!(decode_exact(&def, &wire).is_err());
    }
}

#[test]
fn cbor_fields_require_exactly_one_item_and_enforce_depth_limit() {
    let def = single(Scalar::Cbor);
    assert_wire(&def, &[Value::Cbor(b"\xf6")], b"\xc0\x01\x00\xf6");
    for payload in [&b""[..], &b"\x01\x02"[..], &b"\xff"[..], &b"\xa1"[..]] {
        assert!(encode(&def, &[Value::Cbor(payload)]).is_err());
        let mut bytes = vec![0xc0];
        encode_extended(payload.len() as u64, &mut bytes);
        bytes.extend_from_slice(payload);
        assert!(decode_exact(&def, &bytes).is_err());
    }
    let mut deep = vec![0x81; 65];
    deep.push(1);
    assert!(encode(&def, &[Value::Cbor(&deep)]).is_err());
}

#[test]
fn malformed_masks_values_and_offsets_are_rejected() {
    for (kind, wire) in [
        (Type::Scalar(Scalar::U8), &b"\x81\x01"[..]),
        (Type::Scalar(Scalar::U8), &b"\xc0\x01\x00\x01"[..]),
        (Type::Scalar(Scalar::Bool), &b"\x80\x02"[..]),
        (Type::Scalar(Scalar::String), &b"\x80"[..]),
        (Type::Scalar(Scalar::Bytes), &b"\x80"[..]),
        (Type::Array(Scalar::U8), &b"\x80"[..]),
        (Type::Scalar(Scalar::String), &b"\xc0\x01\x00\xff"[..]),
        (
            Type::Scalar(Scalar::String),
            &b"\xc0\xff\xff\x01\x00\x00\x00a"[..],
        ),
    ] {
        assert!(matches!(
            decode_exact(&single(kind), wire),
            Err(Error::Invalid(_))
        ));
    }
    let def = definition(vec![Field::mapped("x", ["a"])]);
    assert!(decode_exact(&def, &[0x80, 1]).is_err());
    let def = definition(vec![
        Field::new("a", Scalar::Bytes),
        Field::new("b", Scalar::Bytes),
    ]);
    assert!(decode_exact(&def, b"\xf0\x02\x00\x01\x00ab").is_err());
    let mut overflow = vec![0xc0];
    encode_extended(u64::MAX, &mut overflow);
    assert!(matches!(
        decode_exact(&single(Scalar::Bytes), &overflow),
        Err(Error::Overflow)
    ));
    assert!(encode(&single(Scalar::U8), &[]).is_err());
    assert!(encode(&single(Scalar::String), &[Value::Mapped(0)]).is_err());
}

#[test]
fn incomplete_input_is_distinct_from_invalid_complete_frame() {
    let def = definition(vec![
        Field::new("n", Scalar::U64),
        Field::new("names", Type::Array(Scalar::String)),
    ]);
    let items = [Value::String("Jason"), Value::String("Smith")];
    let bytes = encode(&def, &[Value::U64(123), Value::Array(Array::Items(&items))]).unwrap();
    for end in 0..bytes.len() {
        assert!(
            matches!(decode(&def, &bytes[..end]), Err(Error::Incomplete)),
            "end={end}"
        );
    }
    assert!(decode_exact(&def, &bytes).is_ok());
    assert!(matches!(
        decode_exact(&single(Type::Array(Scalar::U16)), &[0xc0, 2, 0, 1, 0]),
        Err(Error::Invalid(_))
    ));
}

#[test]
fn concatenated_frames_preserve_absence_and_null() {
    let def = single(Scalar::String);
    let stream = b"\xc0\x03\x00one\x00\x40\xc0\x00\x00";
    let first = decode(&def, stream).unwrap();
    assert_eq!(first.consumed(), 6);
    assert_eq!(first.values(), &[Value::String("one")]);
    assert!(decode_exact(&def, stream).is_err());
    let frames = Frames::new(&def, stream)
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    assert_eq!(frames.len(), 4);
    for (frame, value) in frames.iter().zip([
        Value::String("one"),
        Value::Absent,
        Value::Null,
        Value::String(""),
    ]) {
        assert_eq!(frame.values(), &[value]);
    }
    let mut broken = Frames::new(&def, &[0x40, 0xc0]);
    assert_eq!(broken.next().unwrap().unwrap().values(), &[Value::Null]);
    assert!(matches!(broken.next().unwrap(), Err(Error::Incomplete)));
    assert!(broken.next().is_none());
}
