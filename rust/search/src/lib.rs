//! Shared native/WASM search semantics. PIL decoding stays in pill-packer.
use serde::{Deserialize, Serialize};
pub type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;
mod collector;
mod query;
mod schema;
pub use query::{predicate, search, text_query};
pub use schema::{document, schema};
#[derive(Default, Deserialize)]
#[serde(default)]
pub struct Cell {
    pub k: usize,
    pub t: Vec<String>,
    pub c: Vec<String>,
    pub e: Vec<String>,
    pub state: u64,
    pub u: Vec<u64>,
    pub i: Vec<i64>,
    pub f: Vec<f64>,
    pub b: Vec<bool>,
}
#[derive(Default, Clone, Deserialize, Serialize)]
#[serde(default, deny_unknown_fields)]
pub struct Filter {
    pub code: Option<u64>,
    pub k: usize,
    pub kind: String,
    pub op: String,
    pub value: String,
}

pub struct QueryPlan<'a> {
    pub ranges: &'a [(u64, u64)],
    pub filters: &'a [Filter],
    pub text: &'a str,
    pub mode: &'a str,
}

#[cfg(test)]
mod tests;
