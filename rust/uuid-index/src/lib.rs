//! A sorted UUID directory with disk leaf pages and an in-memory fence array.
//!
//! This is a standalone index prototype, not the deployed collection index.
//! Addresses are opaque: page splits never relocate the histories they reference.
//! Unix positioned I/O avoids mutable mmap aliases while pages are rewritten.

use std::collections::BTreeMap;
use std::fs::{File, OpenOptions};
use std::io;
use std::os::unix::fs::FileExt;
use std::path::{Path, PathBuf};
use std::sync::RwLock;

pub mod slotted;

pub type Uuid = [u8; 16];
pub const PAGE_BYTES: usize = 4096;
pub const RECORD_BYTES: usize = 24;
pub const PAGE_HEADER_BYTES: usize = 16;
pub const PAGE_CAPACITY: usize = (PAGE_BYTES - PAGE_HEADER_BYTES) / RECORD_BYTES;
pub const MAX_BATCH: usize = 1024;
const MAGIC: &[u8; 8] = b"UUIDIDX0";
const WAL_MAGIC: &[u8; 8] = b"UUIDWAL0";
const MAX_WAL_BYTES: usize = 24 + (2 * MAX_BATCH + 1) * (8 + PAGE_BYTES) + 4;
type Page = [u8; PAGE_BYTES];

#[derive(Debug)]
pub enum Error {
    Io(io::Error),
    Corrupt(&'static str),
    InvalidUuid,
    BatchTooLarge,
    NeedsReopen,
}
impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Io(e) => write!(f, "{e}"),
            Self::Corrupt(s) => write!(f, "corrupt UUID index: {s}"),
            Self::InvalidUuid => write!(f, "UUID does not match the configured version/variant"),
            Self::BatchTooLarge => write!(f, "batch exceeds {MAX_BATCH} entries"),
            Self::NeedsReopen => write!(
                f,
                "index must be reopened after a failed write or poisoned lock"
            ),
        }
    }
}
impl std::error::Error for Error {}
impl From<io::Error> for Error {
    fn from(e: io::Error) -> Self {
        Self::Io(e)
    }
}
pub type Result<T> = std::result::Result<T, Error>;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u8)]
pub enum KeyKind {
    Uuid = 0,
    V7 = 7,
    V8 = 8,
}
impl KeyKind {
    fn validate(self, key: &Uuid) -> Result<()> {
        if self == Self::Uuid || (key[6] >> 4 == self as u8 && key[8] >> 6 == 2) {
            Ok(())
        } else {
            Err(Error::InvalidUuid)
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub struct Entry {
    pub key: Uuid,
    pub address: u64,
}

#[derive(Clone, Copy)]
struct Fence {
    first: Uuid,
    page: u64,
}

struct State {
    fences: Vec<Fence>,
    pages: u64,
    entries: u64,
    max_key: Option<Uuid>,
    failed: bool,
    splits: u64,
    append_pages: u64,
    write_bytes: u64,
}

#[derive(Debug, Clone, Copy)]
pub struct Stats {
    pub entries: u64,
    pub pages: u64,
    pub file_bytes: u64,
    pub directory_bytes: usize,
    pub directory_capacity_bytes: usize,
    /// Counters since opening; not persisted.
    pub splits: u64,
    pub append_pages: u64,
    /// Application bytes written to data and journal, excluding fs metadata.
    pub write_bytes: u64,
}

pub struct UuidIndex {
    file: File,
    journal: File,
    kind: KeyKind,
    state: RwLock<State>,
    #[cfg(test)]
    fail_after: std::sync::atomic::AtomicUsize,
}

impl UuidIndex {
    /// Creates a new index; never replaces an existing one. Parent must exist.
    /// A single process owns the file; threads share this instance (e.g. Arc).
    pub fn create(path: impl AsRef<Path>, kind: KeyKind) -> Result<Self> {
        let path = path.as_ref();
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(path)?;
        file.try_lock()
            .map_err(|e| Error::Io(io::Error::other(e)))?;
        let journal = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(wal_path(path))?;
        let header = file_header(kind, 0, 0);
        file.write_all_at(&header, 0)?;
        file.sync_all()?;
        journal.sync_all()?;
        File::open(
            path.parent()
                .filter(|p| !p.as_os_str().is_empty())
                .unwrap_or(Path::new(".")),
        )?
        .sync_all()?;
        Self::load(file, journal, kind)
    }

    /// Replays a complete redo journal, then validates pages and rebuilds fences.
    /// Opening reads the full index sequentially but retains only page fences.
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let path = path.as_ref();
        let file = OpenOptions::new().read(true).write(true).open(path)?;
        file.try_lock()
            .map_err(|e| Error::Io(io::Error::other(e)))?;
        let journal = OpenOptions::new()
            .read(true)
            .write(true)
            .open(wal_path(path))?;
        recover(&file, &journal)?;
        let mut header = [0; PAGE_BYTES];
        file.read_exact_at(&mut header, 0)?;
        let (kind, _, _) = decode_header(&header)?;
        Self::load(file, journal, kind)
    }

    fn load(file: File, journal: File, kind: KeyKind) -> Result<Self> {
        let mut header = [0; PAGE_BYTES];
        file.read_exact_at(&mut header, 0)?;
        let (_, pages, entries) = decode_header(&header)?;
        let expected = pages
            .checked_add(1)
            .and_then(|n| n.checked_mul(PAGE_BYTES as u64))
            .ok_or(Error::Corrupt("file length overflow"))?;
        if file.metadata()?.len() != expected {
            return Err(Error::Corrupt("file length"));
        }
        // Last keys are temporary validation state, discarded after open.
        let mut ranges = Vec::new();
        let mut actual = 0u64;
        let mut page = [0; PAGE_BYTES];
        for id in 1..=pages {
            read_page(&file, id, &mut page)?;
            let n = count(&page);
            for row in 0..n {
                kind.validate(&key(&page, row))?;
                if row > 0 && key(&page, row - 1) >= key(&page, row) {
                    return Err(Error::Corrupt("unsorted or duplicate page keys"));
                }
            }
            actual += n as u64;
            ranges.push((
                Fence {
                    first: key(&page, 0),
                    page: id,
                },
                key(&page, n - 1),
            ));
        }
        ranges.sort_unstable_by_key(|r| r.0.first);
        if actual != entries || ranges.windows(2).any(|p| p[0].1 >= p[1].0.first) {
            return Err(Error::Corrupt(
                "overlapping ranges or incorrect entry count",
            ));
        }
        let max_key = ranges.last().map(|r| r.1);
        let fences = ranges.into_iter().map(|r| r.0).collect();
        Ok(Self {
            file,
            journal,
            kind,
            state: RwLock::new(State {
                fences,
                pages,
                entries,
                max_key,
                failed: false,
                splits: 0,
                append_pages: 0,
                write_bytes: 0,
            }),
            #[cfg(test)]
            fail_after: std::sync::atomic::AtomicUsize::new(0),
        })
    }

    /// One in-memory fence search plus at most one positioned page read.
    /// No heap allocation, payload hydration, or per-object resident state.
    pub fn get(&self, id: &Uuid) -> Result<Option<u64>> {
        self.kind.validate(id)?;
        let state = self.state.read().map_err(|_| Error::NeedsReopen)?;
        if state.failed {
            return Err(Error::NeedsReopen);
        }
        let upper = state.fences.partition_point(|f| f.first <= *id);
        if upper == 0 {
            return Ok(None);
        }
        let mut page = [0; PAGE_BYTES];
        read_page(&self.file, state.fences[upper - 1].page, &mut page)?;
        let at = lower_bound(&page, id);
        Ok((at < count(&page) && key(&page, at) == *id).then(|| address(&page, at)))
    }

    pub fn upsert(&self, key: Uuid, address: u64) -> Result<()> {
        self.upsert_batch(&[Entry { key, address }])
    }

    /// Atomic, durable batch. Duplicate keys within a batch are last-write-wins.
    /// Batching bounds dirty memory and amortizes durability barriers.
    /// Readers are excluded for the write transaction, including disk sync.
    pub fn upsert_batch(&self, entries: &[Entry]) -> Result<()> {
        if entries.len() > MAX_BATCH {
            return Err(Error::BatchTooLarge);
        }
        for e in entries {
            self.kind.validate(&e.key)?;
        }
        let mut state = self.state.write().map_err(|_| Error::NeedsReopen)?;
        if state.failed {
            return Err(Error::NeedsReopen);
        }
        if entries.is_empty() {
            return Ok(());
        }
        let result = self.apply(&mut state, entries);
        if result.is_err() {
            state.failed = true;
        }
        result
    }

    fn apply(&self, state: &mut State, entries: &[Entry]) -> Result<()> {
        let mut dirty: BTreeMap<u64, Page> = BTreeMap::new();
        for e in entries {
            if state.fences.is_empty() {
                let id = allocate(state)?;
                dirty.insert(id, make_page(id, &[*e]));
                state.fences.push(Fence {
                    first: e.key,
                    page: id,
                });
                state.entries += 1;
                state.max_key = Some(e.key);
                continue;
            }
            // This is checked against the complete UUID, not merely its time.
            let append = self.kind == KeyKind::V7 && state.max_key.is_some_and(|k| e.key > k);
            let fi = if append {
                state.fences.len() - 1
            } else {
                state
                    .fences
                    .partition_point(|f| f.first <= e.key)
                    .saturating_sub(1)
            };
            let page_id = state.fences[fi].page;
            let mut page = if let Some(p) = dirty.remove(&page_id) {
                p
            } else {
                let mut p = [0; PAGE_BYTES];
                read_page(&self.file, page_id, &mut p)?;
                p
            };
            let n = count(&page);
            let at = if append {
                n
            } else {
                lower_bound(&page, &e.key)
            };
            if at < n && key(&page, at) == e.key {
                put_entry(&mut page, at, *e);
                dirty.insert(page_id, page);
                continue;
            }
            state.entries += 1;
            state.max_key = Some(state.max_key.unwrap().max(e.key));
            if n < PAGE_CAPACITY {
                let start = PAGE_HEADER_BYTES + at * RECORD_BYTES;
                page.copy_within(
                    start..PAGE_HEADER_BYTES + n * RECORD_BYTES,
                    start + RECORD_BYTES,
                );
                put_entry(&mut page, at, *e);
                set_count(&mut page, n + 1);
                state.fences[fi].first = key(&page, 0);
                dirty.insert(page_id, page);
            } else if append {
                // Keep the full rightmost page full. An out-of-order key never
                // takes this path, even when all keys are valid UUIDv7 values.
                dirty.insert(page_id, page);
                let new_id = allocate(state)?;
                dirty.insert(new_id, make_page(new_id, &[*e]));
                state.fences.push(Fence {
                    first: e.key,
                    page: new_id,
                });
                state.append_pages += 1;
            } else {
                let mut rows = [Entry::default(); PAGE_CAPACITY + 1];
                for row in 0..n {
                    rows[row + usize::from(row >= at)] = Entry {
                        key: key(&page, row),
                        address: address(&page, row),
                    };
                }
                rows[at] = *e;
                let mid = rows.len() / 2;
                let new_id = allocate(state)?;
                dirty.insert(page_id, make_page(page_id, &rows[..mid]));
                dirty.insert(new_id, make_page(new_id, &rows[mid..]));
                state.fences[fi].first = rows[0].key;
                state.fences.insert(
                    fi + 1,
                    Fence {
                        first: rows[mid].key,
                        page: new_id,
                    },
                );
                state.splits += 1;
            }
        }
        for page in dirty.values_mut() {
            seal(page);
        }
        dirty.insert(0, file_header(self.kind, state.pages, state.entries));
        let bytes = journal_bytes(&dirty);
        self.journal.set_len(0)?;
        self.journal.write_all_at(&bytes, 0)?;
        self.journal.sync_all()?;
        self.check_failpoint(1)?;
        // Header last. Recovery has all after-images before any data is touched.
        let mut written = 0;
        for (&id, page) in dirty.range(1..) {
            self.file.write_all_at(page, page_offset(id)?)?;
            written += 1;
            self.check_failpoint(1 + written)?;
        }
        self.file.write_all_at(&dirty[&0], 0)?;
        self.file.sync_all()?;
        self.check_failpoint(usize::MAX)?;
        self.journal.set_len(0)?;
        self.journal.sync_all()?;
        state.write_bytes += bytes.len() as u64 + dirty.len() as u64 * PAGE_BYTES as u64;
        Ok(())
    }

    #[inline]
    fn check_failpoint(&self, point: usize) -> Result<()> {
        #[cfg(test)]
        if self.fail_after.load(std::sync::atomic::Ordering::Relaxed) == point {
            return Err(Error::Io(io::Error::other("injected interruption")));
        }
        let _ = point;
        Ok(())
    }

    pub fn stats(&self) -> Result<Stats> {
        let s = self.state.read().map_err(|_| Error::NeedsReopen)?;
        if s.failed {
            return Err(Error::NeedsReopen);
        }
        Ok(Stats {
            entries: s.entries,
            pages: s.pages,
            file_bytes: self.file.metadata()?.len(),
            directory_bytes: s.fences.len() * std::mem::size_of::<Fence>(),
            directory_capacity_bytes: s.fences.capacity() * std::mem::size_of::<Fence>(),
            splits: s.splits,
            append_pages: s.append_pages,
            write_bytes: s.write_bytes,
        })
    }
}

fn allocate(s: &mut State) -> Result<u64> {
    let id = s
        .pages
        .checked_add(1)
        .ok_or(Error::Corrupt("page number overflow"))?;
    page_offset(id)?
        .checked_add(PAGE_BYTES as u64)
        .ok_or(Error::Corrupt("file offset overflow"))?;
    s.pages = id;
    Ok(id)
}
fn page_offset(id: u64) -> Result<u64> {
    id.checked_mul(PAGE_BYTES as u64)
        .ok_or(Error::Corrupt("page offset overflow"))
}
fn wal_path(path: &Path) -> PathBuf {
    let mut name = path.as_os_str().to_owned();
    name.push(".wal");
    PathBuf::from(name)
}
fn u64_at(bytes: &[u8], at: usize) -> u64 {
    u64::from_le_bytes(bytes[at..at + 8].try_into().unwrap())
}
fn count(p: &Page) -> usize {
    u16::from_le_bytes(p[..2].try_into().unwrap()) as usize
}
fn set_count(p: &mut Page, n: usize) {
    p[..2].copy_from_slice(&(n as u16).to_le_bytes());
}
fn key(p: &Page, row: usize) -> Uuid {
    let at = PAGE_HEADER_BYTES + row * RECORD_BYTES;
    p[at..at + 16].try_into().unwrap()
}
fn address(p: &Page, row: usize) -> u64 {
    u64_at(p, PAGE_HEADER_BYTES + row * RECORD_BYTES + 16)
}
fn put_entry(p: &mut Page, row: usize, e: Entry) {
    let at = PAGE_HEADER_BYTES + row * RECORD_BYTES;
    p[at..at + 16].copy_from_slice(&e.key);
    p[at + 16..at + 24].copy_from_slice(&e.address.to_le_bytes());
}
fn lower_bound(p: &Page, target: &Uuid) -> usize {
    let (mut lo, mut hi) = (0, count(p));
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        if key(p, mid) < *target {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    lo
}
fn make_page(id: u64, rows: &[Entry]) -> Page {
    let mut p = [0; PAGE_BYTES];
    set_count(&mut p, rows.len());
    p[2] = 1;
    p[8..16].copy_from_slice(&id.to_le_bytes());
    for (row, e) in rows.iter().enumerate() {
        put_entry(&mut p, row, *e);
    }
    seal(&mut p);
    p
}
fn checksum(p: &Page) -> u32 {
    let c = crc32c::crc32c(&p[..4]);
    let c = crc32c::crc32c_append(c, &[0; 4]);
    crc32c::crc32c_append(c, &p[8..])
}
fn seal(p: &mut Page) {
    let crc = checksum(p);
    p[4..8].copy_from_slice(&crc.to_le_bytes());
}
fn valid(p: &Page) -> bool {
    u32::from_le_bytes(p[4..8].try_into().unwrap()) == checksum(p)
}
fn read_page(file: &File, id: u64, page: &mut Page) -> Result<()> {
    file.read_exact_at(page, page_offset(id)?)?;
    if !valid(page)
        || page[2..4] != [1, 0]
        || u64_at(page, 8) != id
        || !(1..=PAGE_CAPACITY).contains(&count(page))
    {
        return Err(Error::Corrupt("leaf header or checksum"));
    }
    Ok(())
}
fn file_header(kind: KeyKind, pages: u64, entries: u64) -> Page {
    let mut p = [0; PAGE_BYTES];
    p[8..16].copy_from_slice(MAGIC);
    p[16] = kind as u8;
    p[20..24].copy_from_slice(&(PAGE_BYTES as u32).to_le_bytes());
    p[24..32].copy_from_slice(&pages.to_le_bytes());
    p[32..40].copy_from_slice(&entries.to_le_bytes());
    seal(&mut p);
    p
}
fn decode_header(p: &Page) -> Result<(KeyKind, u64, u64)> {
    if !valid(p)
        || &p[8..16] != MAGIC
        || u32::from_le_bytes(p[20..24].try_into().unwrap()) != PAGE_BYTES as u32
    {
        return Err(Error::Corrupt("file header"));
    }
    let kind = match p[16] {
        0 => KeyKind::Uuid,
        7 => KeyKind::V7,
        8 => KeyKind::V8,
        _ => return Err(Error::Corrupt("UUID kind")),
    };
    Ok((kind, u64_at(p, 24), u64_at(p, 32)))
}
fn journal_bytes(dirty: &BTreeMap<u64, Page>) -> Vec<u8> {
    let len = 24 + dirty.len() * (8 + PAGE_BYTES) + 4;
    let mut b = Vec::with_capacity(len);
    b.extend_from_slice(WAL_MAGIC);
    b.extend_from_slice(&(len as u64).to_le_bytes());
    b.extend_from_slice(&(dirty.len() as u64).to_le_bytes());
    for (&id, p) in dirty {
        b.extend_from_slice(&id.to_le_bytes());
        b.extend_from_slice(p);
    }
    b.extend_from_slice(&crc32c::crc32c(&b).to_le_bytes());
    b
}
fn recover(file: &File, journal: &File) -> Result<()> {
    let size = journal.metadata()?.len();
    if size == 0 {
        return Ok(());
    }
    if size > MAX_WAL_BYTES as u64 {
        return Err(Error::Corrupt("journal too large"));
    }
    let mut bytes = vec![0; size as usize];
    journal.read_exact_at(&mut bytes, 0)?;
    // An incomplete WAL cannot have reached the data-write phase. A damaged,
    // complete WAL fails closed instead of silently discarding possibly needed data.
    if bytes.len() < 24 {
        journal.set_len(0)?;
        journal.sync_all()?;
        return Ok(());
    }
    if &bytes[..8] != WAL_MAGIC {
        return Err(Error::Corrupt("journal magic"));
    }
    let expected = u64_at(&bytes, 8);
    if expected > MAX_WAL_BYTES as u64 || expected < 24 + 8 + PAGE_BYTES as u64 + 4 {
        return Err(Error::Corrupt("journal length"));
    }
    if size < expected {
        journal.set_len(0)?;
        journal.sync_all()?;
        return Ok(());
    }
    if size != expected {
        return Err(Error::Corrupt("journal length mismatch"));
    }
    let crc = u32::from_le_bytes(bytes[bytes.len() - 4..].try_into().unwrap());
    if crc != crc32c::crc32c(&bytes[..bytes.len() - 4]) {
        return Err(Error::Corrupt("journal checksum"));
    }
    let n = u64_at(&bytes, 16) as usize;
    if n == 0 || n > 2 * MAX_BATCH + 1 || bytes.len() != 24 + n * (8 + PAGE_BYTES) + 4 {
        return Err(Error::Corrupt("journal page count"));
    }
    let mut pages = BTreeMap::new();
    for item in bytes[24..bytes.len() - 4].chunks_exact(8 + PAGE_BYTES) {
        let id = u64_at(item, 0);
        let p: Page = item[8..].try_into().unwrap();
        if !valid(&p) || pages.insert(id, p).is_some() {
            return Err(Error::Corrupt("journal page"));
        }
    }
    let header = pages
        .get(&0)
        .ok_or(Error::Corrupt("missing journal header"))?;
    let (_, total, _) = decode_header(header)?;
    for (&id, p) in pages.range(1..) {
        if id > total
            || u64_at(p, 8) != id
            || p[2..4] != [1, 0]
            || !(1..=PAGE_CAPACITY).contains(&count(p))
        {
            return Err(Error::Corrupt("journal leaf"));
        }
    }
    for (&id, p) in pages.range(1..) {
        file.write_all_at(p, page_offset(id)?)?;
    }
    file.write_all_at(header, 0)?;
    file.sync_all()?;
    journal.set_len(0)?;
    journal.sync_all()?;
    Ok(())
}

#[cfg(test)]
mod tests;
