use crate::{Array, ArrayView, Definition, Error, Type, Value, array, cbor, uint, value};

pub fn encode(definition: &Definition, values: &[Value<'_>]) -> Result<Vec<u8>, Error> {
    let mut encoder = FrameEncoder::default();
    encoder.encode(definition, values)?;
    Ok(encoder.frame)
}

/// Reusable frame buffers. Returned bytes remain valid until the next encode.
/// The wire layout is identical to `encode`; adapters may supply one field at a
/// time in Definition order without allocating an intermediate value vector.
#[derive(Default)]
pub struct FrameEncoder {
    frame: Vec<u8>,
    direct: Vec<u8>,
    offsets: Vec<u8>,
    variable: Vec<u8>,
}
impl FrameEncoder {
    pub fn encode<'a>(
        &mut self,
        definition: &Definition,
        values: &[Value<'a>],
    ) -> Result<&[u8], Error> {
        if values.len() != definition.fields().len() {
            return Err(Error::Invalid("one state is required per Definition field"));
        }
        self.encode_with(definition, |index| Ok(values[index]))
    }

    pub fn encode_with<'a>(
        &mut self,
        definition: &Definition,
        mut value_at: impl FnMut(usize) -> Result<Value<'a>, Error>,
    ) -> Result<&[u8], Error> {
        self.frame.clear();
        self.frame.resize(definition.fields().len().div_ceil(4), 0);
        self.direct.clear();
        self.offsets.clear();
        self.variable.clear();
        let Self {
            frame: mask,
            direct,
            offsets,
            variable,
        } = self;
        for (index, field) in definition.fields().iter().enumerate() {
            let value = value_at(index)?;
            let state = match value {
                Value::Absent => 0,
                Value::Null => {
                    if field.name == "@fields" {
                        return Err(Error::Invalid("@fields cannot be NULL"));
                    }
                    1
                }
                Value::Mapped(reference) => {
                    let width = field
                        .reference_width()
                        .ok_or(Error::Invalid("field has no mapped values"))?;
                    if reference >= field.values.len() as u64 {
                        return Err(Error::Invalid("mapped reference is outside dictionary"));
                    }
                    direct.extend_from_slice(&reference.to_le_bytes()[..width]);
                    2
                }
                Value::Array(array) => {
                    let Type::Array(kind) = field.kind else {
                        return Err(Error::Invalid("array supplied to scalar field"));
                    };
                    array::encode(kind, array, variable)?;
                    uint::write(variable.len() as u64, offsets);
                    3
                }
                scalar => {
                    let Type::Scalar(kind) = field.kind else {
                        return Err(Error::Invalid("scalar supplied to array field"));
                    };
                    if scalar.scalar() != Some(kind) {
                        return Err(Error::Invalid("field value type mismatch"));
                    }
                    if kind.width().is_some() {
                        value::write(scalar, direct)?;
                        2
                    } else {
                        if field.name == "@fields" {
                            let bytes = match scalar {
                                Value::Cbor(bytes) => bytes,
                                Value::ValidatedCbor(value) => value.as_bytes(),
                                _ => unreachable!(),
                            };
                            cbor::dynamic_fields(bytes, definition)?;
                            variable.extend_from_slice(bytes);
                        } else {
                            value::write(scalar, variable)?;
                        }
                        uint::write(variable.len() as u64, offsets);
                        3
                    }
                }
            };
            mask[index / 4] |= state << (6 - 2 * (index % 4));
        }
        mask.extend_from_slice(direct);
        mask.extend_from_slice(offsets);
        mask.extend_from_slice(variable);
        Ok(mask)
    }
}

#[derive(Debug)]
pub struct Frame<'a> {
    definition: &'a Definition,
    values: Vec<Value<'a>>,
    bytes: &'a [u8],
    prefix_len: usize,
}

impl<'a> Frame<'a> {
    pub fn definition(&self) -> &'a Definition {
        self.definition
    }
    pub fn values(&self) -> &[Value<'a>] {
        &self.values
    }
    pub fn as_bytes(&self) -> &'a [u8] {
        self.bytes
    }
    pub fn consumed(&self) -> usize {
        self.bytes.len()
    }
    pub fn prefix_len(&self) -> usize {
        self.prefix_len
    }
    pub fn get(&self, name: &str) -> Option<Value<'a>> {
        let index = self
            .definition
            .fields()
            .iter()
            .position(|field| field.name == name)?;
        Some(self.values[index])
    }
    pub fn string(&self, index: usize) -> Option<&'a str> {
        match self.values.get(index)? {
            Value::String(value) => Some(value),
            Value::Mapped(reference) => self
                .definition
                .fields()
                .get(index)?
                .values
                .get(usize::try_from(*reference).ok()?)
                .map(String::as_str),
            _ => None,
        }
    }
}

fn state(mask: &[u8], index: usize) -> u8 {
    (mask[index / 4] >> (6 - 2 * (index % 4))) & 3
}

pub fn decode<'a>(definition: &'a Definition, input: &'a [u8]) -> Result<Frame<'a>, Error> {
    let count = definition.fields().len();
    let mut cursor = 0;
    let mask = uint::take(input, &mut cursor, count.div_ceil(4))?;
    if !count.is_multiple_of(4) {
        let unused = (4 - count % 4) * 2;
        if mask[mask.len() - 1] & ((1_u8 << unused) - 1) != 0 {
            return Err(Error::Invalid("nonzero unused mask bits"));
        }
    }
    let mut values = vec![Value::Absent; count];
    for (index, field) in definition.fields().iter().enumerate() {
        match state(mask, index) {
            0 => {}
            1 => {
                if field.name == "@fields" {
                    return Err(Error::Invalid("@fields cannot be NULL"));
                }
                values[index] = Value::Null;
            }
            2 => {
                if let Some(width) = field.reference_width() {
                    let bytes = uint::take(input, &mut cursor, width)?;
                    let mut number = [0; 8];
                    number[..width].copy_from_slice(bytes);
                    let reference = u64::from_le_bytes(number);
                    if reference >= field.values.len() as u64 {
                        return Err(Error::Invalid("mapped reference is outside dictionary"));
                    }
                    values[index] = Value::Mapped(reference);
                } else {
                    let Type::Scalar(kind) = field.kind else {
                        return Err(Error::Invalid("DIRECT array"));
                    };
                    let width = kind
                        .width()
                        .ok_or(Error::Invalid("DIRECT variable field without dictionary"))?;
                    values[index] =
                        value::read(kind, uint::take(input, &mut cursor, width)?, false)?;
                }
            }
            3 => {
                if let Type::Scalar(kind) = field.kind
                    && kind.width().is_some()
                {
                    return Err(Error::Invalid("OFFSET fixed-width field"));
                }
            }
            _ => unreachable!(),
        }
    }
    let mut ends = Vec::new();
    let mut previous = 0;
    for index in 0..count {
        if state(mask, index) == 3 {
            let end =
                usize::try_from(uint::read(input, &mut cursor)?).map_err(|_| Error::Overflow)?;
            if end < previous {
                return Err(Error::Invalid("decreasing field end offsets"));
            }
            ends.push((index, end));
            previous = end;
        }
    }
    let prefix_len = cursor;
    let variable = uint::take(input, &mut cursor, previous)?;
    let mut start = 0;
    for (index, end) in ends {
        let field = &definition.fields()[index];
        let bytes = &variable[start..end];
        values[index] = match field.kind {
            Type::Scalar(kind) => {
                if field.name == "@fields" {
                    cbor::dynamic_fields(bytes, definition)?;
                    value::read(kind, bytes, false)?
                } else {
                    value::read(kind, bytes, true)?
                }
            }
            Type::Array(kind) => Value::Array(Array::Encoded(ArrayView::parse(kind, bytes)?)),
        };
        start = end;
    }
    Ok(Frame {
        definition,
        values,
        bytes: &input[..cursor],
        prefix_len,
    })
}

pub fn decode_exact<'a>(definition: &'a Definition, input: &'a [u8]) -> Result<Frame<'a>, Error> {
    let frame = decode(definition, input)?;
    if frame.consumed() != input.len() {
        return Err(Error::Invalid("trailing data after PIL frame"));
    }
    Ok(frame)
}

pub struct Frames<'a> {
    definition: &'a Definition,
    remaining: &'a [u8],
}

impl<'a> Frames<'a> {
    pub fn new(definition: &'a Definition, bytes: &'a [u8]) -> Self {
        Self {
            definition,
            remaining: bytes,
        }
    }
}

impl<'a> Iterator for Frames<'a> {
    type Item = Result<Frame<'a>, Error>;
    fn next(&mut self) -> Option<Self::Item> {
        if self.remaining.is_empty() {
            return None;
        }
        let result = decode(self.definition, self.remaining);
        match &result {
            Ok(frame) => self.remaining = &self.remaining[frame.consumed()..],
            Err(_) => self.remaining = &[],
        }
        Some(result)
    }
}
