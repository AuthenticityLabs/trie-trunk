//! Immutable, checksum-verified blocks, pinned by file incarnation and bounded by bytes.
use std::{
    collections::{BTreeMap, HashMap},
    fs::File,
    io,
    sync::{Arc, Mutex, OnceLock},
    time::{SystemTime, UNIX_EPOCH},
};
type Key = (u64, u64, u64);
struct Entry {
    bytes: Arc<[u8]>,
    _file: Arc<File>,
    last: u64,
    stamp: u64,
}
#[derive(Default)]
struct Cache {
    entries: HashMap<Key, Entry>,
    order: BTreeMap<u64, Key>,
    bytes: usize,
    clock: u64,
    files: HashMap<(u64, u64), usize>,
}
static CACHE: OnceLock<Mutex<Cache>> = OnceLock::new();
const BUDGET: usize = 256 << 20;
fn cutoff() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
        .saturating_sub(86_400_000) as u64
}
fn shared() -> &'static Mutex<Cache> {
    CACHE.get_or_init(Default::default)
}
impl Cache {
    fn remove(&mut self, key: &Key) {
        if let Some(e) = self.entries.remove(key) {
            self.bytes -= e.bytes.len();
            self.order.remove(&e.stamp);
            let id = (key.0, key.1);
            if let Some(count) = self.files.get_mut(&id) {
                *count -= 1;
                if *count == 0 {
                    self.files.remove(&id);
                }
            }
        }
    }
}
pub(crate) fn get(id: (u64, u64), offset: u64, size: u64) -> Option<Arc<[u8]>> {
    if !crate::runtime::resident() {
        return None;
    }
    let key = (id.0, id.1, offset);
    let mut c = shared().lock().unwrap();
    let e = c.entries.get(&key)?;
    if e.last < cutoff() || offset + 8 + e.bytes.len() as u64 > size {
        c.remove(&key);
        return None;
    }
    let bytes = Arc::clone(&e.bytes);
    let old = e.stamp;
    c.order.remove(&old);
    c.clock += 1;
    let stamp = c.clock;
    c.entries.get_mut(&key).unwrap().stamp = stamp;
    c.order.insert(stamp, key);
    Some(bytes)
}
pub(crate) fn insert(
    file: &Arc<File>,
    id: (u64, u64),
    offset: u64,
    last: u64,
    bytes: &[u8],
) -> io::Result<()> {
    if !crate::runtime::resident() || last < cutoff() || bytes.len() > BUDGET {
        return Ok(());
    }
    let key = (id.0, id.1, offset);
    let mut c = shared().lock().unwrap();
    if c.entries.contains_key(&key) {
        return Ok(());
    }
    while c.bytes + bytes.len() > BUDGET
        || c.entries.len() >= 8192
        || (!c.files.contains_key(&id) && c.files.len() >= 128)
    {
        let Some((_, key)) = c.order.first_key_value() else {
            break;
        };
        let key = *key;
        c.remove(&key);
    }
    c.clock += 1;
    let stamp = c.clock;
    c.bytes += bytes.len();
    c.order.insert(stamp, key);
    *c.files.entry(id).or_default() += 1;
    c.entries.insert(
        key,
        Entry {
            bytes: Arc::from(bytes),
            _file: Arc::clone(file),
            last,
            stamp,
        },
    );
    Ok(())
}

/// Run at query admission, not for every admitted block.
pub(crate) fn expire() {
    if let Some(shared) = CACHE.get() {
        let cutoff = cutoff();
        let mut cache = shared.lock().unwrap();
        let old: Vec<_> = cache
            .entries
            .iter()
            .filter_map(|(k, e)| (e.last < cutoff).then_some(*k))
            .collect();
        for k in old {
            cache.remove(&k);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::MetadataExt;
    #[test]
    fn cache_obeys_snapshot_end_incarnation_and_age() {
        let path = std::env::temp_dir().join(format!("trunk-cache-{}", std::process::id()));
        let file = Arc::new(File::create(&path).unwrap());
        let m = file.metadata().unwrap();
        let id = (m.dev(), m.ino());
        crate::runtime::install(Arc::new(std::sync::atomic::AtomicBool::new(false)));
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_millis() as u64;
        insert(&file, id, 8, now, b"validated block").unwrap();
        assert_eq!(
            get(id, 8, 100).as_deref(),
            Some(b"validated block".as_slice())
        );
        assert!(get((id.0, id.1 + 1), 8, 100).is_none());
        assert!(get(id, 8, 18).is_none()); // Truncated snapshot cannot reuse the old block.
        insert(&file, id, 8, now - 86_400_001, b"expired").unwrap();
        assert!(get(id, 8, 100).is_none());
        crate::runtime::clear();
        drop(file);
        std::fs::remove_file(path).unwrap();
    }
}
