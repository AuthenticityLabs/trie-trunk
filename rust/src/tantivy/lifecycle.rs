//! Mutations of sealed/live source handles; called under the source write lock.
use super::{Handle, Request, Response, Result};
use std::{collections::HashMap, path::Path};
use tantivy::Term;

pub(super) fn run(
    mut r: Request,
    handles: &mut HashMap<String, Handle>,
    clock: u64,
) -> Result<Response> {
    if r.op == "release" {
        let prefix = Path::new(&r.path);
        let keys: Vec<_> = handles
            .keys()
            .filter(|key| Path::new(key).starts_with(prefix))
            .cloned()
            .collect();
        for key in keys {
            if let Some(h) = handles.remove(&key) {
                h.writer.wait_merging_threads()?;
            }
        }
        return Ok(Response::default());
    }
    let requested = if r.op.starts_with("live_") {
        format!("{}#live:{}", r.path, r.binding)
    } else {
        r.path.clone()
    };
    if !handles.contains_key(&requested) && handles.len() >= 16 {
        if let Some(key) = handles
            .iter()
            .min_by_key(|(_, h)| h.used)
            .map(|(k, _)| k.clone())
        {
            if let Some(h) = handles.remove(&key) {
                h.writer.wait_merging_threads()?;
            }
        }
    }
    if r.op.starts_with("live_") {
        let key = format!("{}#live:{}", r.path, r.binding);
        if !handles.contains_key(&key) {
            handles.insert(
                key.clone(),
                Handle::open(
                    &Request {
                        binding: r.binding.clone(),
                        fields: r.fields.clone(),
                        ..Default::default()
                    },
                    true,
                )?,
            );
        }
        let h = handles.get_mut(&key).unwrap();
        h.used = clock;
        if r.op == "live_status" {
            return Ok(Response {
                ids: h.live_ids.keys().cloned().collect(),
                ..Default::default()
            });
        }
        if r.op == "live_append" {
            let mut fresh = Vec::new();
            for mut d in r.docs {
                if !h.live_ids.contains_key(&d.id) {
                    d.row = h.next_live;
                    h.next_live = h
                        .next_live
                        .checked_add(1)
                        .ok_or("live reference exhausted")?;
                    h.live_ids.insert(d.id.clone(), d.row);
                    h.live_names.insert(d.row, d.id.clone());
                    fresh.push(d);
                }
            }
            if !fresh.is_empty() {
                h.append(&fresh, h.next_live, false)?;
            }
            return Ok(Response::default());
        }
        if r.op == "live_query" {
            r.ranges = r
                .ids
                .iter()
                .filter_map(|id| h.live_ids.get(id).map(|n| (*n, *n + 1)))
                .collect();
            r.ranges.sort_unstable();
            let mut merged: Vec<(u64, u64)> = Vec::new();
            for (a, b) in r.ranges {
                if let Some(last) = merged.last_mut() {
                    if last.1 == a {
                        last.1 = b;
                        continue;
                    }
                }
                merged.push((a, b));
            }
            r.ranges = merged;
            let ids = h
                .search(&r)?
                .into_iter()
                .filter_map(|n| h.live_names.get(&n).cloned())
                .collect();
            return Ok(Response {
                ids,
                ..Default::default()
            });
        }
        if r.op == "live_prune" {
            let keep: std::collections::HashSet<_> = r.ids.into_iter().collect();
            let row = h.index.schema().get_field("r")?;
            let remove: Vec<_> = h
                .live_ids
                .keys()
                .filter(|id| !keep.contains(*id))
                .cloned()
                .collect();
            for id in &remove {
                if let Some(n) = h.live_ids.remove(id) {
                    h.writer.delete_term(Term::from_field_u64(row, n));
                    h.live_names.remove(&n);
                }
            }
            if !remove.is_empty() {
                h.commit()?;
            }
            if h.live_ids.is_empty() {
                handles.remove(&key);
            }
            return Ok(Response::default());
        }
        return Err("unknown live operation".into());
    }
    // Retention/canonical-tail changes invalidate the cached handle. Opening
    // applies deletion or reset atomically with its new checkpoint.
    let reopen = handles
        .get(&r.path)
        .is_some_and(|h| h.checkpoint.binding != r.binding || h.checkpoint.base != r.base);
    if reopen {
        if let Some(h) = handles.remove(&r.path) {
            h.writer.wait_merging_threads()?;
        }
    }
    if !handles.contains_key(&r.path) {
        handles.insert(r.path.clone(), Handle::open(&r, false)?);
    }
    let h = handles.get_mut(&r.path).unwrap();
    h.used = clock;
    let rows = match r.op.as_str() {
        "status" => vec![],
        "append" => {
            h.append(&r.docs, r.tail, true)?;
            vec![]
        }
        "query" => h.search(&r)?,
        _ => return Err("unknown operation".into()),
    };
    Ok(Response {
        tail: h.checkpoint.tail,
        rows,
        ..Default::default()
    })
}
