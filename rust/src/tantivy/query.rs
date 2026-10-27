//! The native and replicated WASM readers execute the same query implementation.
use super::{Handle, Request, Result};
#[cfg(test)]
pub(super) use trunk_search_core::predicate;
impl Handle {
    pub(super) fn search(&self, r: &Request) -> Result<Vec<u64>> {
        self.search_cancel(r, None)
    }
    pub(super) fn search_cancel(
        &self,
        r: &Request,
        cancel: Option<std::sync::Arc<std::sync::atomic::AtomicBool>>,
    ) -> Result<Vec<u64>> {
        trunk_search_core::search(
            &self.index,
            &self.reader,
            &trunk_search_core::QueryPlan {
                ranges: &r.ranges,
                filters: &r.filters,
                text: &r.text,
                mode: &r.mode,
            },
            cancel,
        )
    }
}
