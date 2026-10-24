//! Schema, durable checkpoints, and ingestion. No original payload is STORED.
use super::{Checkpoint, Doc, Handle, Request, Result};
use std::{collections::HashMap, ops::Bound, path::Path};
use tantivy::query::RangeQuery;
use tantivy::schema::{FAST, INDEXED, STRING, Schema, TEXT};
use tantivy::{Index, ReloadPolicy, TantivyDocument, Term};

pub(super) fn schema(kinds: &[String]) -> Schema {
    let mut b = Schema::builder();
    b.add_u64_field("r", INDEXED);
    b.add_u64_field("rh", FAST);
    b.add_u64_field("rn", FAST);
    for (k, kind) in kinds.iter().enumerate() {
        b.add_u64_field(&format!("p{k}"), INDEXED);
        if matches!(kind.as_str(), "s" | "m" | "j") {
            b.add_text_field(&format!("c{k}"), STRING);
        }
        match kind.as_str() {
            "e" => {
                b.add_text_field(&format!("e{k}"), STRING);
            }
            "s" => {
                b.add_text_field(&format!("t{k}"), TEXT);
                b.add_text_field(&format!("e{k}"), STRING);
            }
            "m" => {
                b.add_text_field(&format!("e{k}"), STRING);
                b.add_text_field(&format!("t{k}"), TEXT);
                b.add_u64_field(&format!("u{k}"), INDEXED);
            }
            "u" => {
                b.add_u64_field(&format!("u{k}"), INDEXED | FAST);
            }
            "i" => {
                b.add_i64_field(&format!("i{k}"), INDEXED | FAST);
            }
            "f" => {
                b.add_f64_field(&format!("f{k}"), INDEXED | FAST);
            }
            "b" => {
                b.add_bool_field(&format!("b{k}"), INDEXED);
            }
            "j" => {
                b.add_text_field(&format!("t{k}"), TEXT);
            }
            _ => {}
        }
    }
    b.build()
}
impl Handle {
    pub(super) fn open(r: &Request, memory: bool) -> Result<Self> {
        let schema = schema(&r.fields);
        let index = if memory {
            Index::create_in_ram(schema.clone())
        } else {
            std::fs::create_dir_all(&r.path)?;
            if Path::new(&r.path).join("meta.json").exists() {
                Index::open_in_dir(&r.path)?
            } else {
                Index::create_in_dir(&r.path, schema.clone())?
            }
        };
        if index.schema() != schema {
            return Err("search schema differs; remove disposable search index and rebuild".into());
        }
        let reader = index
            .reader_builder()
            .reload_policy(ReloadPolicy::Manual)
            .try_into()?;
        // One indexing thread and a bounded writer budget per cached source.
        let writer = index.writer_with_num_threads(1, 15_000_000)?;
        let checkpoint = index
            .load_metas()?
            .payload
            .as_deref()
            .map(serde_json::from_str)
            .transpose()?
            .unwrap_or_default();
        let mut h = Self {
            index,
            reader,
            writer,
            checkpoint,
            used: 0,
            live_ids: HashMap::new(),
            live_names: HashMap::new(),
            next_live: 0,
        };
        if h.checkpoint.binding == r.binding && r.base < h.checkpoint.base {
            return Err("retained source changed; restart search".into());
        }
        if h.checkpoint.binding != r.binding || h.checkpoint.tail < r.base {
            h.writer.delete_all_documents()?;
            h.checkpoint = Checkpoint {
                binding: r.binding.clone(),
                base: r.base,
                tail: r.base,
            };
            h.commit()?;
        } else if h.checkpoint.base < r.base {
            let row = h.index.schema().get_field("r")?;
            h.writer.delete_query(Box::new(RangeQuery::new(
                Bound::Included(Term::from_field_u64(row, 0)),
                Bound::Excluded(Term::from_field_u64(row, r.first_ref)),
            )))?;
            h.checkpoint.base = r.base;
            h.commit()?;
        }
        Ok(h)
    }
    pub(super) fn commit(&mut self) -> Result<()> {
        let payload = serde_json::to_string(&self.checkpoint)?;
        let mut commit = self.writer.prepare_commit()?;
        commit.set_payload(&payload);
        commit.commit()?;
        self.reader.reload()?;
        Ok(())
    }
    pub(super) fn append(&mut self, docs: &[Doc], tail: u64, contiguous: bool) -> Result<()> {
        if contiguous
            && (docs
                .first()
                .is_none_or(|d| d.source_row < self.checkpoint.tail)
                || docs.last().map(|d| d.source_row + 1) != Some(tail)
                || docs.windows(2).any(|w| w[1].source_row <= w[0].source_row))
        {
            return Err("noncontiguous index append".into());
        }
        let schema = self.index.schema();
        let row = schema.get_field("r")?;
        for input in docs {
            let mut doc = TantivyDocument::default();
            doc.add_u64(row, input.row);
            doc.add_u64(schema.get_field("rh")?, input.row >> 32);
            doc.add_u64(schema.get_field("rn")?, input.row & 0xffff_ffff);
            for c in &input.cells {
                doc.add_u64(schema.get_field(&format!("p{}", c.k))?, c.state);
                if let Ok(f) = schema.get_field(&format!("e{}", c.k)) {
                    for v in &c.e {
                        doc.add_text(f, v);
                    }
                }
                if let Ok(f) = schema.get_field(&format!("c{}", c.k)) {
                    for v in &c.c {
                        doc.add_text(f, v);
                    }
                }
                if let Ok(f) = schema.get_field(&format!("t{}", c.k)) {
                    for v in &c.t {
                        doc.add_text(f, v);
                    }
                }
                if let Ok(f) = schema.get_field(&format!("e{}", c.k)) {
                    for v in &c.t {
                        doc.add_text(f, v);
                    }
                }
                if let Ok(f) = schema.get_field(&format!("u{}", c.k)) {
                    for &v in &c.u {
                        doc.add_u64(f, v);
                    }
                }
                if let Ok(f) = schema.get_field(&format!("i{}", c.k)) {
                    for &v in &c.i {
                        doc.add_i64(f, v);
                    }
                }
                if let Ok(f) = schema.get_field(&format!("f{}", c.k)) {
                    for &v in &c.f {
                        doc.add_f64(f, v);
                    }
                }
                if let Ok(f) = schema.get_field(&format!("b{}", c.k)) {
                    for &v in &c.b {
                        doc.add_bool(f, v);
                    }
                }
            }
            self.writer.add_document(doc)?;
        }
        self.checkpoint.tail = tail;
        self.commit()
    }
}
