use crate::{Files, Result, Store};
use std::cell::RefCell;
#[link(wasm_import_module = "files")]
unsafe extern "C" {
    fn size(file: u32) -> f64;
    fn read(file: u32, at: f64, ptr: *mut u8, len: usize) -> i32;
    fn write(file: u32, at: f64, ptr: *const u8, len: usize) -> i32;
    fn truncate(file: u32, len: f64) -> i32;
    fn flush(file: u32) -> i32;
}
struct Host;
fn okay(n: i32) -> Result<()> {
    if n == 0 {
        Ok(())
    } else {
        Err("browser file I/O failed".into())
    }
}
impl Files for Host {
    fn size(&self, id: u32) -> Result<u64> {
        let n = unsafe { size(id) };
        if n.is_finite() && n >= 0.0 && n <= 9_007_199_254_740_991.0 {
            Ok(n as u64)
        } else {
            Err("invalid file size".into())
        }
    }
    fn read(&self, id: u32, at: u64, out: &mut [u8]) -> Result<()> {
        okay(unsafe { read(id, at as f64, out.as_mut_ptr(), out.len()) })
    }
    fn write(&mut self, id: u32, at: u64, b: &[u8]) -> Result<()> {
        okay(unsafe { write(id, at as f64, b.as_ptr(), b.len()) })
    }
    fn truncate(&mut self, id: u32, len: u64) -> Result<()> {
        okay(unsafe { truncate(id, len as f64) })
    }
    fn flush(&mut self, id: u32) -> Result<()> {
        okay(unsafe { flush(id) })
    }
}
#[derive(Default)]
struct State {
    input: Vec<u8>,
    output: Vec<u8>,
    error: String,
    stores: Vec<Option<Store>>,
}
thread_local! {static STATE:RefCell<State>=RefCell::new(State::default());}
fn run(action: impl FnOnce(&mut State) -> Result<u32>) -> u32 {
    STATE.with_borrow_mut(|s| match action(s) {
        Ok(v) => v,
        Err(e) => {
            s.error = e;
            u32::MAX
        }
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn input(len: usize) -> *mut u8 {
    STATE.with_borrow_mut(|s| {
        if len > 64 * 1024 * 1024 {
            s.error = "storage batch exceeds 64 MiB".into();
            return std::ptr::null_mut();
        }
        s.input.resize(len, 0);
        s.input.as_mut_ptr()
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn open(day: f64, schema: usize) -> u32 {
    run(|s| {
        if !day.is_finite() || day < 0.0 || day.fract() != 0.0 {
            return Err("invalid day".into());
        }
        let map = s.input.get(..schema).ok_or("invalid schema range")?;
        let pil = &s.input[schema..];
        let store = Store::open(map, pil, day as u64, &mut Host)?;
        if let Some(i) = s.stores.iter().position(Option::is_none) {
            s.stores[i] = Some(store);
            Ok(i as u32)
        } else {
            s.stores.push(Some(store));
            Ok((s.stores.len() - 1) as u32)
        }
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn refresh(id: usize) -> u32 {
    run(|s| {
        s.stores
            .get_mut(id)
            .and_then(Option::as_mut)
            .ok_or("invalid store")?
            .reload(&mut Host)?;
        Ok(0)
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn put(id: usize, start: usize, len: usize) -> u32 {
    run(|s| {
        let b = s
            .input
            .get(start..start.checked_add(len).ok_or("invalid range")?)
            .ok_or("invalid range")?;
        s.stores
            .get_mut(id)
            .and_then(Option::as_mut)
            .ok_or("invalid store")?
            .put(b)?;
        Ok(0)
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn commit(id: usize) -> u32 {
    run(|s| {
        let refs = s
            .stores
            .get_mut(id)
            .and_then(Option::as_mut)
            .ok_or("invalid store")?
            .commit(&mut Host)?;
        s.output = refs;
        Ok(s.output.len() as u32)
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn get(id: usize) -> u32 {
    run(|s| {
        s.output = s
            .stores
            .get_mut(id)
            .and_then(Option::as_mut)
            .ok_or("invalid store")?
            .get(&s.input, &Host)?;
        Ok(s.output.len() as u32)
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn lineage(id: usize, leaf: u32) -> u32 {
    run(|s| {
        s.output = s
            .stores
            .get(id)
            .and_then(Option::as_ref)
            .ok_or("invalid store")?
            .lineage(leaf)?;
        Ok(s.output.len() as u32)
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn close(id: usize) {
    STATE.with_borrow_mut(|s| {
        if let Some(v) = s.stores.get_mut(id) {
            *v = None;
        }
    })
}
#[unsafe(no_mangle)]
pub extern "C" fn checksum() -> u32 {
    STATE.with_borrow(|s| crc32c::crc32c(&s.input))
}
#[unsafe(no_mangle)]
pub extern "C" fn output() -> *const u8 {
    STATE.with_borrow(|s| s.output.as_ptr())
}
#[unsafe(no_mangle)]
pub extern "C" fn error_ptr() -> *const u8 {
    STATE.with_borrow(|s| s.error.as_ptr())
}
#[unsafe(no_mangle)]
pub extern "C" fn error_len() -> usize {
    STATE.with_borrow(|s| s.error.len())
}
