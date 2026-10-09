use crate::Error;

pub(crate) fn take<'a>(
    input: &'a [u8],
    cursor: &mut usize,
    size: usize,
) -> Result<&'a [u8], Error> {
    let end = cursor.checked_add(size).ok_or(Error::Overflow)?;
    let bytes = input.get(*cursor..end).ok_or(Error::Incomplete)?;
    *cursor = end;
    Ok(bytes)
}

pub(crate) fn write(value: u64, output: &mut Vec<u8>) {
    if value < u64::from(u16::MAX) {
        output.extend_from_slice(&(value as u16).to_le_bytes());
    } else {
        output.extend_from_slice(&u16::MAX.to_le_bytes());
        if value < u64::from(u32::MAX) {
            output.extend_from_slice(&(value as u32).to_le_bytes());
        } else {
            output.extend_from_slice(&u32::MAX.to_le_bytes());
            output.extend_from_slice(&value.to_le_bytes());
        }
    }
}

pub(crate) fn read(input: &[u8], cursor: &mut usize) -> Result<u64, Error> {
    let short = u16::from_le_bytes(take(input, cursor, 2)?.try_into().unwrap());
    if short != u16::MAX {
        return Ok(u64::from(short));
    }
    let medium = u32::from_le_bytes(take(input, cursor, 4)?.try_into().unwrap());
    if medium != u32::MAX {
        if medium < u32::from(u16::MAX) {
            return Err(Error::Invalid("nonminimal extended integer"));
        }
        return Ok(u64::from(medium));
    }
    let long = u64::from_le_bytes(take(input, cursor, 8)?.try_into().unwrap());
    if long < u64::from(u32::MAX) {
        return Err(Error::Invalid("nonminimal extended integer"));
    }
    Ok(long)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn boundary_vectors() {
        let cases: &[(u64, &[u8])] = &[
            (0, &[0, 0]),
            (42, &[42, 0]),
            (65534, &[254, 255]),
            (65535, &[255, 255, 255, 255, 0, 0]),
            (4294967294, &[255, 255, 254, 255, 255, 255]),
            (
                4294967295,
                &[255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 0, 0, 0, 0],
            ),
            (u64::MAX, &[255; 14]),
        ];
        for &(value, wire) in cases {
            let mut encoded = Vec::new();
            write(value, &mut encoded);
            assert_eq!(encoded, wire);
            let mut cursor = 0;
            assert_eq!(read(wire, &mut cursor), Ok(value));
            assert_eq!(cursor, wire.len());
            for length in 0..wire.len() {
                assert_eq!(read(&wire[..length], &mut 0), Err(Error::Incomplete));
            }
        }
    }

    #[test]
    fn rejects_nonminimal_encodings() {
        assert_eq!(
            read(&[255, 255, 42, 0, 0, 0], &mut 0),
            Err(Error::Invalid("nonminimal extended integer"))
        );
        let mut long = vec![255; 6];
        long.extend_from_slice(&65535_u64.to_le_bytes());
        assert_eq!(
            read(&long, &mut 0),
            Err(Error::Invalid("nonminimal extended integer"))
        );
    }
}
