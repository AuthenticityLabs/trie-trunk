use crate::{Definition, Error, Scalar, Type, Value};

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum AdaptiveIntegers {
    #[default]
    None,
    From16,
    From32,
}
impl AdaptiveIntegers {
    pub fn name(self) -> &'static str {
        match self {
            Self::None => "none",
            Self::From16 => "16",
            Self::From32 => "32",
        }
    }
    pub fn parse(s: &str) -> Result<Self, Error> {
        match s {
            "none" => Ok(Self::None),
            "16" => Ok(Self::From16),
            "32" => Ok(Self::From32),
            _ => Err(Error::Definition(
                "adaptive_integers must be none, 16 or 32".into(),
            )),
        }
    }
}
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub(crate) struct IntegerField {
    pub width: usize,
    pub bits: usize,
    pub signed: bool,
}
impl IntegerField {
    pub fn new(kind: Type, mode: AdaptiveIntegers) -> Self {
        let Type::Scalar(k) = kind else {
            return Self::default();
        };
        let signed = matches!(k, Scalar::I16 | Scalar::I32 | Scalar::I64);
        let eligible = match k {
            Scalar::I16 | Scalar::U16 => mode == AdaptiveIntegers::From16,
            Scalar::I32 | Scalar::U32 | Scalar::I64 | Scalar::U64 => mode != AdaptiveIntegers::None,
            _ => false,
        };
        let width = k.width().unwrap_or(0);
        Self {
            width,
            bits: if eligible {
                if width == 2 { 1 } else { 2 }
            } else {
                0
            },
            signed,
        }
    }
    pub fn code(self, bytes: &[u8]) -> u8 {
        let mut b = [0; 8];
        b[..bytes.len()].copy_from_slice(bytes);
        let mut u = u64::from_le_bytes(b);
        if self.signed {
            let shift = 64 - bytes.len() * 8;
            let s = ((u << shift) as i64) >> shift;
            u = ((s ^ (s >> 63)) as u64) << 1;
        }
        let used = (64 - u.leading_zeros() as usize).div_ceil(8);
        [0, 0, 1, 2, 2, 3, 3, 3, 3][used]
    }
}
pub(crate) fn put(b: &mut [u8], bit: usize, n: usize, code: u8) {
    let i = bit / 8;
    let off = bit % 8;
    if off + n <= 8 {
        b[i] |= code << (8 - off - n)
    } else {
        b[i] |= code >> 1;
        b[i + 1] |= (code & 1) << 7
    }
}
pub(crate) fn get(b: &[u8], bit: usize, n: usize) -> u8 {
    let i = bit / 8;
    let off = bit % 8;
    if off + n <= 8 {
        (b[i] >> (8 - off - n)) & ((1 << n) - 1)
    } else {
        (b[i] & 1) << 1 | b[i + 1] >> 7
    }
}
impl Definition {
    pub(crate) fn width_bits(&self, mask: &[u8]) -> usize {
        self.integer_controls
            .iter()
            .zip(mask)
            .map(|(table, &m)| table[m as usize] as usize)
            .sum()
    }
    pub(crate) fn plane<'a>(
        &self,
        input: &'a [u8],
        mask: &[u8],
        cursor: &mut usize,
    ) -> Result<&'a [u8], Error> {
        let n = self.width_bits(mask);
        let plane = crate::uint::take(input, cursor, n.div_ceil(8))?;
        if n % 8 != 0 && plane[plane.len() - 1] & ((1 << (8 - n % 8)) - 1) != 0 {
            return Err(Error::Invalid("nonzero width padding"));
        };
        Ok(plane)
    }
    pub(crate) fn wire_width(
        &self,
        i: usize,
        native: usize,
        plane: &[u8],
        bit: &mut usize,
    ) -> Result<usize, Error> {
        let f = self.integers[i];
        if f.bits == 0 {
            return Ok(native);
        }
        let width = 1usize << get(plane, *bit, f.bits);
        *bit += f.bits;
        if width > native {
            return Err(Error::Invalid("width exceeds declared type"));
        };
        Ok(width)
    }
    pub(crate) fn integer_value<'a>(&self, i: usize, bytes: &'a [u8]) -> Result<Value<'a>, Error> {
        let Type::Scalar(kind) = self.fields()[i].kind else {
            return Err(Error::Invalid("not scalar"));
        };
        let f = self.integers[i];
        if f.bits == 0 {
            return crate::value::read(kind, bytes, false);
        }
        if bytes.len() > 1 && 1usize << f.code(bytes) != bytes.len() {
            return Err(Error::Invalid("noncanonical integer width"));
        }
        let mut b = if f.signed && bytes[bytes.len() - 1] & 128 != 0 {
            [255; 8]
        } else {
            [0; 8]
        };
        b[..bytes.len()].copy_from_slice(bytes);
        let u = u64::from_le_bytes(b);
        Ok(match kind {
            Scalar::I16 => Value::I16(u as i16),
            Scalar::I32 => Value::I32(u as i32),
            Scalar::I64 => Value::I64(u as i64),
            Scalar::U16 => Value::U16(u as u16),
            Scalar::U32 => Value::U32(u as u32),
            Scalar::U64 => Value::U64(u),
            _ => unreachable!(),
        })
    }
}
