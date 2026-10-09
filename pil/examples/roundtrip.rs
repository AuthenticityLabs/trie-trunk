use pil_codec::{Array, Definition, Field, Frames, Scalar, Type, Value, encode};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let definition = Definition::new(
        "event",
        1,
        vec![
            Field::new("id", Scalar::Uuid),
            Field::mapped("service", ["vault", "billing"]),
            Field::new("source_time", Scalar::TimestampUs),
            Field::new("message", Scalar::String),
            Field::new("durations", Type::Array(Scalar::U16)),
        ],
    )?;
    let definition_bytes = definition.to_cbor()?;
    let negotiated = Definition::from_cbor(&definition_bytes)?;
    let durations = [Value::U16(100), Value::U16(200), Value::U16(300)];
    let first = encode(
        &definition,
        &[
            Value::Uuid([
                0x01, 0x7f, 0x22, 0xe2, 0x79, 0xb0, 0x7c, 0xc3, 0x98, 0xc4, 0xdc, 0x0c, 0x0c, 0x07,
                0x39, 0x8f,
            ]),
            Value::Mapped(0),
            Value::TimestampUs(1_645_557_742_000_123),
            Value::String("Request completed"),
            Value::Array(Array::Items(&durations)),
        ],
    )?;
    let second = encode(
        &definition,
        &[
            Value::Absent,
            Value::Absent,
            Value::Absent,
            Value::String("Reviewed"),
            Value::Null,
        ],
    )?;
    let stream = [first.as_slice(), second.as_slice()].concat();
    println!(
        "Definition: {} bytes, exchanged once",
        definition_bytes.len()
    );
    println!("Frames: {} and {} bytes", first.len(), second.len());
    println!("Wire: {stream:02x?}");
    for (index, result) in Frames::new(&negotiated, &stream).enumerate() {
        let frame = result?;
        println!("Frame {index}: {} bytes", frame.consumed());
        println!("  service reference: {:?}", frame.get("service"));
        println!("  service text on demand: {:?}", frame.string(1));
        println!("  message: {:?}", frame.get("message"));
        match frame.get("durations") {
            Some(Value::Array(Array::Encoded(view))) => {
                for value in view.iter() {
                    println!("  duration: {:?}", value?);
                }
            }
            value => println!("  durations: {value:?}"),
        }
    }
    Ok(())
}
