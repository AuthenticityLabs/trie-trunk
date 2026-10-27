//! Immutable, lazy file reads. The worker supplies synchronous OPFS reads;
//! neither the complete index nor expanded events are copied into JS memory.
use serde::Deserialize;
use std::{
    collections::BTreeMap,
    io,
    ops::Range,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
};
use tantivy::directory::error::{DeleteError, LockError, OpenReadError, OpenWriteError};
use tantivy::directory::{
    Directory, DirectoryLock, FileHandle, Lock, OwnedBytes, WatchCallback, WatchHandle, WritePtr,
};
use tantivy_common::HasLen;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FileSpec {
    pub id: u32,
    pub name: String,
    pub len: usize,
}

#[derive(Debug)]
struct File {
    context: u32,
    id: u32,
    len: usize,
    cache: Arc<Mutex<BlockCache>>,
}
const BLOCK: usize = 64 * 1024;
const CACHE_BLOCKS: usize = 128; // At most 8 MiB of retained file blocks per reader.
#[derive(Debug, Default)]
struct BlockCache {
    clock: u64,
    blocks: BTreeMap<(u32, usize), (u64, OwnedBytes)>,
}
impl File {
    fn block(&self, start: usize) -> io::Result<OwnedBytes> {
        let key = (self.id, start);
        let mut cache = self
            .cache
            .lock()
            .map_err(|_| io::Error::other("Index cache poisoned"))?;
        cache.clock += 1;
        let clock = cache.clock;
        if let Some((used, bytes)) = cache.blocks.get_mut(&key) {
            *used = clock;
            return Ok(bytes.clone());
        }
        drop(cache);
        let mut bytes = vec![0; BLOCK.min(self.len - start)];
        crate::read(self.context, self.id, start, &mut bytes)?;
        let bytes = OwnedBytes::new(bytes);
        let mut cache = self
            .cache
            .lock()
            .map_err(|_| io::Error::other("Index cache poisoned"))?;
        if cache.blocks.len() >= CACHE_BLOCKS {
            if let Some(old) = cache
                .blocks
                .iter()
                .min_by_key(|(_, (used, _))| used)
                .map(|(key, _)| *key)
            {
                cache.blocks.remove(&old);
            }
        }
        cache.blocks.insert(key, (clock, bytes.clone()));
        Ok(bytes)
    }
}
impl HasLen for File {
    fn len(&self) -> usize {
        self.len
    }
}
impl FileHandle for File {
    fn read_bytes(&self, range: Range<usize>) -> io::Result<OwnedBytes> {
        if range.start > range.end || range.end > self.len {
            return Err(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "Index read outside file",
            ));
        }
        if range.is_empty() {
            return Ok(OwnedBytes::empty());
        }
        let first = range.start / BLOCK * BLOCK;
        if range.end <= first + BLOCK {
            return Ok(self
                .block(first)?
                .slice(range.start - first..range.end - first));
        }
        let mut bytes = Vec::with_capacity(range.len());
        for start in (first..range.end).step_by(BLOCK) {
            let block = self.block(start)?;
            let a = range.start.saturating_sub(start);
            let b = (range.end - start).min(block.len());
            bytes.extend_from_slice(&block[a..b]);
        }
        Ok(OwnedBytes::new(bytes))
    }
}

#[derive(Debug, Clone)]
pub struct ReadOnlyDirectory {
    files: Arc<BTreeMap<PathBuf, Arc<File>>>,
}
impl ReadOnlyDirectory {
    pub fn new(context: u32, specs: Vec<FileSpec>) -> crate::Result<Self> {
        let mut files = BTreeMap::new();
        let cache = Arc::new(Mutex::new(BlockCache::default()));
        let mut ids = std::collections::HashSet::new();
        if specs.len() > 4096 {
            return Err("Too many index files".into());
        }
        for f in specs {
            if f.name.is_empty()
                || f.name == "."
                || f.name == ".."
                || f.name.contains(['/', '\\'])
                || f.len > 0x7fff_ffff
            {
                return Err("Invalid immutable index file".into());
            }
            if !ids.insert(f.id) {
                return Err("Duplicate index file handle".into());
            }
            if files
                .insert(
                    PathBuf::from(f.name),
                    Arc::new(File {
                        context,
                        id: f.id,
                        len: f.len,
                        cache: cache.clone(),
                    }),
                )
                .is_some()
            {
                return Err("Duplicate index file".into());
            }
        }
        Ok(Self {
            files: Arc::new(files),
        })
    }
}
fn denied() -> io::Error {
    io::Error::new(
        io::ErrorKind::PermissionDenied,
        "Replicated index is immutable",
    )
}
impl Directory for ReadOnlyDirectory {
    fn get_file_handle(&self, path: &Path) -> Result<Arc<dyn FileHandle>, OpenReadError> {
        self.files
            .get(path)
            .map(|f| f.clone() as Arc<dyn FileHandle>)
            .ok_or_else(|| OpenReadError::FileDoesNotExist(path.to_owned()))
    }
    fn exists(&self, path: &Path) -> Result<bool, OpenReadError> {
        Ok(self.files.contains_key(path))
    }
    fn atomic_read(&self, path: &Path) -> Result<Vec<u8>, OpenReadError> {
        let f = self.get_file_handle(path)?;
        f.read_bytes(0..f.len())
            .map(|b| b.to_vec())
            .map_err(|e| OpenReadError::wrap_io_error(e, path.to_owned()))
    }
    fn delete(&self, path: &Path) -> Result<(), DeleteError> {
        Err(DeleteError::IoError {
            io_error: Arc::new(denied()),
            filepath: path.to_owned(),
        })
    }
    fn open_write(&self, path: &Path) -> Result<WritePtr, OpenWriteError> {
        Err(OpenWriteError::wrap_io_error(denied(), path.to_owned()))
    }
    fn atomic_write(&self, _: &Path, _: &[u8]) -> io::Result<()> {
        Err(denied())
    }
    fn sync_directory(&self) -> io::Result<()> {
        Err(denied())
    }
    // The manifest and its files cannot change while this reader is alive.
    // No garbage collector or index writer operates on this directory.
    fn acquire_lock(&self, _: &Lock) -> Result<DirectoryLock, LockError> {
        Ok(DirectoryLock::from(Box::new(())))
    }
    fn watch(&self, _: WatchCallback) -> tantivy::Result<WatchHandle> {
        Ok(WatchHandle::empty())
    }
}
