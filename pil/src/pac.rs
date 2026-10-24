//! Version-zero PAC framing and borrowed field access. Compression belongs to
//! the PAC body. This borrowed scanner accepts the decoded representation.
use crate::{ABSENT, Definition, Error, FrameIndex, NULL, uint};

pub const HEADER_SIZE: usize = 64;
pub const SOURCE_EXTENSION: u32 = 1;
pub const NATIVE_EXTENSION: u32 = 0x5454524b;

#[derive(Default)]
pub struct PacIndex {
    common: [u32; 2],
    members: Vec<[u32; 2]>,
    prefix: FrameIndex,
    member: FrameIndex,
}

impl PacIndex {
    pub fn common(&self) -> [u32; 2] {
        self.common
    }
    pub fn members(&self) -> &[[u32; 2]] {
        &self.members
    }

    pub fn scan(
        &mut self,
        definition: &Definition,
        input: &[u8],
        expected_digest: &[u8; 32],
        supported: &[u32],
        maximum: usize,
    ) -> Result<usize, Error> {
        self.members.clear();
        self.common = [0, 0];
        if input.len() < HEADER_SIZE {
            return Err(Error::Incomplete);
        }
        if u32::from_le_bytes(input[..4].try_into().unwrap()) as usize != usize::try_from(u64::from_le_bytes(input[8..16].try_into().unwrap())).map_err(|_| Error::Overflow)? {
            return Err(Error::Invalid("PAC stored length"));
        }
        let flags = u32::from_le_bytes(input[4..8].try_into().unwrap());
        let number = |at| {
            usize::try_from(u64::from_le_bytes(input[at..at + 8].try_into().unwrap()))
                .map_err(|_| Error::Overflow)
        };
        let size = number(8)?;
        let header = number(16)?;
        let count = number(24)?;
        if flags > 1
            || size > maximum
            || size >= NULL as usize
            || header < HEADER_SIZE
            || header > size
        {
            return Err(Error::Invalid("PAC bounds/features"));
        }
        if size > input.len() {
            return Err(Error::Incomplete);
        }
        if input[32..64] != *expected_digest {
            return Err(Error::Invalid("PAC definition binding"));
        }
        let mut at = HEADER_SIZE;
        let h = &input[..header];
        let extensions = usize::try_from(uint::read(h, &mut at)?).map_err(|_| Error::Overflow)?;
        if extensions > (header - at) / 10 {
            return Err(Error::Invalid("PAC extension count"));
        }
        let mut seen = std::collections::HashSet::new();
        for _ in 0..extensions {
            let fixed = uint::take(h, &mut at, 8)?;
            let kind = u32::from_le_bytes(fixed[..4].try_into().unwrap());
            let version = u16::from_le_bytes(fixed[4..6].try_into().unwrap());
            let ef = u16::from_le_bytes(fixed[6..8].try_into().unwrap());
            if ef > 1 || !seen.insert(kind) {
                return Err(Error::Invalid("PAC extension flags/duplicate"));
            }
            if (ef == 1 && (!supported.contains(&kind) || version != 0))
                || (supported.contains(&kind) && version != 0)
            {
                return Err(Error::Invalid("unsupported PAC extension"));
            }
            let length = usize::try_from(uint::read(h, &mut at)?).map_err(|_| Error::Overflow)?;
            let value = uint::take(h, &mut at, length)?;
            if kind == SOURCE_EXTENSION && version == 0 {
                std::str::from_utf8(value).map_err(|_| Error::Invalid("PAC source UTF-8"))?;
            }
        }
        let length = usize::try_from(uint::read(h, &mut at)?).map_err(|_| Error::Overflow)?;
        if length != header - at || (length > 0) != (flags == 1) {
            return Err(Error::Invalid("PAC common bounds"));
        }
        self.common = [at as u32, header as u32];
        if length > 0 && self.prefix.scan(definition, &input[at..header], maximum)? != length {
            return Err(Error::Invalid("PAC common frame"));
        }
        let mask = definition.fields().len().div_ceil(4);
        if mask == 0 || count > u32::MAX as usize || count > (size - header) / mask {
            return Err(Error::Invalid("PAC count"));
        }
        at = header;
        for _ in 0..count {
            let n = self.member.scan(definition, &input[at..size], maximum)?;
            if length > 0 {
                for (c, m) in self.prefix.spans().iter().zip(self.member.spans()) {
                    if c[0] != ABSENT && m[0] != ABSENT {
                        return Err(Error::Invalid("PAC common override"));
                    }
                }
            }
            self.members.push([at as u32, (at + n) as u32]);
            at += n;
        }
        if at != size {
            return Err(Error::Invalid("PAC trailing bytes"));
        }
        Ok(size)
    }
}

/// Borrow the encoded value from a common or member frame. The returned span
/// retains ABSENT/NULL sentinels and does not build or expand application objects.
pub fn field<'a>(
    common: &'a [u8],
    common_index: &FrameIndex,
    member: &'a [u8],
    member_index: &FrameIndex,
    index: usize,
) -> Result<(u32, &'a [u8]), Error> {
    let m = *member_index
        .spans()
        .get(index)
        .ok_or(Error::Invalid("PAC field index"))?;
    let c = common_index
        .spans()
        .get(index)
        .copied()
        .unwrap_or([ABSENT, ABSENT]);
    if c[0] != ABSENT && m[0] != ABSENT {
        return Err(Error::Invalid("PAC override"));
    }
    let (bytes, span) = if c[0] != ABSENT {
        (common, c)
    } else {
        (member, m)
    };
    if span[0] == ABSENT || span[0] == NULL {
        return Ok((span[0], &[]));
    }
    Ok((
        span[0],
        bytes
            .get(span[0] as usize..span[1] as usize)
            .ok_or(Error::Invalid("PAC field bounds"))?,
    ))
}
