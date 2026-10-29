//! Standalone page-local collection prototype. Eight-byte event references are
//! opaque, source-bound locators, not a new PAC address encoding.
pub mod page;
use crate::{KeyKind, Uuid};
use page::{MAX_REVISIONS, PAGE, Page};
use std::{
    collections::{BTreeMap, BTreeSet},
    fs::{File, OpenOptions},
    io,
    os::unix::fs::FileExt,
    path::{Path, PathBuf},
    sync::{Arc, Mutex, RwLock, Weak},
};

pub type Result<T> = std::result::Result<T, Error>;
#[derive(Debug)]
pub enum Error {
    Io(io::Error),
    Corrupt(&'static str),
    PageFull,
    ObjectFull,
    DuplicateRevision,
    ExpiredRevision,
    ZeroAddress,
    InvalidUuid,
    InvalidPolicy,
    NeedsReopen,
    Injected(u8),
}
impl From<io::Error> for Error {
    fn from(e: io::Error) -> Self {
        Self::Io(e)
    }
}
impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{self:?}")
    }
}
impl std::error::Error for Error {}
#[derive(Clone, Copy, Debug, Default)]
pub struct Stats {
    pub pointer_probes: u64,
    pub uuid_comparisons: u64,
    pub page_reads: u64,
    pub splits: u64,
    pub written_bytes: u64,
    pub allocated_pages: u64,
    pub reused_pages: u64,
    pub commits: u64,
    pub recovery_replayed: u64,
    pub peak_dirty_pages: usize,
}
#[derive(Clone, Copy, Debug)]
pub struct Command {
    pub object: Uuid,
    pub revision: u16,
    pub address: u64,
    pub retire: bool,
}
impl Command {
    pub fn put(object: Uuid, revision: u16, address: u64) -> Self {
        Self {
            object,
            revision,
            address,
            retire: false,
        }
    }
    pub fn retire_before(object: Uuid, revision: u16) -> Self {
        Self {
            object,
            revision,
            address: 0,
            retire: true,
        }
    }
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Fence {
    first: Uuid,
    page: u32,
}
#[derive(Default)]
struct Directory {
    fences: Vec<Fence>,
    metadata: Vec<u32>,
    checkpoint: u64,
    sequence: u64,
}
impl Directory {
    fn pages(&self) -> impl Iterator<Item = u32> + '_ {
        self.fences
            .iter()
            .map(|f| f.page)
            .chain(self.metadata.iter().copied())
    }
}
struct Retired {
    view: Weak<Directory>,
    pages: Vec<u32>,
}
struct Writer {
    free: Vec<u32>,
    next: u32,
    roots: [Option<Arc<Directory>>; 2],
    retired: Vec<Retired>,
    failed: bool,
    stats: Stats,
}

pub struct Index {
    file: Arc<File>,
    seed: File,
    kind: KeyKind,
    keep: Option<u16>,
    published: RwLock<Arc<Directory>>,
    writer: Mutex<Writer>,
    fault: std::sync::atomic::AtomicU8,
}
pub struct Snapshot {
    file: Arc<File>,
    directory: Arc<Directory>,
    kind: KeyKind,
}

fn validate_key(kind: KeyKind, key: &Uuid) -> Result<()> {
    kind.validate(key).map_err(|_| Error::InvalidUuid)
}
fn source_path(path: &Path) -> PathBuf {
    let mut s = path.as_os_str().to_owned();
    s.push(".seed");
    s.into()
}
fn read_page(file: &File, id: u32) -> Result<Page> {
    let mut p = Page::default();
    file.read_exact_at(&mut p.0, id as u64 * PAGE as u64)?;
    p.validate()?;
    Ok(p)
}
fn root_bytes(d: &Directory, kind: KeyKind, keep: Option<u16>) -> [u8; 64] {
    let mut b = [0u8; 64];
    b[..8].copy_from_slice(b"COLLIDX0");
    b[8..16].copy_from_slice(&d.sequence.to_le_bytes());
    b[16..24].copy_from_slice(&d.checkpoint.to_le_bytes());
    b[24..28].copy_from_slice(&d.metadata.first().copied().unwrap_or(0).to_le_bytes());
    b[28..32].copy_from_slice(&(d.metadata.len() as u32).to_le_bytes());
    b[32..36].copy_from_slice(&(d.fences.len() as u32).to_le_bytes());
    b[36..38].copy_from_slice(&keep.unwrap_or(0).to_le_bytes());
    b[38] = kind as u8;
    let crc = crc32c::crc32c(&b);
    b[40..44].copy_from_slice(&crc.to_le_bytes());
    b
}
type RootRecord = (u64, u64, u32, u32, u32, KeyKind, Option<u16>);
fn parse_root(mut b: [u8; 64]) -> Option<RootRecord> {
    let expected = u32::from_le_bytes(b[40..44].try_into().ok()?);
    b[40..44].fill(0);
    if &b[..8] != b"COLLIDX0" || crc32c::crc32c(&b) != expected {
        return None;
    }
    let kind = match b[38] {
        0 => KeyKind::Uuid,
        7 => KeyKind::V7,
        8 => KeyKind::V8,
        _ => return None,
    };
    let keep = u16::from_le_bytes(b[36..38].try_into().ok()?);
    if keep as usize > MAX_REVISIONS {
        return None;
    }
    Some((
        u64::from_le_bytes(b[8..16].try_into().ok()?),
        u64::from_le_bytes(b[16..24].try_into().ok()?),
        u32::from_le_bytes(b[24..28].try_into().ok()?),
        u32::from_le_bytes(b[28..32].try_into().ok()?),
        u32::from_le_bytes(b[32..36].try_into().ok()?),
        kind,
        (keep != 0).then_some(keep),
    ))
}
fn read_directory(
    file: &File,
    root: (u64, u64, u32, u32, u32, KeyKind, Option<u16>),
    page_limit: u32,
) -> Result<Directory> {
    let (sequence, checkpoint, mut next, count, entries, _, _) = root;
    if count > page_limit || entries as u64 > count as u64 * 204 || (count == 0) != (entries == 0) {
        return Err(Error::Corrupt("directory count"));
    }
    let mut d = Directory {
        sequence,
        checkpoint,
        ..Directory::default()
    };
    let mut seen = BTreeSet::new();
    for _ in 0..count {
        if next == 0 || next >= page_limit || !seen.insert(next) {
            return Err(Error::Corrupt("directory pointer"));
        }
        let mut raw = [0u8; PAGE];
        file.read_exact_at(&mut raw, next as u64 * PAGE as u64)?;
        let expected = u32::from_le_bytes(raw[12..16].try_into().unwrap());
        raw[12..16].fill(0);
        if &raw[..4] != b"DIR0" || crc32c::crc32c(&raw) != expected {
            return Err(Error::Corrupt("directory checksum"));
        }
        d.metadata.push(next);
        next = u32::from_le_bytes(raw[4..8].try_into().unwrap());
        let n = u16::from_le_bytes(raw[8..10].try_into().unwrap()) as usize;
        if n == 0 || n > 204 {
            return Err(Error::Corrupt("directory entries"));
        }
        for i in 0..n {
            let at = 16 + i * 20;
            d.fences.push(Fence {
                first: raw[at..at + 16].try_into().unwrap(),
                page: u32::from_le_bytes(raw[at + 16..at + 20].try_into().unwrap()),
            });
        }
    }
    if next != 0
        || d.fences.len() != entries as usize
        || d.fences.windows(2).any(|f| f[0].first >= f[1].first)
    {
        return Err(Error::Corrupt("directory ordering"));
    }
    for f in &d.fences {
        if f.page == 0 || f.page >= page_limit || !seen.insert(f.page) {
            return Err(Error::Corrupt("duplicate or invalid page"));
        }
    }
    Ok(d)
}
fn encode_command(c: Command) -> [u8; 32] {
    let mut b = [0; 32];
    b[..16].copy_from_slice(&c.object);
    b[16..18].copy_from_slice(&c.revision.to_le_bytes());
    b[18..26].copy_from_slice(&c.address.to_le_bytes());
    b[26] = u8::from(c.retire);
    let crc = crc32c::crc32c(&b[..28]);
    b[28..].copy_from_slice(&crc.to_le_bytes());
    b
}
fn decode_command(b: [u8; 32]) -> Result<Command> {
    if crc32c::crc32c(&b[..28]) != u32::from_le_bytes(b[28..].try_into().unwrap())
        || b[26] > 1
        || b[27] != 0
    {
        return Err(Error::Corrupt("seed record"));
    }
    Ok(Command {
        object: b[..16].try_into().unwrap(),
        revision: u16::from_le_bytes(b[16..18].try_into().unwrap()),
        address: u64::from_le_bytes(b[18..26].try_into().unwrap()),
        retire: b[26] != 0,
    })
}

impl Snapshot {
    fn page(&self, key: &Uuid, stats: &mut Stats) -> Result<Option<(Page, usize)>> {
        validate_key(self.kind, key)?;
        let n = self.directory.fences.partition_point(|f| f.first <= *key);
        if n == 0 {
            return Ok(None);
        }
        let mut p = Page::default();
        self.file.read_exact_at(
            &mut p.0,
            self.directory.fences[n - 1].page as u64 * PAGE as u64,
        )?;
        // Every published page is constructed here or structurally validated
        // at open. Recheck its checksum, not all objects, on each point read.
        p.verify_checksum()?;
        stats.page_reads += 1;
        Ok(p.find(key, stats).map(|at| (p, at)))
    }
    pub fn get(&self, key: &Uuid, revision: u16) -> Result<Option<u64>> {
        Ok(self
            .page(key, &mut Stats::default())?
            .and_then(|(p, i)| p.reference(i, revision)))
    }
    pub fn latest(&self, key: &Uuid) -> Result<Option<(u16, u64)>> {
        self.latest_measured(key, &mut Stats::default())
    }
    pub fn latest_measured(&self, key: &Uuid, stats: &mut Stats) -> Result<Option<(u16, u64)>> {
        Ok(self.page(key, stats)?.and_then(|(p, i)| p.latest(i)))
    }
    pub fn history(&self, key: &Uuid, visit: impl FnMut(u16, u64)) -> Result<()> {
        if let Some((p, i)) = self.page(key, &mut Stats::default())? {
            p.history(i, visit);
        }
        Ok(())
    }
    pub fn pages(&self) -> usize {
        self.directory.fences.len()
    }
    pub fn directory_bytes(&self) -> usize {
        self.directory.fences.capacity() * std::mem::size_of::<Fence>()
            + self.directory.metadata.capacity() * 4
    }
    pub fn checkpoint(&self) -> u64 {
        self.directory.checkpoint
    }
    pub fn export_pages(&self) -> Result<Vec<(u32, Page)>> {
        self.directory
            .fences
            .iter()
            .map(|f| Ok((f.page, read_page(&self.file, f.page)?)))
            .collect()
    }
}

impl Index {
    pub fn create(path: impl AsRef<Path>, kind: KeyKind, keep: Option<u16>) -> Result<Self> {
        if keep.is_some_and(|n| n == 0 || n as usize > MAX_REVISIONS) {
            return Err(Error::InvalidPolicy);
        }
        let path = path.as_ref();
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(path)?;
        file.try_lock()
            .map_err(|e| Error::Io(io::Error::other(e)))?;
        let seed = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(source_path(path))?;
        file.set_len(PAGE as u64)?;
        let d = Directory {
            sequence: 1,
            ..Directory::default()
        };
        file.write_all_at(&root_bytes(&d, kind, keep), 64)?;
        file.sync_all()?;
        seed.sync_all()?;
        File::open(
            path.parent()
                .filter(|p| !p.as_os_str().is_empty())
                .unwrap_or(Path::new(".")),
        )?
        .sync_all()?;
        let current = Arc::new(d);
        Ok(Self {
            file: Arc::new(file),
            seed,
            kind,
            keep,
            published: RwLock::new(current.clone()),
            writer: Mutex::new(Writer {
                free: vec![],
                next: 1,
                roots: [None, Some(current)],
                retired: vec![],
                failed: false,
                stats: Stats::default(),
            }),
            fault: 0.into(),
        })
    }
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let path = path.as_ref();
        let file = OpenOptions::new().read(true).write(true).open(path)?;
        file.try_lock()
            .map_err(|e| Error::Io(io::Error::other(e)))?;
        let seed = OpenOptions::new()
            .read(true)
            .write(true)
            .open(source_path(path))?;
        let pages = file.metadata()?.len() / PAGE as u64;
        if pages < 1 || pages > u32::MAX as u64 {
            return Err(Error::Corrupt("file size"));
        }
        let mut roots: [Option<Arc<Directory>>; 2] = [None, None];
        let mut policy = None;
        for (i, slot) in roots.iter_mut().enumerate() {
            let mut raw = [0; 64];
            file.read_exact_at(&mut raw, i as u64 * 64)?;
            if let Some(r) = parse_root(raw) {
                if policy.is_some_and(|p| p != (r.5, r.6)) {
                    return Err(Error::Corrupt("root policy"));
                }
                policy = Some((r.5, r.6));
                *slot = Some(Arc::new(read_directory(&file, r, pages as u32)?));
            }
        }
        let current = roots
            .iter()
            .flatten()
            .max_by_key(|d| d.sequence)
            .cloned()
            .ok_or(Error::Corrupt("no committed root"))?;
        let (kind, keep) = policy.unwrap();
        let mut live = BTreeSet::new();
        for root in roots.iter().flatten() {
            live.extend(root.pages());
        }
        let mut previous = None;
        for f in &current.fences {
            let p = read_page(&file, f.page)?;
            if p.used() == 0
                || p.first() != f.first
                || previous.is_some_and(|last| last >= p.first())
            {
                return Err(Error::Corrupt("page fence"));
            }
            let mut last = p.first();
            for i in 0..p.used() {
                if p.start(i) {
                    last = p.key(i);
                    validate_key(kind, &last)?;
                }
            }
            previous = Some(last);
        }
        let seed_count = seed.metadata()?.len() / 32;
        if current.checkpoint > seed_count {
            return Err(Error::Corrupt("checkpoint beyond seed"));
        }
        seed.set_len(seed_count * 32)?;
        file.set_len(pages * PAGE as u64)?;
        let index = Self {
            file: Arc::new(file),
            seed,
            kind,
            keep,
            published: RwLock::new(current),
            writer: Mutex::new(Writer {
                free: (1..pages as u32).filter(|p| !live.contains(p)).collect(),
                next: pages as u32,
                roots,
                retired: vec![],
                failed: false,
                stats: Stats::default(),
            }),
            fault: 0.into(),
        };
        // An older root is kept until overwritten. Its pages then become reusable.
        {
            let mut w = index.writer.lock().unwrap();
            let current = index.published.read().unwrap().clone();
            for d in w.roots.clone().into_iter().flatten() {
                if d.sequence != current.sequence {
                    let current_pages: BTreeSet<_> = current.pages().collect();
                    w.retired.push(Retired {
                        view: Arc::downgrade(&d),
                        pages: d.pages().filter(|p| !current_pages.contains(p)).collect(),
                    });
                }
            }
        }
        loop {
            let checkpoint = index.snapshot()?.checkpoint();
            if checkpoint == seed_count {
                break;
            }
            let mut batch = Vec::new();
            for n in checkpoint..seed_count.min(checkpoint + 256) {
                let mut raw = [0; 32];
                index.seed.read_exact_at(&mut raw, n * 32)?;
                batch.push(decode_command(raw)?);
            }
            index.apply_inner(&batch, true)?;
            index.writer.lock().unwrap().stats.recovery_replayed += batch.len() as u64;
        }
        Ok(index)
    }
    pub fn snapshot(&self) -> Result<Snapshot> {
        Ok(Snapshot {
            file: self.file.clone(),
            directory: self
                .published
                .read()
                .map_err(|_| Error::NeedsReopen)?
                .clone(),
            kind: self.kind,
        })
    }
    pub fn latest(&self, key: &Uuid) -> Result<Option<(u16, u64)>> {
        self.snapshot()?.latest(key)
    }
    pub fn get(&self, key: &Uuid, revision: u16) -> Result<Option<u64>> {
        self.snapshot()?.get(key, revision)
    }
    pub fn stats(&self) -> Stats {
        self.writer.lock().unwrap().stats
    }
    pub fn file_bytes(&self) -> Result<u64> {
        Ok(self.file.metadata()?.len())
    }
    pub fn apply(&self, commands: &[Command]) -> Result<()> {
        self.apply_inner(commands, false)
    }
    /// Fault injection is for the standalone recovery experiment, not production.
    pub fn inject_fault(&self, point: u8) {
        self.fault
            .store(point, std::sync::atomic::Ordering::Relaxed);
    }
    fn fault(&self, point: u8) -> Result<()> {
        if self.fault.load(std::sync::atomic::Ordering::Relaxed) == point {
            Err(Error::Injected(point))
        } else {
            Ok(())
        }
    }
    fn allocate(w: &mut Writer) -> Result<u32> {
        if let Some(id) = w.free.pods() {
            w.stats.reused_pages += 1;
            return Ok(id);
        }
        let id = w.next;
        w.next = w.next.checked_add(1).ok_or(Error::ObjectFull)?;
        w.stats.allocated_pages += 1;
        Ok(id)
    }
    fn apply_inner(&self, commands: &[Command], replay: bool) -> Result<()> {
        if commands.is_empty() {
            return Ok(());
        }
        if commands.len() > 4096 {
            return Err(Error::InvalidPolicy);
        }
        for c in commands {
            validate_key(self.kind, &c.object)?;
        }
        let mut w = self.writer.lock().map_err(|_| Error::NeedsReopen)?;
        if w.failed {
            return Err(Error::NeedsReopen);
        }
        let mut retired = std::mem::take(&mut w.retired);
        // A page can be shared by several directory generations. Waiting only
        // for the view that removed it would miss an older pinned snapshot.
        let mut pinned = BTreeSet::new();
        for d in w.roots.iter().flatten() {
            pinned.extend(d.pages());
        }
        for r in &retired {
            if let Some(d) = r.view.upgrade() {
                pinned.extend(d.pages());
            }
        }
        for mut r in retired.drain(..) {
            r.pages.retain(|id| {
                if pinned.contains(id) {
                    true
                } else {
                    w.free.push(*id);
                    false
                }
            });
            if !r.pages.is_empty() || r.view.strong_count() > 0 {
                w.retired.push(r);
            }
        }
        let current = self
            .published
            .read()
            .map_err(|_| Error::NeedsReopen)?
            .clone();
        let mut fences = current.fences.clone();
        let mut dirty = BTreeMap::<u32, Page>::new();
        let mut allocated = Vec::new();
        let prepared = (|| -> Result<()> {
            for c in commands {
                let fi = fences
                    .partition_point(|f| f.first <= c.object)
                    .saturating_sub(1);
                let old_id = fences.get(fi).map(|f| f.page);
                let private = old_id.is_some_and(|id| dirty.contains_key(&id));
                let p = if let Some(id) = old_id {
                    if let Some(p) = dirty.get(&id) {
                        p.clone()
                    } else {
                        read_page(&self.file, id)?
                    }
                } else {
                    Page::default()
                };
                let mut changed = p.clone();
                let pages = match changed.apply(c, self.keep) {
                    Ok(()) => {
                        if changed.used() == 0 {
                            vec![]
                        } else {
                            vec![changed]
                        }
                    }
                    Err(Error::PageFull)
                        if self.kind == KeyKind::V7
                            && fi + 1 == fences.len()
                            && (0..p.used())
                                .rev()
                                .find(|&i| p.start(i))
                                .is_some_and(|i| p.key(i) < c.object) =>
                    {
                        let mut tail = Page::default();
                        tail.apply(c, self.keep)?;
                        w.stats.splits += 1;
                        vec![p.clone(), tail]
                    }
                    Err(Error::PageFull) => {
                        let mut groups = Vec::new();
                        let mut i = 0;
                        let mut inserted = false;
                        while i < p.used() {
                            if !inserted && p.key(i) > c.object {
                                let mut n = Page::default();
                                n.apply(c, self.keep)?;
                                groups.push(n);
                                inserted = true;
                            }
                            let mut n = Page::default();
                            p.copy_group(i, &mut n)?;
                            if p.key(i) == c.object {
                                n.apply(c, self.keep)?;
                                inserted = true;
                            }
                            groups.push(n);
                            i = p.end(i);
                        }
                        if !inserted {
                            let mut n = Page::default();
                            n.apply(c, self.keep)?;
                            groups.push(n);
                        }
                        let mut packed = Vec::new();
                        let mut n = Page::default();
                        // Whole groups move together; no historical external pointers.
                        // Balance by occupied units while allowing a large object its own page.
                        let total: usize = groups.iter().map(|g| g.used()).sum();
                        let target = total.div_ceil(2).min(page::UNITS);
                        for g in groups {
                            if n.used() > 0
                                && (n.used() + g.used() > page::UNITS || (n.used() >= target))
                            {
                                packed.push(n);
                                n = Page::default();
                            }
                            g.copy_group(0, &mut n)?;
                        }
                        if n.used() > 0 {
                            packed.push(n);
                        }
                        w.stats.splits += 1;
                        packed
                    }
                    Err(e) => return Err(e),
                };
                if let Some(id) = old_id {
                    dirty.remove(&id);
                    fences.remove(fi);
                }
                for (offset, replacement) in pages.into_iter().enumerate() {
                    let unchanged = offset == 0 && old_id.is_some() && replacement.0 == p.0;
                    let id = if offset == 0 && (private || unchanged) {
                        old_id.unwrap()
                    } else {
                        let id = Self::allocate(&mut w)?;
                        allocated.push(id);
                        id
                    };
                    fences.insert(
                        fi + offset,
                        Fence {
                            first: replacement.first(),
                            page: id,
                        },
                    );
                    if private || !unchanged {
                        dirty.insert(id, replacement);
                    }
                }
                w.stats.peak_dirty_pages = w.stats.peak_dirty_pages.max(dirty.len());
            }
            Ok(())
        })();
        if let Err(e) = prepared {
            w.free.extend(allocated);
            return Err(e);
        }
        // Intermediate private pages were never published and can be reused now.
        for id in &allocated {
            if !dirty.contains_key(id) {
                w.free.push(*id);
            }
        }
        let transaction = (|| -> Result<()> {
            if !replay {
                if self.seed.metadata()?.len() != current.checkpoint * 32 {
                    return Err(Error::Corrupt("unrecovered seed tail"));
                }
                let mut encoded = Vec::with_capacity(commands.len() * 32);
                for c in commands {
                    encoded.extend_from_slice(&encode_command(*c));
                }
                self.seed.write_all_at(&encoded, current.checkpoint * 32)?;
                w.stats.written_bytes += encoded.len() as u64;
                self.seed.sync_all()?;
            }
            self.fault(1)?;
            for (i, (id, p)) in dirty.iter_mut().enumerate() {
                p.seal();
                if i == 0 && self.fault.load(std::sync::atomic::Ordering::Relaxed) == 2 {
                    self.file
                        .write_all_at(&p.0[..128], *id as u64 * PAGE as u64)?;
                    return Err(Error::Injected(2));
                }
                self.file.write_all_at(&p.0, *id as u64 * PAGE as u64)?;
                w.stats.written_bytes += PAGE as u64;
            }
            let mut d = Directory {
                fences,
                metadata: vec![],
                checkpoint: current.checkpoint + commands.len() as u64,
                sequence: current.sequence + 1,
            };
            for _ in 0..d.fences.len().div_ceil(204) {
                d.metadata.push(Self::allocate(&mut w)?);
            }
            for (i, chunk) in d.fences.chunks(204).enumerate() {
                let mut raw = [0u8; PAGE];
                raw[..4].copy_from_slice(b"DIR0");
                raw[4..8]
                    .copy_from_slice(&d.metadata.get(i + 1).copied().unwrap_or(0).to_le_bytes());
                raw[8..10].copy_from_slice(&(chunk.len() as u16).to_le_bytes());
                for (j, f) in chunk.iter().enumerate() {
                    let at = 16 + j * 20;
                    raw[at..at + 16].copy_from_slice(&f.first);
                    raw[at + 16..at + 20].copy_from_slice(&f.page.to_le_bytes());
                }
                let crc = crc32c::crc32c(&raw);
                raw[12..16].copy_from_slice(&crc.to_le_bytes());
                self.file
                    .write_all_at(&raw, d.metadata[i] as u64 * PAGE as u64)?;
                w.stats.written_bytes += PAGE as u64;
            }
            self.file.sync_all()?;
            self.fault(3)?;
            let slot = (d.sequence % 2) as usize;
            let root = root_bytes(&d, self.kind, self.keep);
            if self.fault.load(std::sync::atomic::Ordering::Relaxed) == 4 {
                self.file.write_all_at(&root[..32], slot as u64 * 64)?;
                self.file.sync_all()?;
                return Err(Error::Injected(4));
            }
            self.file.write_all_at(&root, slot as u64 * 64)?;
            w.stats.written_bytes += 64;
            self.file.sync_all()?;
            self.fault(5)?;
            let new_pages: BTreeSet<_> = d.pages().collect();
            w.retired.push(Retired {
                view: Arc::downgrade(&current),
                pages: current.pages().filter(|p| !new_pages.contains(p)).collect(),
            });
            let d = Arc::new(d);
            w.roots[slot] = Some(d.clone());
            *self.published.write().map_err(|_| Error::NeedsReopen)? = d;
            w.stats.commits += 1;
            self.fault(6)?;
            Ok(())
        })();
        if transaction.is_err() {
            w.failed = true;
        }
        transaction
    }
}

#[cfg(test)]
mod tests;
