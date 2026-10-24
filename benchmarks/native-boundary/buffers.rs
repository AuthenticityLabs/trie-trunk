// Benchmark-only ownership probe, not a production ingress ABI.
// One session has one caller. Returned buffers stay stable until session_free.
struct Session {
    buffers: Vec<Box<[u8]>>,
    bytes: usize,
}
#[unsafe(no_mangle)]
pub extern "C" fn session_new() -> *mut std::ffi::c_void {
    Box::into_raw(Box::new(Session { buffers: Vec::new(), bytes: 0 })).cast()
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn session_buffer(raw: *mut std::ffi::c_void, len: usize) -> *mut u8 {
    if raw.is_null() || len == 0 || len > 16 * 1024 * 1024 { return std::ptr::null_mut(); }
    let s = unsafe { &mut *raw.cast::<Session>() };
    let mut bytes = Vec::new();
    if bytes.try_reserve_exact(len).is_err() { return std::ptr::null_mut(); }
    bytes.resize(len, 0);
    let mut bytes = bytes.into_boxed_slice();
    let ptr = bytes.as_mut_ptr();
    s.bytes += len;
    s.buffers.push(bytes);
    ptr
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn session_bytes(raw: *mut std::ffi::c_void) -> usize {
    if raw.is_null() { 0 } else { unsafe { (*raw.cast::<Session>()).bytes } }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn session_free(raw: *mut std::ffi::c_void) {
    if !raw.is_null() { drop(unsafe { Box::from_raw(raw.cast::<Session>()) }); }
}
// Identical byte verification for all paths, including Go-owned payloads.
// The borrowed pointer is never retained. This is not cryptographic validation.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn payload_sum(ptr: *const u8, len: usize) -> u64 {
    if len == 0 { return 0; }
    unsafe { std::slice::from_raw_parts(ptr, len) }.iter().map(|b| *b as u64).sum()
}
