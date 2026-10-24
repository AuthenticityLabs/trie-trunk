//! Pins sources and separates shared queries from exclusive publication.
use super::{Arc, AtomicBool, Ordering, check};
use crate::tantivy_engine::{Handle, Request, Response, Result, run};
use std::sync::{Mutex, OnceLock, RwLock, atomic::AtomicU64};
use std::{collections::HashMap, path::Path, time::Duration};

struct SourceState {
    closed: bool,
    handles: HashMap<String, Handle>,
}
struct Source {
    state: RwLock<SourceState>,
    used: AtomicU64,
}
static SOURCES: OnceLock<Mutex<HashMap<String, Arc<Source>>>> = OnceLock::new();
static CLOCK: AtomicU64 = AtomicU64::new(0);
fn registry() -> &'static Mutex<HashMap<String, Arc<Source>>> {
    SOURCES.get_or_init(|| Mutex::new(HashMap::new()))
}
fn close(state: &mut SourceState) -> Result<()> {
    state.closed = true;
    for (_, h) in state.handles.drain() {
        h.writer.wait_merging_threads()?;
    }
    Ok(())
}
pub(super) fn execute(mut r: Request, c: Option<&Arc<AtomicBool>>) -> Result<Response> {
    check(c)?;
    let clock = CLOCK.fetch_add(1, Ordering::Relaxed);
    let source = {
        let mut all = registry().lock().map_err(|_| "search registry poisoned")?;
        if r.op == "release" {
            let keys: Vec<_> = all
                .keys()
                .filter(|p| Path::new(p).starts_with(&r.path))
                .cloned()
                .collect();
            for key in keys {
                if let Some(s) = all.remove(&key) {
                    close(&mut *s.state.write().map_err(|_| "search source poisoned")?)?;
                }
            }
            return Ok(Response::default());
        }
        if !all.contains_key(&r.path) && all.len() >= 16 {
            // Evict only idle sources: active calls pin their source with Arc.
            let old = all
                .iter()
                .filter(|(_, s)| Arc::strong_count(s) == 1)
                .min_by_key(|(_, s)| s.used.load(Ordering::Relaxed))
                .map(|(k, _)| k.clone());
            if let Some(key) = old {
                let s = all.remove(&key).unwrap();
                close(&mut *s.state.write().map_err(|_| "search source poisoned")?)?;
            }
        }
        all.entry(r.path.clone())
            .or_insert_with(|| {
                Arc::new(Source {
                    state: RwLock::new(SourceState {
                        closed: false,
                        handles: HashMap::new(),
                    }),
                    used: AtomicU64::new(clock),
                })
            })
            .clone()
    };
    source.used.store(clock, Ordering::Relaxed);
    let key = if r.op.starts_with("live_") {
        format!("{}#live:{}", r.path, r.binding)
    } else {
        r.path.clone()
    };
    if matches!(
        r.op.as_str(),
        "query" | "live_query" | "status" | "live_status"
    ) {
        let state = loop {
            check(c)?;
            match source.state.try_read() {
                Ok(s) => break s,
                Err(std::sync::TryLockError::WouldBlock) => {
                    std::thread::sleep(Duration::from_micros(100))
                }
                Err(_) => return Err("search source poisoned".into()),
            }
        };
        if state.closed {
            return Err("search source released".into());
        }
        if let Some(h) = state.handles.get(&key).filter(|h| {
            r.op.starts_with("live_")
                || (h.checkpoint.binding == r.binding && h.checkpoint.base == r.base)
        }) {
            if r.op == "status" {
                return Ok(Response {
                    tail: h.checkpoint.tail,
                    ..Default::default()
                });
            }
            if r.op == "live_status" {
                return Ok(Response {
                    ids: h.live_ids.keys().cloned().collect(),
                    ..Default::default()
                });
            }
            if r.op == "live_query" {
                live_ranges(h, &mut r);
                let ids = h
                    .search_cancel(&r, c.cloned())?
                    .into_iter()
                    .filter_map(|n| h.live_names.get(&n).cloned())
                    .collect();
                return Ok(Response {
                    ids,
                    ..Default::default()
                });
            }
            return Ok(Response {
                tail: h.checkpoint.tail,
                rows: h.search_cancel(&r, c.cloned())?,
                ..Default::default()
            });
        }
    }
    let mut state = loop {
        check(c)?;
        match source.state.try_write() {
            Ok(s) => break s,
            Err(std::sync::TryLockError::WouldBlock) => {
                std::thread::sleep(Duration::from_micros(100))
            }
            Err(_) => return Err("search source poisoned".into()),
        }
    };
    if state.closed {
        return Err("search source released".into());
    }
    // Cold open/updates are exclusive per source. Warm queries use shared locks.
    // Once a mutation begins, finish its checkpoint commit; cancellation must
    // not leave half-updated live identity maps or a half-published checkpoint.
    if r.op == "query" || r.op == "live_query" {
        let query = r.op.clone();
        r.op = if query == "query" {
            "status"
        } else {
            "live_status"
        }
        .into();
        run(
            Request {
                op: r.op.clone(),
                path: r.path.clone(),
                binding: r.binding.clone(),
                fields: r.fields.clone(),
                base: r.base,
                first_ref: r.first_ref,
                ..Default::default()
            },
            &mut state.handles,
            clock,
        )?;
        r.op = query;
        let h = &state.handles[&key];
        if r.op == "live_query" {
            live_ranges(h, &mut r);
            return Ok(Response {
                ids: h
                    .search_cancel(&r, c.cloned())?
                    .into_iter()
                    .filter_map(|n| h.live_names.get(&n).cloned())
                    .collect(),
                ..Default::default()
            });
        }
        return Ok(Response {
            tail: h.checkpoint.tail,
            rows: h.search_cancel(&r, c.cloned())?,
            ..Default::default()
        });
    }
    run(r, &mut state.handles, clock)
}
fn live_ranges(h: &Handle, r: &mut Request) {
    r.ranges = r
        .ids
        .iter()
        .filter_map(|id| h.live_ids.get(id).map(|n| (*n, *n + 1)))
        .collect();
    r.ranges.sort_unstable();
    let mut merged: Vec<(u64, u64)> = Vec::new();
    for &(a, b) in &r.ranges {
        if let Some(last) = merged.last_mut() {
            if last.1 == a {
                last.1 = b;
                continue;
            }
        }
        merged.push((a, b));
    }
    r.ranges = merged;
}
#[cfg(test)]
mod tests {
    use super::*;
    use crate::tantivy_engine::{Cell, Doc, Filter};
    use std::sync::mpsc;
    fn setup(path: &str) -> Result<()> {
        execute(
            Request {
                op: "live_append".into(),
                path: path.into(),
                binding: "test".into(),
                fields: vec!["e".into()],
                docs: vec![Doc {
                    id: "event".into(),
                    source_row: 0,
                    row: 0,
                    cells: vec![Cell {
                        k: 0,
                        e: vec!["object".into()],
                        state: 2,
                        ..Default::default()
                    }],
                }],
                ..Default::default()
            },
            None,
        )?;
        Ok(())
    }
    fn query(path: &str) -> Request {
        Request {
            op: "live_query".into(),
            path: path.into(),
            binding: "test".into(),
            fields: vec!["e".into()],
            ids: vec!["event".into()],
            filters: vec![Filter {
                k: 0,
                kind: "e".into(),
                op: "==".into(),
                value: "object".into(),
                ..Default::default()
            }],
            ..Default::default()
        }
    }
    #[test]
    fn warm_queries_share_source_read_lock_and_cancel_independently() -> Result<()> {
        let path = "native-read-concurrency";
        setup(path)?;
        let source = registry().lock().unwrap()[path].clone();
        let guard = source.state.read().unwrap();
        let (tx, rx) = mpsc::channel();
        let worker = std::thread::spawn(move || {
            tx.send(
                execute(query(path), None)
                    .map(|r| r.ids)
                    .map_err(|e| e.to_string()),
            )
            .unwrap()
        });
        let result = rx.recv_timeout(Duration::from_secs(2));
        drop(guard);
        worker.join().unwrap();
        assert_eq!(result??, vec!["event"]);
        let guard = source.state.write().unwrap();
        let cancel = Arc::new(AtomicBool::new(false));
        let worker_flag = cancel.clone();
        let (tx, rx) = mpsc::channel();
        let worker = std::thread::spawn(move || {
            tx.send(
                execute(query(path), Some(&worker_flag))
                    .map(|r| r.ids)
                    .map_err(|e| e.to_string()),
            )
            .unwrap()
        });
        cancel.store(true, Ordering::Relaxed);
        let result = rx.recv_timeout(Duration::from_secs(2));
        drop(guard);
        worker.join().unwrap();
        assert!(result?.unwrap_err().contains("canceled"));
        assert_eq!(execute(query(path), None)?.ids, vec!["event"]);
        execute(
            Request {
                op: "release".into(),
                path: path.into(),
                ..Default::default()
            },
            None,
        )?;
        Ok(())
    }
    #[test]
    fn one_source_writer_does_not_gate_another_source_query() -> Result<()> {
        let a = "native-independent-a";
        let b = "native-independent-b";
        setup(a)?;
        setup(b)?;
        let source = registry().lock().unwrap()[a].clone();
        let guard = source.state.write().unwrap();
        let (tx, rx) = mpsc::channel();
        let worker = std::thread::spawn(move || {
            tx.send(
                execute(query(b), None)
                    .map(|r| r.ids)
                    .map_err(|e| e.to_string()),
            )
            .unwrap()
        });
        let result = rx.recv_timeout(Duration::from_secs(2));
        drop(guard);
        worker.join().unwrap();
        assert_eq!(result??, vec!["event"]);
        for path in [a, b] {
            execute(
                Request {
                    op: "release".into(),
                    path: path.into(),
                    ..Default::default()
                },
                None,
            )?;
        }
        Ok(())
    }
    #[test]
    fn canceled_collector_rejects_query_without_poisoning_reader() -> Result<()> {
        let r = Request {
            binding: "collector-cancel".into(),
            fields: vec!["e".into()],
            ranges: vec![(0, 2)],
            ..Default::default()
        };
        let mut h = Handle::open(&r, true)?;
        h.append(
            &[Doc {
                row: 1,
                source_row: 1,
                cells: vec![],
                ..Default::default()
            }],
            2,
            true,
        )?;
        assert!(
            h.search_cancel(&r, Some(Arc::new(AtomicBool::new(true))))
                .is_err()
        );
        assert_eq!(h.search(&r)?, vec![1]);
        Ok(())
    }
}
