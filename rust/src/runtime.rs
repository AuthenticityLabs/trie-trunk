//! Request-local cancellation for the resident reader. Offline searches need no runtime.
use std::cell::RefCell;
use std::io;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
thread_local! { static CANCEL: RefCell<Option<Arc<AtomicBool>>> = const { RefCell::new(None) }; }
pub fn install(cancel: Arc<AtomicBool>) {
    crate::index_cache::expire();
    CANCEL.with(|c| *c.borrow_mut() = Some(cancel));
}
pub fn clear() {
    CANCEL.with(|c| *c.borrow_mut() = None);
}
pub fn resident() -> bool {
    CANCEL.with(|c| c.borrow().is_some())
}
pub fn check() -> io::Result<()> {
    if CANCEL.with(|c| {
        c.borrow()
            .as_ref()
            .is_some_and(|c| c.load(Ordering::Relaxed))
    }) {
        Err(io::Error::new(
            io::ErrorKind::Interrupted,
            "search canceled",
        ))
    } else {
        Ok(())
    }
}

pub fn cancellation() -> Option<Arc<AtomicBool>> {
    CANCEL.with(|c| c.borrow().clone())
}
pub fn parallelism() -> usize {
    std::env::var("TRUNK_SEARCH_WORKERS")
        .ok()
        .and_then(|n| n.parse().ok())
        .filter(|n| (1..=8).contains(n))
        .unwrap_or(4)
}
