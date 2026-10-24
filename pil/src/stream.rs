use crate::{Definition, Type, decode_extended};
use std::io::{self, Read};

pub fn read_frame_bytes(
    definition: &Definition,
    input: &mut impl Read,
    maximum: usize,
) -> io::Result<Option<Vec<u8>>> {
    let invalid = |message| io::Error::new(io::ErrorKind::InvalidData, message);
    let count = definition.fields().len();
    let mask_len = count.div_ceil(4);
    let mut bytes = Vec::new();
    let mut first = [0];
    loop {
        match input.read(&mut first) {
            Ok(0) => return Ok(None),
            Ok(_) => break,
            Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
            Err(error) => return Err(error),
        }
    }
    bytes.push(first[0]);
    append(input, &mut bytes, mask_len - 1, maximum)?;
    let width_bytes = definition.width_bits(&bytes).div_ceil(8);
    append(input, &mut bytes, width_bytes, maximum)?;
    let mut at = mask_len;
    let plane = definition
        .plane(&bytes, &bytes[..mask_len], &mut at)
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
    let mut width_bit = 0;
    let mut direct = 0usize;
    let mut offsets = 0;
    for (index, field) in definition.fields().iter().enumerate() {
        match (bytes[index / 4] >> (6 - index % 4 * 2)) & 3 {
            2 => {
                let width = field
                    .reference_width()
                    .or_else(|| match field.kind {
                        Type::Scalar(kind) => kind.width(),
                        Type::Array(_) => None,
                    })
                    .ok_or_else(|| invalid("invalid DIRECT field"))?;
                let width = definition
                    .wire_width(index, width, plane, &mut width_bit)
                    .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
                direct = direct
                    .checked_add(width)
                    .ok_or_else(|| invalid("frame size overflow"))?;
            }
            3 => offsets += 1,
            _ => {}
        }
    }
    append(input, &mut bytes, direct, maximum)?;
    let mut previous = 0;
    for _ in 0..offsets {
        let start = bytes.len();
        append(input, &mut bytes, 2, maximum)?;
        if bytes[start..] == [255, 255] {
            append(input, &mut bytes, 4, maximum)?;
            if bytes[start + 2..] == [255; 4] {
                append(input, &mut bytes, 8, maximum)?;
            }
        }
        let (end, _) = decode_extended(&bytes[start..])
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
        if end < previous {
            return Err(invalid("decreasing end offsets"));
        }
        previous = end;
    }
    let size = usize::try_from(previous).map_err(|_| invalid("frame size overflow"))?;
    append(input, &mut bytes, size, maximum)?;
    Ok(Some(bytes))
}

fn append(
    input: &mut impl Read,
    bytes: &mut Vec<u8>,
    count: usize,
    maximum: usize,
) -> io::Result<()> {
    let end = bytes
        .len()
        .checked_add(count)
        .filter(|&n| n <= maximum)
        .ok_or_else(|| {
            io::Error::new(io::ErrorKind::InvalidData, "PIL frame exceeds byte limit")
        })?;
    let start = bytes.len();
    bytes.resize(end, 0);
    input.read_exact(&mut bytes[start..])
}
