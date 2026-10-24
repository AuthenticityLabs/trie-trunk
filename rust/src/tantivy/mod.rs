//! Native secondary search index. Canonical events remain in trie-trunk.
//! Rust owns Tantivy readers/writers, postings, and result buffers.
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use tantivy::{Index, IndexReader, IndexWriter};
type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;

mod handle;
mod lifecycle;
mod native;
mod query;
use lifecycle::run;

#[derive(Default)]
#[cfg_attr(test, derive(Deserialize))]
#[cfg_attr(test, serde(default))]
struct Cell {
    k: usize,
    t: Vec<String>,
    c: Vec<String>,
    e: Vec<String>,
    state: u64,
    u: Vec<u64>,
    i: Vec<i64>,
    f: Vec<f64>,
    b: Vec<bool>,
}
#[derive(Default)]
#[cfg_attr(test, derive(Deserialize))]
struct Doc {
    #[cfg_attr(test, serde(default))]
    id: String,
    #[cfg_attr(test, serde(default))]
    source_row: u64,
    row: u64,
    cells: Vec<Cell>,
}
#[derive(Default)]
struct Filter {
    code: Option<u64>,
    k: usize,
    kind: String,
    op: String,
    value: String,
}
#[derive(Default)]
struct Request {
    op: String,
    path: String,
    binding: String,
    fields: Vec<String>,
    base: u64,
    tail: u64,
    first_ref: u64,
    docs: Vec<Doc>,
    ranges: Vec<(u64, u64)>,
    filters: Vec<Filter>,
    text: String,
    mode: String,
    ids: Vec<String>,
}
#[derive(Default)]
struct Response {
    error: String,
    tail: u64,
    rows: Vec<u64>,
    ids: Vec<String>,
}
#[derive(Default, Serialize, Deserialize)]
struct Checkpoint {
    binding: String,
    base: u64,
    tail: u64,
}
struct Handle {
    index: Index,
    reader: IndexReader,
    writer: IndexWriter,
    checkpoint: Checkpoint,
    used: u64,
    live_ids: HashMap<String, u64>,
    live_names: HashMap<u64, String>,
    next_live: u64,
}

#[cfg(test)]
#[path = "../address_tradeoff_test.rs"]
mod address_tradeoff_test;
#[cfg(test)]
mod tests;
