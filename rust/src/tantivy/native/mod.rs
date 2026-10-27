//! Typed C ABI. Input views are borrowed for the call; output belongs to Rust
//! until trunk_search_result_free. No Go pointers are retained by the library.
use super::{Cell, Doc, Filter, Request, Response, Result};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};

mod registry;
use registry::execute;

#[repr(C)]
#[derive(Clone, Copy)]
pub struct Text {
    ptr: *const u8,
    len: usize,
}
#[repr(C)]
#[derive(Clone, Copy)]
pub struct Slice<T> {
    ptr: *const T,
    len: usize,
}
#[repr(C)]
pub struct NativeCell {
    k: u64,
    state: u64,
    t: Slice<Text>,
    c: Slice<Text>,
    e: Slice<Text>,
    u: Slice<u64>,
    i: Slice<i64>,
    f: Slice<f64>,
    b: Slice<u8>,
}
#[repr(C)]
pub struct NativeDoc {
    id: Text,
    source_row: u64,
    row: u64,
    cells: Slice<NativeCell>,
}
#[repr(C)]
pub struct NativeFilter {
    k: u64,
    code: u64,
    has_code: u64,
    kind: Text,
    op: Text,
    value: Text,
}
#[repr(C)]
#[derive(Clone, Copy)]
pub struct Range {
    start: u64,
    end: u64,
}
#[repr(C)]
pub struct NativeRequest {
    op: Text,
    path: Text,
    binding: Text,
    text: Text,
    mode: Text,
    base: u64,
    tail: u64,
    first_ref: u64,
    fields: Slice<Text>,
    ids: Slice<Text>,
    docs: Slice<NativeDoc>,
    ranges: Slice<Range>,
    filters: Slice<NativeFilter>,
}
#[repr(C)]
pub struct NativeResponse {
    tail: u64,
    rows: Slice<u64>,
    ids: Slice<Text>,
    error: Text,
}
#[repr(C)]
struct Reply {
    view: NativeResponse,
    owned: Response,
    ids: Vec<Text>,
}

unsafe fn view<'a, T>(s: &Slice<T>) -> Result<&'a [T]> {
    if s.len == 0 {
        return Ok(&[]);
    }
    if s.ptr.is_null() || s.len > 16 << 20 {
        return Err("invalid native input slice".into());
    }
    Ok(unsafe { std::slice::from_raw_parts(s.ptr, s.len) })
}
unsafe fn text(s: &Text) -> Result<String> {
    let bytes = unsafe {
        view(&Slice {
            ptr: s.ptr,
            len: s.len,
        })
    }?;
    Ok(std::str::from_utf8(bytes)?.to_owned())
}
unsafe fn texts(s: &Slice<Text>) -> Result<Vec<String>> {
    unsafe { view(s) }?
        .iter()
        .map(|s| unsafe { text(s) })
        .collect()
}
unsafe fn request(r: &NativeRequest) -> Result<Request> {
    Ok(Request {
        op: unsafe { text(&r.op) }?,
        path: unsafe { text(&r.path) }?,
        binding: unsafe { text(&r.binding) }?,
        text: unsafe { text(&r.text) }?,
        mode: unsafe { text(&r.mode) }?,
        base: r.base,
        tail: r.tail,
        first_ref: r.first_ref,
        fields: unsafe { texts(&r.fields) }?,
        ids: unsafe { texts(&r.ids) }?,
        ranges: unsafe { view(&r.ranges) }?
            .iter()
            .map(|r| (r.start, r.end))
            .collect(),
        filters: unsafe { view(&r.filters) }?
            .iter()
            .map(|f| {
                Ok(Filter {
                    k: f.k as usize,
                    code: if f.has_code != 0 { Some(f.code) } else { None },
                    kind: unsafe { text(&f.kind) }?,
                    op: unsafe { text(&f.op) }?,
                    value: unsafe { text(&f.value) }?,
                })
            })
            .collect::<Result<_>>()?,
        docs: unsafe { view(&r.docs) }?
            .iter()
            .map(|d| {
                Ok(Doc {
                    id: unsafe { text(&d.id) }?,
                    source_row: d.source_row,
                    row: d.row,
                    cells: unsafe { view(&d.cells) }?
                        .iter()
                        .map(|c| {
                            Ok(Cell {
                                k: c.k as usize,
                                state: c.state,
                                t: unsafe { texts(&c.t) }?,
                                c: unsafe { texts(&c.c) }?,
                                e: unsafe { texts(&c.e) }?,
                                u: unsafe { view(&c.u) }?.to_vec(),
                                i: unsafe { view(&c.i) }?.to_vec(),
                                f: unsafe { view(&c.f) }?.to_vec(),
                                b: unsafe { view(&c.b) }?.iter().map(|b| *b != 0).collect(),
                            })
                        })
                        .collect::<Result<_>>()?,
                })
            })
            .collect::<Result<_>>()?,
    })
}
fn text_view(s: &str) -> Text {
    Text {
        ptr: s.as_ptr(),
        len: s.len(),
    }
}
fn reply(r: Response) -> *mut NativeResponse {
    let ids: Vec<_> = r.ids.iter().map(|s| text_view(s)).collect();
    let view = NativeResponse {
        tail: r.tail,
        rows: Slice {
            ptr: r.rows.as_ptr(),
            len: r.rows.len(),
        },
        ids: Slice {
            ptr: ids.as_ptr(),
            len: ids.len(),
        },
        error: text_view(&r.error),
    };
    Box::into_raw(Box::new(Reply {
        view,
        owned: r,
        ids,
    }))
    .cast()
}

pub struct Cancellation(Arc<AtomicBool>);
#[unsafe(no_mangle)]
pub extern "C" fn trunk_search_cancel_new() -> *mut Cancellation {
    Box::into_raw(Box::new(Cancellation(Arc::new(AtomicBool::new(false)))))
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_search_cancel_set(c: *const Cancellation) {
    if let Some(c) = unsafe { c.as_ref() } {
        c.0.store(true, Ordering::Relaxed)
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_search_cancel_free(c: *mut Cancellation) {
    if !c.is_null() {
        drop(unsafe { Box::from_raw(c) })
    }
}
fn check(c: Option<&Arc<AtomicBool>>) -> Result<()> {
    if c.is_some_and(|c| c.load(Ordering::Relaxed)) {
        Err("search canceled".into())
    } else {
        Ok(())
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_search_execute(
    r: *const NativeRequest,
    c: *const Cancellation,
) -> *mut NativeResponse {
    let outcome = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| -> Result<Response> {
        let r = unsafe { r.as_ref() }.ok_or("null search request")?;
        let flag = unsafe { c.as_ref() }.map(|c| &c.0);
        check(flag)?;
        execute(unsafe { request(r) }?, flag)
    }));
    match outcome {
        Ok(Ok(r)) => reply(r),
        Ok(Err(e)) => reply(Response {
            error: e.to_string(),
            ..Default::default()
        }),
        Err(_) => reply(Response {
            error: "native search panicked; source may require reopen".into(),
            ..Default::default()
        }),
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_search_result_free(r: *mut NativeResponse) {
    if !r.is_null() {
        drop(unsafe { Box::from_raw(r.cast::<Reply>()) })
    }
}
