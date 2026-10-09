use crate::Error;
use std::{fmt, str::FromStr};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Scalar {
    Bool,
    I8,
    I16,
    I32,
    I64,
    U8,
    U16,
    /// Opaque shared-value reference. The binding identifies its target collection.
    Ref16,
    U32,
    U64,
    F32,
    F64,
    Uuid,
    TimestampMs,
    TimestampUs,
    TimestampNs,
    String,
    Bytes,
    Cbor,
}

impl Scalar {
    pub fn width(self) -> Option<usize> {
        match self {
            Self::Bool | Self::I8 | Self::U8 => Some(1),
            Self::I16 | Self::U16 | Self::Ref16 => Some(2),
            Self::I32 | Self::U32 | Self::F32 => Some(4),
            Self::I64
            | Self::U64
            | Self::F64
            | Self::TimestampMs
            | Self::TimestampUs
            | Self::TimestampNs => Some(8),
            Self::Uuid => Some(16),
            Self::String | Self::Bytes | Self::Cbor => None,
        }
    }
}

impl fmt::Display for Scalar {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            Self::Bool => "BOOL",
            Self::I8 => "I8",
            Self::I16 => "I16",
            Self::I32 => "I32",
            Self::I64 => "I64",
            Self::U8 => "U8",
            Self::U16 => "U16",
            Self::Ref16 => "REF16",
            Self::U32 => "U32",
            Self::U64 => "U64",
            Self::F32 => "F32",
            Self::F64 => "F64",
            Self::Uuid => "UUID",
            Self::TimestampMs => "TIMESTAMP_MS",
            Self::TimestampUs => "TIMESTAMP_US",
            Self::TimestampNs => "TIMESTAMP_NS",
            Self::String => "STRING",
            Self::Bytes => "BYTES",
            Self::Cbor => "CBOR",
        })
    }
}

impl FromStr for Scalar {
    type Err = Error;
    fn from_str(text: &str) -> Result<Self, Error> {
        Ok(match text {
            "BOOL" => Self::Bool,
            "I8" => Self::I8,
            "I16" => Self::I16,
            "I32" => Self::I32,
            "I64" => Self::I64,
            "U8" => Self::U8,
            "U16" => Self::U16,
            "REF16" => Self::Ref16,
            "U32" => Self::U32,
            "U64" => Self::U64,
            "F32" => Self::F32,
            "F64" => Self::F64,
            "UUID" => Self::Uuid,
            "TIMESTAMP_MS" => Self::TimestampMs,
            "TIMESTAMP_US" => Self::TimestampUs,
            "TIMESTAMP_NS" => Self::TimestampNs,
            "STRING" => Self::String,
            "BYTES" => Self::Bytes,
            "CBOR" => Self::Cbor,
            _ => return Err(Error::Definition(format!("unsupported scalar type {text}"))),
        })
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Type {
    Scalar(Scalar),
    Array(Scalar),
}

impl From<Scalar> for Type {
    fn from(value: Scalar) -> Self {
        Self::Scalar(value)
    }
}

impl fmt::Display for Type {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Scalar(kind) => kind.fmt(f),
            Self::Array(kind) => write!(f, "ARRAY<{kind}>"),
        }
    }
}

impl FromStr for Type {
    type Err = Error;
    fn from_str(text: &str) -> Result<Self, Error> {
        if let Some(element) = text
            .strip_prefix("ARRAY<")
            .and_then(|s| s.strip_suffix('>'))
        {
            Ok(Self::Array(element.parse()?))
        } else {
            Ok(Self::Scalar(text.parse()?))
        }
    }
}
