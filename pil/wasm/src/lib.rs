//! Small synchronous ABI. Input and index buffers are reusable; JS copies bytes
//! once per network batch, never once per field. All handles belong to this instance.
use pil_codec::{Definition, Error, FrameIndex};
use std::cell::RefCell;
const MAX: usize = 64 * 1024 * 1024;
#[derive(Default)]
struct Codec {
    input: Vec<u8>,
    inflated: Vec<u8>,
    definitions: Vec<Option<Definition>>,
    index: FrameIndex,
    pac: pil_codec::pac::PacIndex,
    error: String,
}
thread_local! { static CODEC: RefCell<Codec> = RefCell::new(Codec::default()); }
#[unsafe(no_mangle)]
pub extern "C" fn input(length: usize) -> *mut u8 {
    CODEC.with_borrow_mut(|c| {
        if length >= u32::MAX as usize {
            c.error = "PIL input exceeds 64 MiB".into();
            return std::ptr::null_mut();
        }
        c.input.resize(length, 0);
        c.input.as_mut_ptr()
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn definition() -> u32 {
    CODEC.with_borrow_mut(|c| match Definition::from_cbor(&c.input) {
        Ok(d) => {
            if let Some(i) = c.definitions.iter().position(Option::is_none) {
                c.definitions[i] = Some(d);
                i as u32
            } else {
                c.definitions.push(Some(d));
                (c.definitions.len() - 1) as u32
            }
        }
        Err(e) => {
            c.error = e.to_string();
            u32::MAX
        }
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn release(handle: usize) {
    CODEC.with_borrow_mut(|c| {
        if let Some(d) = c.definitions.get_mut(handle) {
            *d = None;
        }
    });
}
#[unsafe(no_mangle)]
pub extern "C" fn scan(handle: usize, start: usize, length: usize) -> u32 {
    CODEC.with_borrow_mut(|c| {
        let Codec {
            input,
            definitions,
            index,
            error,
            ..
        } = c;
        let result = definitions
            .get(handle)
            .and_then(Option::as_ref)
            .zip(
                start
                    .checked_add(length)
                    .and_then(|end| input.get(start..end)),
            )
            .ok_or(Error::Invalid("invalid WASM handle or range"))
            .and_then(|(d, b)| index.scan(d, b, MAX));
        match result {
            Ok(n) => n as u32,
            Err(Error::Incomplete) => 0,
            Err(e) => {
                *error = e.to_string();
                u32::MAX
            }
        }
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn spans() -> *const [u32; 2] {
    CODEC.with_borrow(|c| c.index.spans().as_ptr())
}

#[unsafe(no_mangle)]
pub extern "C" fn scan_pac(handle: usize, start: usize, length: usize, native: u32) -> u32 {
    CODEC.with_borrow_mut(|c| {
        let Codec { input, definitions, pac, error, .. } = c;
        let result = (|| {
            let d = definitions.get(handle).and_then(Option::as_ref).ok_or(Error::Invalid("PAC definition handle"))?;
            let b = start.checked_add(length).and_then(|end| input.get(start..end)).ok_or(Error::Invalid("PAC input bounds"))?;
            let digest: [u8;32] = b.get(32..64).ok_or(Error::Incomplete)?.try_into().unwrap();
            let supported = if native == 1 { &[pil_codec::pac::SOURCE_EXTENSION,pil_codec::pac::NATIVE_EXTENSION][..] } else { &[pil_codec::pac::SOURCE_EXTENSION][..] };
            pac.scan(d,b,&digest,supported,u32::MAX as usize-1)
        })();
        match result { Ok(n)=>n as u32, Err(Error::Incomplete)=>0, Err(e)=>{*error=e.to_string();u32::MAX} }
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn pac_members() -> *const [u32;2] {
    CODEC.with_borrow(|c| c.pac.members().as_ptr())
}
#[unsafe(no_mangle)]
pub extern "C" fn error_ptr() -> *const u8 {
    CODEC.with_borrow(|c| c.error.as_ptr())
}
#[unsafe(no_mangle)]
pub extern "C" fn error_len() -> usize {
    CODEC.with_borrow(|c| c.error.len())
}

#[unsafe(no_mangle)]
pub extern "C" fn inflate(length: usize) -> u32 {
    CODEC.with_borrow_mut(|c| {
        match pil_codec::compression::inflate(&c.input, length, &mut c.inflated) {
            Ok(()) => length as u32,
            Err(e) => {
                c.error = e.to_string();
                u32::MAX
            }
        }
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn inflated_ptr() -> *const u8 {
    CODEC.with_borrow(|c| c.inflated.as_ptr())
}
