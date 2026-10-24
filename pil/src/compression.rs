//! Bounded, single-member gzip decoding for the negotiated event envelope.
//! Does not change PIL primitive/frame semantics. Output capacity is reused.
use std::io::{self, Read};
const MAX: usize = u32::MAX as usize - 1;
pub fn inflate(input: &[u8], length: usize, output: &mut Vec<u8>) -> io::Result<()> {
    if length == 0 || length > MAX || input.len() > MAX {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "invalid gzip event length",
        ));
    }
    let mut decoder = flate2::bufread::GzDecoder::new(input);
    output.try_reserve(length.saturating_sub(output.len())).map_err(io::Error::other)?;
    output.resize(length, 0);
    decoder.read_exact(output)?;
    let mut tail = [0];
    if decoder.read(&mut tail)? != 0 || !decoder.into_inner().is_empty() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "gzip event trailing bytes or length mismatch",
        ));
    }
    Ok(())
}
#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    #[test]
    fn bounded_member() {
        let mut encoder = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
        encoder.write_all(b"hello hello hello").unwrap();
        let raw = encoder.finish().unwrap();
        let mut out = Vec::new();
        inflate(&raw, 17, &mut out).unwrap();
        assert_eq!(out, b"hello hello hello");
        assert!(inflate(&raw, 16, &mut out).is_err());
        assert!(inflate(&raw, 18, &mut out).is_err());
        assert!(inflate(&raw[..raw.len() - 1], 17, &mut out).is_err());
        let mut bad = raw.clone();
        bad[raw.len() - 8] ^= 1;
        assert!(inflate(&bad, 17, &mut out).is_err());
        let mut two = raw.clone();
        two.extend_from_slice(&raw);
        assert!(inflate(&two, 17, &mut out).is_err());
        assert!(inflate(&raw, MAX + 1, &mut out).is_err());
    }
}
