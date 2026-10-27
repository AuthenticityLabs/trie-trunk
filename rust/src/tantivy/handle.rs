//! Schema, durable checkpoints, and ingestion. No original payload is STORED.
use super::{Checkpoint, Doc, Handle, Request, Result};
use std::{collections::HashMap, ops::Bound, path::Path};
use tantivy::query::RangeQuery;
use tantivy::{Index, ReloadPolicy, Term};
pub(super) use trunk_search_core::schema;

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
        for input in docs {
            self.writer.add_document(trunk_search_core::document(
                &schema,
                input.row,
                &input.cells,
            )?)?;
        }
        self.checkpoint.tail = tail;
        self.commit()
    }
}
