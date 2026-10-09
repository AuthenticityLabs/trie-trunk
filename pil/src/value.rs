use crate::{Error, Scalar, array::Array, cbor};

#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Value<'a> {
    Absent,
    Null,
    Bool(bool),
    I8(i8),
    I16(i16),
    I32(i32),
    I64(i64),
    U8(u8),
    U16(u16),
    Ref16(u16),
    U32(u32),
    U64(u64),
    F32(f32),
    F64(f64),
    Uuid([u8; 16]),
    TimestampMs(i64),
    TimestampUs(i64),
    TimestampNs(i64),
    Mapped(u64),
    String(&'a str),
    Bytes(&'a [u8]),
    Cbor(&'a [u8]),
    ValidatedCbor(crate::CborRef<'a>),
    Array(Array<'a>),
}

impl Value<'_> {
    pub fn scalar(self) -> Option<Scalar> {
        Some(match self {
            Self::Bool(_) => Scalar::Bool,
            Self::I8(_) => Scalar::I8,
            Self::I16(_) => Scalar::I16,
            Self::I32(_) => Scalar::I32,
            Self::I64(_) => Scalar::I64,
            Self::U8(_) => Scalar::U8,
            Self::U16(_) => Scalar::U16,
            Self::Ref16(_) => Scalar::Ref16,
            Self::U32(_) => Scalar::U32,
            Self::U64(_) => Scalar::U64,
            Self::F32(_) => Scalar::F32,
            Self::F64(_) => Scalar::F64,
            Self::Uuid(_) => Scalar::Uuid,
            Self::TimestampMs(_) => Scalar::TimestampMs,
            Self::TimestampUs(_) => Scalar::TimestampUs,
            Self::TimestampNs(_) => Scalar::TimestampNs,
            Self::String(_) => Scalar::String,
            Self::Bytes(_) => Scalar::Bytes,
            Self::Cbor(_) | Self::ValidatedCbor(_) => Scalar::Cbor,
            Self::Absent | Self::Null | Self::Mapped(_) | Self::Array(_) => return None,
        })
    }
}

pub(crate) fn write(value: Value<'_>, output: &mut Vec<u8>) -> Result<(), Error> {
    macro_rules! fixed {
        ($($kind:ident),* $(,)?) => {
            match value {
                $(Value::$kind(v) => output.extend_from_slice(&v.to_le_bytes()),)*
                Value::Bool(v) => output.push(u8::from(v)),
                Value::Uuid(v) => output.extend_from_slice(&v),
                Value::String(v) => output.extend_from_slice(v.as_bytes()),
                Value::Bytes(v) => output.extend_from_slice(v),
                Value::Cbor(v) => { crate::CborRef::parse(v)?; output.extend_from_slice(v); },
                Value::ValidatedCbor(v) => output.extend_from_slice(v.as_bytes()),
                _ => return Err(Error::Invalid("expected a scalar value")),
            }
        };
    }
    fixed!(
        I8,
        I16,
        I32,
        I64,
        U8,
        U16,
        Ref16,
        U32,
        U64,
        F32,
        F64,
        TimestampMs,
        TimestampUs,
        TimestampNs
    );
    Ok(())
}

pub(crate) fn read(kind: Scalar, bytes: &[u8], validate_cbor: bool) -> Result<Value<'_>, Error> {
    macro_rules! fixed {
        ($($kind:ident : $native:ty),* $(,)?) => {
            match kind {
                $(Scalar::$kind => Value::$kind(<$native>::from_le_bytes(bytes.try_into().map_err(|_| Error::Invalid("wrong scalar width"))?)),)*
                Scalar::Bool => match bytes {
                    [0] => Value::Bool(false), [1] => Value::Bool(true),
                    _ => return Err(Error::Invalid("invalid BOOL")),
                },
                Scalar::Uuid => Value::Uuid(bytes.try_into().map_err(|_| Error::Invalid("wrong UUID width"))?),
                Scalar::String => Value::String(std::str::from_utf8(bytes).map_err(|_| Error::Invalid("invalid UTF-8"))?),
                Scalar::Bytes => Value::Bytes(bytes),
                Scalar::Cbor => {
                    if validate_cbor { cbor::parse(bytes)?; }
                    Value::Cbor(bytes)
                },
            }
        };
    }
    Ok(
        fixed!(I8:i8, I16:i16, I32:i32, I64:i64, U8:u8, U16:u16, Ref16:u16, U32:u32, U64:u64, F32:f32, F64:f64, TimestampMs:i64, TimestampUs:i64, TimestampNs:i64),
    )
}
