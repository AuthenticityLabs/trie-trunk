//! Native byte ownership for the Go adapter. Buffers are mutable only while
//! uniquely owned. After sharing, Go may append only to an exclusively leased
//! unused suffix; published spans are immutable. Pins outlive readers.
use std::{
    cell::UnsafeCell,
    ffi::c_void,
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
};
struct Pool {
    limit: u64,
    used: AtomicU64,
    peak: AtomicU64,
}
impl Pool {
    fn reserve(&self, n: u64) -> bool {
        let mut old = self.used.load(Ordering::Relaxed);
        loop {
            let Some(next) = old.checked_add(n) else {
                return false;
            };
            if next > self.limit {
                return false;
            }
            match self
                .used
                .compare_exchange_weak(old, next, Ordering::AcqRel, Ordering::Relaxed)
            {
                Ok(_) => {
                    self.peak.fetch_max(next, Ordering::Relaxed);
                    return true;
                }
                Err(v) => old = v,
            }
        }
    }
}
struct Buffer {
    pool: Arc<Pool>,
    bytes: UnsafeCell<Box<[u8]>>,
}
// SAFETY: the adapter serializes each allocation's writer lease. Sharing freezes
// allocation size and all published spans; only the disjoint unused suffix can
// be written. Readers never inspect that suffix. Release follows the last pin.
unsafe impl Sync for Buffer {}
impl Drop for Buffer {
    fn drop(&mut self) {
        self.pool
            .used
            .fetch_sub(self.bytes.get_mut().len() as u64, Ordering::AcqRel);
    }
}
struct Arena {
    buffers: Mutex<Vec<Arc<Buffer>>>,
}
// Raw handles are boxed Arc values. Cloning creates a new independently owned
// handle. Go serializes mutation of each handle; pins can be dropped concurrently.
#[unsafe(no_mangle)]
pub extern "C" fn trunk_memory_new(limit: u64) -> *mut c_void {
    Box::into_raw(Box::new(Arc::new(Pool {
        limit,
        used: AtomicU64::new(0),
        peak: AtomicU64::new(0),
    })))
    .cast()
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_memory_free(p: *mut c_void) {
    if !p.is_null() {
        drop(unsafe { Box::from_raw(p.cast::<Arc<Pool>>()) })
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_memory_used(p: *const c_void) -> u64 {
    unsafe { &*p.cast::<Arc<Pool>>() }
        .used
        .load(Ordering::Acquire)
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_memory_peak(p: *const c_void) -> u64 {
    unsafe { &*p.cast::<Arc<Pool>>() }
        .peak
        .load(Ordering::Acquire)
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_buffer_new(p: *const c_void) -> *mut c_void {
    let pool = unsafe { &*p.cast::<Arc<Pool>>() }.clone();
    Box::into_raw(Box::new(Arc::new(Buffer {
        pool,
        bytes: UnsafeCell::new(Box::new([])),
    })))
    .cast()
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_buffer_resize(p: *mut c_void, len: usize) -> *mut u8 {
    let handle = unsafe { &mut *p.cast::<Arc<Buffer>>() };
    let Some(b) = Arc::get_mut(handle) else {
        return std::ptr::null_mut();
    };
    let current = b.bytes.get_mut();
    if len <= current.len() {
        return current.as_mut_ptr();
    }
    if !b.pool.reserve(len as u64) {
        return std::ptr::null_mut();
    }
    let mut bytes = Vec::new();
    if bytes.try_reserve_exact(len).is_err() {
        b.pool.used.fetch_sub(len as u64, Ordering::AcqRel);
        return std::ptr::null_mut();
    }
    bytes.resize(len, 0);
    bytes[..current.len()].copy_from_slice(current);
    b.pool
        .used
        .fetch_sub(current.len() as u64, Ordering::AcqRel);
    *current = bytes.into_boxed_slice();
    current.as_mut_ptr()
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_buffer_free(p: *mut c_void) {
    if !p.is_null() {
        drop(unsafe { Box::from_raw(p.cast::<Arc<Buffer>>()) })
    }
}
#[unsafe(no_mangle)]
pub extern "C" fn trunk_arena_new() -> *mut c_void {
    Box::into_raw(Box::new(Arc::new(Arena {
        buffers: Mutex::new(Vec::new()),
    })))
    .cast()
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_arena_pin(p: *const c_void) -> *mut c_void {
    Box::into_raw(Box::new(unsafe { &*p.cast::<Arc<Arena>>() }.clone())).cast()
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_arena_free(p: *mut c_void) {
    if !p.is_null() {
        drop(unsafe { Box::from_raw(p.cast::<Arc<Arena>>()) })
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_arena_adopt(a: *const c_void, b: *const c_void) -> bool {
    let arena = unsafe { &*a.cast::<Arc<Arena>>() };
    let Ok(mut buffers) = arena.buffers.lock() else {
        return false;
    };
    if buffers.try_reserve(1).is_err() {
        return false;
    }
    buffers.push(unsafe { &*b.cast::<Arc<Buffer>>() }.clone());
    true
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn append_only_suffix_survives_pool_and_other_hour_release() {
        unsafe {
            let pool = trunk_memory_new(256);
            let buffer = trunk_buffer_new(pool);
            let bytes = trunk_buffer_resize(buffer, 128);
            *bytes = 41;
            let first = trunk_arena_new();
            let second = trunk_arena_new();
            assert!(trunk_arena_adopt(first, buffer));
            // Publishing a prefix does not forbid a disjoint suffix write.
            *bytes.add(64) = 42;
            assert!(trunk_arena_adopt(second, buffer));
            assert!(trunk_buffer_resize(buffer, 256).is_null());
            trunk_buffer_free(buffer);
            trunk_arena_free(first);
            trunk_memory_free(pool);
            assert_eq!(*bytes, 41);
            assert_eq!(*bytes.add(64), 42);
            trunk_arena_free(second);
        }
    }
    #[test]
    fn pins_budget_and_freezing() {
        unsafe {
            let p = trunk_memory_new(512);
            let b = trunk_buffer_new(p);
            let ptr = trunk_buffer_resize(b, 128);
            assert!(!ptr.is_null());
            *ptr = 42;
            let a = trunk_arena_new();
            assert!(trunk_arena_adopt(a, b));
            assert!(trunk_buffer_resize(b, 256).is_null());
            trunk_buffer_free(b);
            let pin = trunk_arena_pin(a);
            trunk_arena_free(a);
            assert_eq!(*ptr, 42);
            assert_eq!(trunk_memory_used(p), 128);
            trunk_arena_free(pin);
            assert_eq!(trunk_memory_used(p), 0);
            let b = trunk_buffer_new(p);
            assert!(trunk_buffer_resize(b, 513).is_null());
            assert_eq!(trunk_memory_used(p), 0);
            trunk_buffer_free(b);
            trunk_memory_free(p);
        }
    }
}
