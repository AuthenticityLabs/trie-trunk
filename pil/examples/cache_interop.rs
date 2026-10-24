//! Validate frames emitted by the Go collection-native prototype against the
//! existing shared Rust/WASM codec. The storage extension stays in the same map.
use pil_codec::{Definition, decode_exact, read_frame_bytes};
use std::{
    env, fs,
    io::{self, BufReader},
};
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<_> = env::args().collect();
    if args.len() != 3 {
        return Err("usage: cache_interop resource.map frames.pil".into());
    }
    let definition = Definition::from_cbor(&fs::read(&args[1])?)?;
    let mut input = BufReader::new(fs::File::open(&args[2])?);
    let mut count = 0;
    while let Some(frame) = read_frame_bytes(&definition, &mut input, 16 << 20)? {
        decode_exact(&definition, &frame)
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
        count += 1;
    }
    println!(
        "Validated {count} frames with {} fields using the shared Rust PIL codec",
        definition.fields().len()
    );
    Ok(())
}
