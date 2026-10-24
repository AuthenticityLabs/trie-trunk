mod array;
mod cbor;
mod definition;
mod error;
mod frame;
mod types;
mod uint;
mod value;

pub use array::{Array, ArrayIter, ArrayView};
pub use definition::{Definition, Field};
pub use error::Error;
pub use frame::{Frame, FrameEncoder, Frames, decode, decode_exact, encode};
pub use types::{Scalar, Type};
pub use value::Value;

pub fn encode_extended(value: u64, output: &mut Vec<u8>) {
    uint::write(value, output);
}

pub fn decode_extended(input: &[u8]) -> Result<(u64, usize), Error> {
    let mut cursor = 0;
    let value = uint::read(input, &mut cursor)?;
    Ok((value, cursor))
}

mod stream;
pub use stream::read_frame_bytes;

mod borrowed_cbor;
pub use borrowed_cbor::{CborMap, CborRef};
mod index;
pub use index::{ABSENT, FrameIndex, NULL};
pub mod pac;

mod adaptive;
pub use adaptive::AdaptiveIntegers;

#[cfg(feature = "gzip")]
pub mod compression;
