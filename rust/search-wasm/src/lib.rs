//! Read-only Tantivy over committed, immutable replicated files.
//! Run in a dedicated worker with OPFS synchronous access handles.
use js_sys::{Function, Uint8Array};
use serde::Deserialize;
use std::{cell::RefCell, collections::HashMap, io};
use tantivy::{Index, IndexReader, ReloadPolicy};
use wasm_bindgen::prelude::*;
mod directory;
type Result<T> = trunk_search_core::Result<T>;
#[wasm_bindgen]
extern "C" {
    #[wasm_bindgen(js_namespace = console, js_name = error)]
    fn log_error(message: &str);
}

thread_local! {
    static HOSTS: RefCell<(u32, HashMap<u32, Function>)> = RefCell::new((0, HashMap::new()));
}
fn read(context: u32, file: u32, at: usize, bytes: &mut [u8]) -> io::Result<()> {
    if bytes.is_empty() {
        return Ok(());
    }
    let host = HOSTS
        .with(|h| h.borrow().1.get(&context).cloned())
        .ok_or_else(|| io::Error::other("Closed index reader"))?;
    // The callback must synchronously fill this view, never retain it or
    // re-enter WASM. This avoids an extra copy for every posting-list read.
    let view = unsafe { Uint8Array::view(bytes) };
    let count = host
        .call3(&JsValue::NULL, &file.into(), &(at as f64).into(), &view)
        .map_err(|_| io::Error::other("Index file read failed"))?;
    if count.as_f64() != Some(bytes.len() as f64) {
        return Err(io::Error::new(
            io::ErrorKind::UnexpectedEof,
            "Short index file read",
        ));
    }
    Ok(())
}

#[derive(Deserialize)]
#[serde(default, deny_unknown_fields)]
struct Query {
    ranges: Vec<(u64, u64)>,
    filters: Vec<trunk_search_core::Filter>,
    text: String,
    mode: String,
}
impl Default for Query {
    fn default() -> Self {
        Self {
            ranges: vec![],
            filters: vec![],
            text: String::new(),
            mode: "contains".into(),
        }
    }
}
fn js_error(e: impl ToString) -> JsValue {
    JsValue::from_str(&e.to_string())
}

#[wasm_bindgen]
pub struct SearchIndex {
    index: Index,
    reader: IndexReader,
    context: u32,
}
impl Drop for SearchIndex {
    fn drop(&mut self) {
        HOSTS.with(|h| {
            h.borrow_mut().1.remove(&self.context);
        });
    }
}
#[wasm_bindgen]
impl SearchIndex {
    /// Files must belong to one committed hour, definition, and generation.
    /// Caller verifies the replication manifest before exposing these files.
    #[wasm_bindgen(constructor)]
    pub fn new(files: &str, read_file: Function) -> std::result::Result<SearchIndex, JsValue> {
        std::panic::set_hook(Box::new(|info| log_error(&info.to_string())));
        let specs = serde_json::from_str(files).map_err(js_error)?;
        let context = HOSTS
            .with(|h| -> Result<u32> {
                let mut h = h.borrow_mut();
                h.0 = h.0.checked_add(1).ok_or("Index handle exhausted")?;
                let id = h.0;
                h.1.insert(id, read_file);
                Ok(id)
            })
            .map_err(js_error)?;
        let opened = (|| -> Result<Self> {
            let directory = directory::ReadOnlyDirectory::new(context, specs)?;
            let index = Index::open(directory)?;
            let reader = index
                .reader_builder()
                .reload_policy(ReloadPolicy::Manual)
                .try_into()?;
            Ok(Self {
                index,
                reader,
                context,
            })
        })();
        if opened.is_err() {
            HOSTS.with(|h| {
                h.borrow_mut().1.remove(&context);
            });
        }
        opened.map_err(js_error)
    }
    pub fn search(&self, json: &str) -> std::result::Result<Vec<u64>, JsValue> {
        if json.len() > 1 << 20 {
            return Err(js_error("Query too large"));
        }
        let q: Query = serde_json::from_str(json).map_err(js_error)?;
        if q.filters.len() > 32 || q.text.len() > 8192 || q.ranges.iter().any(|(a, b)| a >= b) {
            return Err(js_error("Invalid query bounds"));
        }
        trunk_search_core::search(
            &self.index,
            &self.reader,
            &trunk_search_core::QueryPlan {
                ranges: &q.ranges,
                filters: &q.filters,
                text: &q.text,
                mode: &q.mode,
            },
            None,
        )
        .map_err(js_error)
    }
    pub fn documents(&self) -> u64 {
        self.reader.searcher().num_docs()
    }
}
