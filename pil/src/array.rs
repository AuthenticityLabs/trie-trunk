use crate::{Error, Scalar, Value, cbor, uint, value};

#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Array<'a> {
    Items(&'a [Value<'a>]),
    Encoded(ArrayView<'a>),
}

impl Array<'_> {
    pub fn len(self) -> usize {
        match self {
            Self::Items(items) => items.len(),
            Self::Encoded(view) => view.len(),
        }
    }
    pub fn is_empty(self) -> bool {
        self.len() == 0
    }
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct ArrayView<'a> {
    kind: Scalar,
    count: usize,
    bytes: &'a [u8],
    offsets_start: usize,
    values_start: usize,
}

impl<'a> ArrayView<'a> {
    pub fn parse(kind: Scalar, bytes: &'a [u8]) -> Result<Self, Error> {
        Self::parse_inner(kind, bytes).map_err(|e| {
            if e == Error::Incomplete {
                Error::Invalid("array exceeds its field boundary")
            } else {
                e
            }
        })
    }

    fn parse_inner(kind: Scalar, bytes: &'a [u8]) -> Result<Self, Error> {
        let mut cursor = 0;
        let count =
            usize::try_from(uint::read(bytes, &mut cursor)?).map_err(|_| Error::Overflow)?;
        let offsets_start = cursor;
        if let Some(width) = kind.width() {
            let size = count.checked_mul(width).ok_or(Error::Overflow)?;
            if cursor.checked_add(size).ok_or(Error::Overflow)? != bytes.len() {
                return Err(Error::Invalid(
                    "fixed array size does not match field boundary",
                ));
            }
        } else {
            let minimum = count.checked_mul(2).ok_or(Error::Overflow)?;
            if minimum > bytes.len() - cursor {
                return Err(Error::Invalid("array offset table exceeds field boundary"));
            }
            let mut previous = 0;
            for _ in 0..count {
                let end = uint::read(bytes, &mut cursor)?;
                if end < previous {
                    return Err(Error::Invalid("decreasing array end offsets"));
                }
                previous = end;
            }
            if usize::try_from(previous).map_err(|_| Error::Overflow)? != bytes.len() - cursor {
                return Err(Error::Invalid(
                    "array payload size does not match final end offset",
                ));
            }
        }
        let view = Self {
            kind,
            count,
            bytes,
            offsets_start,
            values_start: cursor,
        };
        for element in view.iter() {
            if let Value::Cbor(bytes) = element?
                && matches!(cbor::parse(bytes)?, ciborium::Value::Null)
            {
                return Err(Error::Invalid("null array element"));
            }
        }
        Ok(view)
    }

    pub fn len(self) -> usize {
        self.count
    }
    pub fn is_empty(self) -> bool {
        self.count == 0
    }
    pub fn element_type(self) -> Scalar {
        self.kind
    }
    pub fn as_bytes(self) -> &'a [u8] {
        self.bytes
    }
    pub fn iter(self) -> ArrayIter<'a> {
        ArrayIter {
            view: self,
            remaining: self.count,
            cursor: self.offsets_start,
            previous: 0,
        }
    }
}

pub struct ArrayIter<'a> {
    view: ArrayView<'a>,
    remaining: usize,
    cursor: usize,
    previous: usize,
}

impl<'a> Iterator for ArrayIter<'a> {
    type Item = Result<Value<'a>, Error>;
    fn next(&mut self) -> Option<Self::Item> {
        if self.remaining == 0 {
            return None;
        }
        self.remaining -= 1;
        Some((|| {
            let end = if let Some(width) = self.view.kind.width() {
                self.previous.checked_add(width).ok_or(Error::Overflow)?
            } else {
                usize::try_from(uint::read(self.view.bytes, &mut self.cursor)?)
                    .map_err(|_| Error::Overflow)?
            };
            let payload = &self.view.bytes[self.view.values_start..];
            let bytes = payload
                .get(self.previous..end)
                .ok_or(Error::Invalid("array element outside payload"))?;
            self.previous = end;
            value::read(self.view.kind, bytes, false)
        })())
    }
    fn size_hint(&self) -> (usize, Option<usize>) {
        (self.remaining, Some(self.remaining))
    }
}

impl ExactSizeIterator for ArrayIter<'_> {}

pub(crate) fn encode(kind: Scalar, array: Array<'_>, output: &mut Vec<u8>) -> Result<(), Error> {
    let Array::Items(items) = array else {
        let Array::Encoded(view) = array else {
            unreachable!()
        };
        if view.kind != kind {
            return Err(Error::Invalid("array element type mismatch"));
        }
        output.extend_from_slice(view.bytes);
        return Ok(());
    };
    uint::write(items.len() as u64, output);
    let mut payload = Vec::new();
    for &item in items {
        if item.scalar() != Some(kind) {
            return Err(Error::Invalid(
                "array element type mismatch or null element",
            ));
        }
        if kind.width().is_some() {
            value::write(item, output)?;
        } else {
            if let Value::ValidatedCbor(value) = item {
                if value.is_null() {
                    return Err(Error::Invalid("null array element"));
                }
                payload.extend_from_slice(value.as_bytes());
            } else if let Value::Cbor(bytes) = item {
                if matches!(cbor::parse(bytes)?, ciborium::Value::Null) {
                    return Err(Error::Invalid("null array element"));
                }
                payload.extend_from_slice(bytes);
            } else {
                value::write(item, &mut payload)?;
            }
            uint::write(payload.len() as u64, output);
        }
    }
    output.extend_from_slice(&payload);
    Ok(())
}
