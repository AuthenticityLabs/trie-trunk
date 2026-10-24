use crate::{ArrayView, CborRef, Definition, Error, Scalar, Type, uint, value};

/// Reusable offsets into an encoded frame. No decoded event or owned CBOR tree.
#[derive(Default)]
pub struct FrameIndex {
    spans: Vec<[u32; 2]>,
}
pub const ABSENT: u32 = u32::MAX;
pub const NULL: u32 = u32::MAX - 1;
impl FrameIndex {
    pub fn spans(&self) -> &[[u32; 2]] {
        &self.spans
    }
    /// Validate one frame at the start of input. Incomplete data may be retried.
    pub fn scan(
        &mut self,
        definition: &Definition,
        input: &[u8],
        maximum: usize,
    ) -> Result<usize, Error> {
        let count = definition.fields().len();
        let mut cursor = 0;
        let mask = uint::take(input, &mut cursor, count.div_ceil(4))?;
        if count % 4 != 0 && mask[mask.len() - 1] & ((1 << ((4 - count % 4) * 2)) - 1) != 0 {
            return Err(Error::Invalid("nonzero unused mask bits"));
        }
        let plane = definition.plane(input, mask, &mut cursor)?;
        let mut width_bit = 0;
        self.spans.clear();
        let state = |i: usize| (mask[i / 4] >> (6 - 2 * (i % 4))) & 3;
        for (i, field) in definition.fields().iter().enumerate() {
            self.spans.push(match state(i) {
                0 => [ABSENT, ABSENT],
                1 => {
                    if field.name == "@fields" {
                        return Err(Error::Invalid("@fields cannot be NULL"));
                    }
                    [NULL, NULL]
                }
                2 => {
                    let width = field
                        .reference_width()
                        .or_else(|| match field.kind {
                            Type::Scalar(k) => k.width(),
                            _ => None,
                        })
                        .ok_or(Error::Invalid("invalid DIRECT field"))?;
                    let width = definition.wire_width(i, width, plane, &mut width_bit)?;
                    let start = cursor;
                    let bytes = uint::take(input, &mut cursor, width)?;
                    if field.reference_width().is_some() {
                        let mut n = [0; 8];
                        n[..width].copy_from_slice(bytes);
                        if u64::from_le_bytes(n) >= field.values.len() as u64 {
                            return Err(Error::Invalid("mapped reference is outside dictionary"));
                        }
                    } else if let Type::Scalar(_) = field.kind {
                        definition.integer_value(i, bytes)?;
                    }
                    [
                        u32::try_from(start).map_err(|_| Error::Overflow)?,
                        u32::try_from(cursor).map_err(|_| Error::Overflow)?,
                    ]
                }
                3 => {
                    if matches!(field.kind, Type::Scalar(k) if k.width().is_some()) {
                        return Err(Error::Invalid("OFFSET fixed-width field"));
                    }
                    [0, 0]
                }
                _ => unreachable!(),
            });
        }
        let mut previous = 0usize;
        for i in 0..count {
            if state(i) != 3 {
                continue;
            }
            let end =
                usize::try_from(uint::read(input, &mut cursor)?).map_err(|_| Error::Overflow)?;
            if end < previous {
                return Err(Error::Invalid("decreasing field end offsets"));
            }
            if end > maximum {
                return Err(Error::Invalid("PIL frame exceeds byte limit"));
            }
            self.spans[i] = [previous as u32, end as u32];
            previous = end;
        }
        let prefix = cursor;
        let length = prefix
            .checked_add(previous)
            .filter(|n| *n <= maximum && *n < NULL as usize)
            .ok_or(Error::Overflow)?;
        uint::take(input, &mut cursor, previous)?;
        for (i, field) in definition.fields().iter().enumerate() {
            if state(i) != 3 {
                continue;
            }
            let span = &mut self.spans[i];
            span[0] += prefix as u32;
            span[1] += prefix as u32;
            let bytes = &input[span[0] as usize..span[1] as usize];
            match field.kind {
                Type::Scalar(Scalar::Cbor) => {
                    if field.name == "@fields" {
                        crate::cbor::dynamic_fields(bytes, definition)?;
                    } else {
                        CborRef::parse(bytes)?;
                    }
                }
                Type::Scalar(k) => {
                    value::read(k, bytes, false)?;
                }
                Type::Array(k) => {
                    ArrayView::parse(k, bytes)?;
                }
            }
        }
        Ok(length)
    }
}
