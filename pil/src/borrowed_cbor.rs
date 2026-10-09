//! Validated, borrowed CBOR items for adapters that already store CBOR.
//! Validation walks boundaries and UTF-8 without constructing an object tree.
use crate::{Error, cbor};
use ciborium_ll::{Decoder, Header};

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct CborRef<'a> {
    bytes: &'a [u8],
    header: Header,
    header_len: usize,
}

fn header(input: &mut &[u8]) -> Result<Header, Error> {
    Decoder::from(input)
        .pull()
        .map_err(|_| Error::Invalid("malformed CBOR header"))
}

fn take<'a>(input: &mut &'a [u8], count: usize) -> Result<&'a [u8], Error> {
    let bytes = input.get(..count).ok_or(Error::Incomplete)?;
    *input = &input[count..];
    Ok(bytes)
}

fn scan(input: &mut &[u8], item: Header, depth: usize) -> Result<bool, Error> {
    match item {
        Header::Positive(_) | Header::Negative(_) | Header::Float(_) | Header::Simple(20..=23) => {
            Ok(false)
        }
        Header::Bytes(Some(n)) | Header::Text(Some(n)) => {
            let bytes = take(input, n)?;
            if matches!(item, Header::Text(_)) {
                std::str::from_utf8(bytes).map_err(|_| Error::Invalid("invalid CBOR UTF-8"))?;
            }
            Ok(false)
        }
        Header::Bytes(None) | Header::Text(None) => {
            loop {
                let chunk = header(input)?;
                match (item, chunk) {
                    (_, Header::Break) => break,
                    (Header::Bytes(_), Header::Bytes(Some(_)))
                    | (Header::Text(_), Header::Text(Some(_))) => {
                        scan(input, chunk, depth)?;
                    }
                    _ => return Err(Error::Invalid("invalid CBOR string chunk")),
                }
            }
            Ok(false)
        }
        Header::Array(count) | Header::Map(count) => {
            let depth = depth
                .checked_sub(1)
                .ok_or(Error::Invalid("CBOR nesting limit exceeded"))?;
            let width = if matches!(item, Header::Map(_)) { 2 } else { 1 };
            // Every child takes at least one byte; also bounds malicious counts.
            if count.is_some_and(|n| n > input.len() / width) {
                return Err(Error::Incomplete);
            }
            let mut tags = false;
            let mut remaining = count;
            loop {
                if remaining == Some(0) {
                    break;
                }
                let first = header(input)?;
                if count.is_none() && first == Header::Break {
                    break;
                }
                tags |= scan(input, first, depth)?;
                if width == 2 {
                    let second = header(input)?;
                    tags |= scan(input, second, depth)?;
                }
                if let Some(n) = &mut remaining {
                    *n -= 1;
                }
            }
            Ok(tags)
        }
        Header::Tag(_) => {
            let depth = depth
                .checked_sub(1)
                .ok_or(Error::Invalid("CBOR nesting limit exceeded"))?;
            let next = header(input)?;
            scan(input, next, depth)?;
            Ok(true)
        }
        _ => Err(Error::Invalid("unexpected CBOR break or simple value")),
    }
}

impl<'a> CborRef<'a> {
    pub fn parse(bytes: &'a [u8]) -> Result<Self, Error> {
        let mut input = bytes;
        let value = Self::read(&mut input, 64).map_err(|error| match error {
            Error::Incomplete => Error::Invalid("CBOR exceeds its field boundary"),
            error => error,
        })?;
        if !input.is_empty() {
            return Err(Error::Invalid("CBOR range contains trailing bytes"));
        }
        Ok(value)
    }

    fn read(input: &mut &'a [u8], depth: usize) -> Result<Self, Error> {
        let start = *input;
        let head = header(input)?;
        let header_len = start.len() - input.len();
        let tags = scan(input, head, depth)?;
        let bytes = &start[..start.len() - input.len()];
        // Retain the codec's existing semantic handling of tagged/bignum values.
        if tags {
            cbor::parse(bytes)?;
        }
        Ok(Self {
            bytes,
            header: head,
            header_len,
        })
    }

    pub fn as_bytes(self) -> &'a [u8] {
        self.bytes
    }
    pub fn is_null(self) -> bool {
        matches!(self.header, Header::Simple(22 | 23))
    }
    pub fn unsigned(self) -> Option<u64> {
        if let Header::Positive(n) = self.header {
            Some(n)
        } else {
            None
        }
    }
    pub fn text(self) -> Option<&'a str> {
        if matches!(self.header, Header::Text(Some(_))) {
            // UTF-8 was checked by the constructor.
            std::str::from_utf8(&self.bytes[self.header_len..]).ok()
        } else {
            None
        }
    }
    pub fn bytes(self) -> Option<&'a [u8]> {
        matches!(self.header, Header::Bytes(Some(_))).then_some(&self.bytes[self.header_len..])
    }
}

/// Validates each key and value as it is consumed. Exhaust the iterator to check
/// the complete map, including its final boundary. No child values are allocated.
pub struct CborMap<'a> {
    input: &'a [u8],
    remaining: Option<usize>,
    depth: usize,
    done: bool,
}
impl<'a> CborMap<'a> {
    pub fn new(mut input: &'a [u8], maximum_depth: usize) -> Result<Self, Error> {
        let Header::Map(remaining) = header(&mut input)? else {
            return Err(Error::Invalid("expected CBOR map"));
        };
        if remaining.is_some_and(|n| n > input.len() / 2) {
            return Err(Error::Incomplete);
        }
        Ok(Self {
            input,
            remaining,
            depth: maximum_depth
                .checked_sub(1)
                .ok_or(Error::Invalid("CBOR nesting limit exceeded"))?,
            done: false,
        })
    }
}
impl<'a> Iterator for CborMap<'a> {
    type Item = Result<(CborRef<'a>, CborRef<'a>), Error>;
    fn next(&mut self) -> Option<Self::Item> {
        if self.done {
            return None;
        }
        if self.remaining == Some(0) || self.remaining.is_none() && self.input.first() == Some(&255)
        {
            if self.remaining.is_none() {
                self.input = &self.input[1..];
            }
            self.done = true;
            return (!self.input.is_empty())
                .then_some(Err(Error::Invalid("trailing CBOR map data")));
        }
        let result = (|| {
            let key = CborRef::read(&mut self.input, self.depth)?;
            let value = CborRef::read(&mut self.input, self.depth)?;
            if let Some(n) = &mut self.remaining {
                *n -= 1;
            }
            Ok((key, value))
        })();
        if result.is_err() {
            self.done = true;
        }
        Some(result)
    }
    fn size_hint(&self) -> (usize, Option<usize>) {
        (
            0,
            Some(if self.done {
                0
            } else {
                self.remaining
                    .unwrap_or(self.input.len() / 2)
                    .saturating_add(1)
            }),
        )
    }
}
