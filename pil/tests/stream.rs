use pil_codec::{Definition, Field, Scalar, Value, decode_exact, encode, read_frame_bytes};
use std::io::{self, Read};
struct Fragmented<'a>(&'a [u8]);
impl Read for Fragmented<'_> {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        if buf.is_empty() || self.0.is_empty() {
            return Ok(0);
        }
        buf[0] = self.0[0];
        self.0 = &self.0[1..];
        Ok(1)
    }
}
#[test]
fn reads_fragmented_self_delimiting_frames_without_consuming_next() {
    let def = Definition::new("test", 1, vec![Field::new("value", Scalar::Bytes)]).unwrap();
    let payload = vec![42; 65535];
    let frame = encode(&def, &[Value::Bytes(&payload)]).unwrap();
    let wire = [frame.as_slice(), &[0x40]].concat();
    let mut input = Fragmented(&wire);
    let read = read_frame_bytes(&def, &mut input, 100000).unwrap().unwrap();
    assert_eq!(read, frame);
    assert_eq!(
        decode_exact(&def, &read).unwrap().values(),
        &[Value::Bytes(&payload)]
    );
    assert_eq!(
        read_frame_bytes(&def, &mut input, 1).unwrap().unwrap(),
        [0x40]
    );
    assert!(read_frame_bytes(&def, &mut input, 1).unwrap().is_none());
}
#[test]
fn rejects_truncation_and_size_overflow_before_payload_allocation() {
    let def = Definition::new("test", 1, vec![Field::new("value", Scalar::Bytes)]).unwrap();
    let frame = encode(&def, &[Value::Bytes(b"abc")]).unwrap();
    for end in 1..frame.len() {
        assert_eq!(
            read_frame_bytes(&def, &mut &frame[..end], 100)
                .unwrap_err()
                .kind(),
            io::ErrorKind::UnexpectedEof
        );
    }
    assert_eq!(
        read_frame_bytes(&def, &mut frame.as_slice(), frame.len() - 1)
            .unwrap_err()
            .kind(),
        io::ErrorKind::InvalidData
    );
    let wire = [vec![0xc0], vec![0xff; 14]].concat();
    assert_eq!(
        read_frame_bytes(&def, &mut wire.as_slice(), 100)
            .unwrap_err()
            .kind(),
        io::ErrorKind::InvalidData
    );
}
