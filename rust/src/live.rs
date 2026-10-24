//! Live event metadata. Payload addresses borrow immutable native hour arenas;
//! the Go adapter holds its store lock and pins arenas for any escaping snapshot.
//! No Go pointer, decoded field, or payload copy is retained here.
use std::{
    collections::{BTreeMap, BTreeSet, HashMap},
    ffi::c_void,
    sync::Mutex,
};
type Id = [u8; 16];
#[repr(C)]
pub struct LiveId {
    bytes: Id,
}
#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct LiveRecord {
    pub id: Id,
    pub address: usize,
    pub length: usize,
    pub row: u64,
}
#[derive(Clone, Copy)]
struct Entry {
    record: LiveRecord,
    object: Option<(Id, u64)>,
    pending: bool,
    hot: bool,
}
#[derive(Default)]
struct State {
    entries: HashMap<Id, Entry>,
    pending: BTreeSet<Id>,
    hot: BTreeSet<Id>,
    revisions: HashMap<Id, BTreeMap<u64, Id>>,
    objects: HashMap<Id, BTreeSet<Id>>,
}
struct Live {
    state: Mutex<State>,
    limit: usize,
}
fn ms(id: &Id) -> i64 {
    let mut b = [0; 8];
    b[2..].copy_from_slice(&id[..6]);
    u64::from_be_bytes(b) as i64
}
fn bound(t: i64) -> Id {
    let mut id = [0; 16];
    id[..6].copy_from_slice(&(t.max(0) as u64).to_be_bytes()[2..]);
    id
}
impl State {
    fn detach_pending(&mut self, id: Id) {
        if let Some(e) = self.entries.get_mut(&id) {
            e.pending = false;
            self.pending.remove(&id);
            if let Some((obj, rev)) = e.object {
                if let Some(v) = self.revisions.get_mut(&obj) {
                    v.remove(&rev);
                    if v.is_empty() {
                        self.revisions.remove(&obj);
                    }
                }
            }
        }
    }
    fn prune(&mut self, cutoff: i64) {
        while let Some(id) = self.hot.first().copied() {
            if ms(&id) >= cutoff {
                break;
            }
            self.hot.remove(&id);
            if let Some(e) = self.entries.get_mut(&id) {
                e.hot = false;
                if !e.pending {
                    self.remove(id);
                }
            }
        }
        if self.entries.capacity() > 1024
            && self.entries.capacity() > self.entries.len().saturating_mul(4)
        {
            self.entries.shrink_to(self.entries.len().saturating_mul(2));
            self.objects.shrink_to(self.objects.len().saturating_mul(2));
            self.revisions
                .shrink_to(self.revisions.len().saturating_mul(2));
        }
    }
    fn remove(&mut self, id: Id) {
        if let Some(e) = self.entries.remove(&id) {
            if let Some((obj, _)) = e.object {
                if let Some(v) = self.objects.get_mut(&obj) {
                    v.remove(&id);
                    if v.is_empty() {
                        self.objects.remove(&obj);
                    }
                }
            }
        }
    }
    fn put(&mut self, r: LiveRecord, object: Option<(Id, u64)>, pending: bool, cutoff: i64) {
        self.prune(cutoff);
        let hot = ms(&r.id) >= cutoff;
        if !pending && !hot {
            return;
        }
        if let Some(e) = self.entries.get_mut(&r.id) {
            // Sealing retains pending identity until the journal and seed rewrite
            // commit. Readers must still see this event while packing is in flight.
            e.record.row = r.row;
            return;
        }
        self.entries.insert(
            r.id,
            Entry {
                record: r,
                object,
                pending,
                hot,
            },
        );
        if hot {
            self.hot.insert(r.id);
        }
        if pending {
            self.pending.insert(r.id);
        }
        if let Some((obj, rev)) = object {
            self.objects.entry(obj).or_default().insert(r.id);
            if pending {
                self.revisions.entry(obj).or_default().insert(rev, r.id);
            }
        }
    }
}
unsafe fn live<'a>(p: *const c_void) -> &'a Live {
    unsafe { &*p.cast::<Live>() }
}
#[unsafe(no_mangle)]
pub extern "C" fn trunk_live_new(limit: usize) -> *mut c_void {
    Box::into_raw(Box::new(Live {
        state: Mutex::new(State::default()),
        limit,
    }))
    .cast()
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_free(p: *mut c_void) {
    if !p.is_null() {
        drop(unsafe { Box::from_raw(p.cast::<Live>()) });
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_room(p: *const c_void, cutoff: i64) -> bool {
    let l = unsafe { live(p) };
    let mut s = l.state.lock().unwrap();
    s.prune(cutoff);
    s.entries.len() < l.limit
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_put(
    p: *const c_void,
    r: LiveRecord,
    obj: *const u8,
    rev: u64,
    pending: bool,
    cutoff: i64,
) -> bool {
    let l = unsafe { live(p) };
    let mut s = l.state.lock().unwrap();
    s.prune(cutoff);
    if s.entries.len() >= l.limit && !s.entries.contains_key(&r.id) {
        return false;
    }
    let object = if obj.is_null() {
        None
    } else {
        Some((unsafe { *(obj.cast::<Id>()) }, rev))
    };
    s.put(r, object, pending, cutoff);
    true
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_get(
    p: *const c_void,
    id: LiveId,
    pending: bool,
    cutoff: i64,
) -> LiveRecord {
    let s = unsafe { live(p) }.state.lock().unwrap();
    if let Some(e) = s.entries.get(&id.bytes) {
        if (pending && e.pending) || (!pending && e.hot && ms(&id.bytes) >= cutoff) {
            return e.record;
        }
    }
    LiveRecord::default()
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_revision(p: *const c_void, obj: *const u8, rev: u64) -> bool {
    unsafe { live(p) }
        .state
        .lock()
        .unwrap()
        .revisions
        .get(unsafe { &*obj.cast::<Id>() })
        .is_some_and(|v| v.contains_key(&rev))
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_prune(p: *const c_void, cutoff: i64) {
    unsafe { live(p) }.state.lock().unwrap().prune(cutoff);
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_finish(p: *const c_void, cutoff: i64) {
    let mut s = unsafe { live(p) }.state.lock().unwrap();
    while let Some(id) = s.pending.first().copied() {
        if ms(&id) >= cutoff {
            break;
        }
        s.detach_pending(id);
        if !s.entries[&id].hot {
            s.remove(id);
        }
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_count(p: *const c_void, kind: u8) -> usize {
    let s = unsafe { live(p) }.state.lock().unwrap();
    match kind {
        0 => s.pending.len(),
        1 => s.hot.len(),
        _ => s.entries.len(),
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn record(t: u64, n: u8) -> LiveRecord {
        let mut id = bound(t as i64);
        id[15] = n;
        LiveRecord {
            id,
            address: 1,
            length: 12,
            row: u64::MAX,
        }
    }
    #[test]
    fn seal_keeps_pending_until_publication_and_prune_cleans_objects() {
        let mut s = State::default();
        let obj = [7; 16];
        let mut r = record(100, 1);
        s.put(r, Some((obj, 3)), true, 0);
        r.row = 42;
        s.put(r, Some((obj, 3)), false, 0);
        assert!(s.entries[&r.id].pending);
        assert_eq!(s.revisions[&obj][&3], r.id);
        s.detach_pending(r.id);
        assert!(s.revisions.is_empty());
        s.prune(101);
        assert!(s.entries.is_empty());
        assert!(s.objects.is_empty());
    }
    #[test]
    fn expired_pending_survives_until_commit() {
        let mut s = State::default();
        let r = record(100, 1);
        s.put(r, None, true, 0);
        s.prune(101);
        assert_eq!(s.entries.len(), 1);
        s.detach_pending(r.id);
        if !s.entries[&r.id].hot {
            s.remove(r.id);
        }
        assert!(s.entries.is_empty());
    }
}

#[repr(C)]
pub struct LiveResult {
    data: *const LiveRecord,
    len: usize,
    capacity: usize,
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_snapshot(
    p: *const c_void,
    obj: *const u8,
    kind: u8,
    from: i64,
    to: i64,
    cutoff: i64,
) -> LiveResult {
    let mut s = unsafe { live(p) }.state.lock().unwrap();
    s.prune(cutoff);
    let ids = if obj.is_null() {
        if kind == 0 {
            Some(&s.pending)
        } else {
            Some(&s.hot)
        }
    } else {
        s.objects.get(unsafe { &*obj.cast::<Id>() })
    };
    let mut records = Vec::with_capacity(ids.map_or(0, |ids| ids.len().min(256)));
    if from < to && from < (1i64 << 48) && to > 0 {
        if let Some(ids) = ids {
            for id in ids.range((
                std::ops::Bound::Included(bound(from)),
                if to >= (1i64 << 48) {
                    std::ops::Bound::Unbounded
                } else {
                    std::ops::Bound::Excluded(bound(to))
                },
            )) {
                let e = &s.entries[id];
                if (kind == 0 && !e.pending) || (kind == 1 && (!e.hot || e.pending)) {
                    continue;
                }
                let mut r = e.record;
                if e.pending {
                    r.row = u64::MAX;
                }
                records.push(r);
            }
        }
    }
    let records = std::mem::ManuallyDrop::new(records);
    LiveResult {
        data: records.as_ptr(),
        len: records.len(),
        capacity: records.capacity(),
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_result_free(p: *mut LiveRecord, len: usize, cap: usize) {
    if cap > 0 {
        drop(unsafe { Vec::from_raw_parts(p, len, cap) });
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn trunk_live_first_pending(p: *const c_void) -> i64 {
    let s = unsafe { live(p) }.state.lock().unwrap();
    s.pending.first().map_or(i64::MAX, ms)
}
