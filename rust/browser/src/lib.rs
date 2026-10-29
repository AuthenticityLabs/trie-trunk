//! Minimal format-2 store. The host supplies random file I/O; no filesystem,
//! network, query engine, JSON projection, rollups, or browser bindings here.
mod mapping;
#[cfg(target_arch = "wasm32")]
mod wasm;
use mapping::Mapping;
use pil_codec::{FrameEncoder, FrameIndex};
use std::collections::{HashMap, HashSet};
pub type Result<T> = std::result::Result<T, String>;
fn bad<T>(s: &str) -> Result<T> {
    Err(s.into())
}
pub const TRI: u32 = 0;
pub const SODS: u32 = 1;
/// UUID (16), REF16 (2), payload start/end/block (8 each), all big endian.
/// Existing 44-byte U32 pointers remain readable; new pointers are 42 bytes.
pub const ROW_SIZE: usize = 42;
const LEGACY_ROW_SIZE: usize = 44;
const MAX: usize = 16 * 1024 * 1024;
pub trait Files {
    fn size(&self, file: u32) -> Result<u64>;
    fn read(&self, file: u32, at: u64, out: &mut [u8]) -> Result<()>;
    fn write(&mut self, file: u32, at: u64, data: &[u8]) -> Result<()>;
    fn truncate(&mut self, file: u32, len: u64) -> Result<()>;
    fn flush(&mut self, file: u32) -> Result<()>;
}
pub fn block(data: &[u8]) -> Result<Vec<u8>> {
    if data.is_empty() || data.len() > MAX {
        return bad("invalid block size");
    }
    let mut out = Vec::with_capacity(8 + data.len());
    out.extend_from_slice(&(data.len() as u32).to_be_bytes());
    out.extend_from_slice(&crc32c::crc32c(data).to_be_bytes());
    out.extend_from_slice(data);
    Ok(out)
}
fn init(f: &mut impl Files, id: u32) -> Result<()> {
    let expected = [b"TTRI", b"TSOD"][id as usize];
    let mut header = [0; 8];
    header[..4].copy_from_slice(expected);
    header[7] = 2;
    let size = f.size(id)?;
    if size < 8 {
        f.write(id, 0, &header)?;
        f.flush(id)?;
    } else {
        let mut actual = [0; 8];
        f.read(id, 0, &mut actual)?;
        if actual != header {
            return bad("invalid store header");
        }
    }
    Ok(())
}
/// An incomplete final append is recoverable. A complete bad-CRC block is not.
pub fn read_block(f: &impl Files, id: u32, at: u64) -> Result<Option<Vec<u8>>> {
    let size = f.size(id)?;
    if at > size {
        return bad("block outside file");
    }
    if size - at < 8 {
        return Ok(None);
    }
    let mut header = [0; 8];
    f.read(id, at, &mut header)?;
    let len = u32::from_be_bytes(header[..4].try_into().unwrap()) as usize;
    if len == 0 || len > MAX {
        return bad("invalid store block length");
    }
    if size - at - 8 < len as u64 {
        return Ok(None);
    }
    let mut bytes = vec![0; len];
    f.read(id, at + 8, &mut bytes)?;
    if crc32c::crc32c(&bytes) != u32::from_be_bytes(header[4..].try_into().unwrap()) {
        return bad("store checksum mismatch");
    }
    Ok(Some(bytes))
}
#[derive(Clone)]
struct Node {
    parent: u32,
    key: u16,
    value: u64,
}
struct Pending {
    uuid: [u8; 16],
    leaf: u32,
    payload: Vec<u8>,
}
pub struct Store {
    mapping: Mapping,
    day: u64,
    committed_nodes: usize,
    nodes: Vec<Node>,
    edges: HashMap<(u32, u16, u64), u32>,
    ends: [u64; 2],
    payload: Option<(u64, Vec<u8>)>,
    pending: Vec<Pending>,
    pending_ids: HashSet<[u8; 16]>,
    frame: FrameIndex,
    encoder: FrameEncoder,
}
impl Store {
    pub fn open(schema: &[u8], pil: &[u8], day: u64, f: &mut impl Files) -> Result<Self> {
        let mapping = Mapping::new(schema, pil)?;
        for id in [TRI, SODS] {
            init(f, id)?;
        }
        let mut s = Self {
            mapping,
            day,
            committed_nodes: 0,
            nodes: Vec::new(),
            edges: HashMap::new(),
            ends: [8; 2],
            payload: None,
            pending: Vec::new(),
            pending_ids: HashSet::new(),
            frame: FrameIndex::default(),
            encoder: FrameEncoder::default(),
        };
        s.reload(f)?;
        Ok(s)
    }
    pub fn reload(&mut self, f: &mut impl Files) -> Result<()> {
        // Reopen only when another tab/worker changed a file. The browser holds
        // an origin-wide Web Lock throughout every call and flush sequence.
        if self.ends == [f.size(TRI)?, f.size(SODS)?] && !self.nodes.is_empty() {
            return Ok(());
        }
        self.payload = None;
        self.nodes.clear();
        self.edges.clear();
        self.pending.clear();
        self.pending_ids.clear();
        self.nodes.push(Node {
            parent: 0,
            key: 0,
            value: 0,
        });
        let mut at = 8;
        while let Some(b) = read_block(f, TRI, at)? {
            if b.len() < 7 {
                return bad("invalid trie node");
            }
            let parent = u32::from_be_bytes(b[..4].try_into().unwrap());
            let key = u16::from_be_bytes(b[4..6].try_into().unwrap());
            let value = pil_codec::ObjectRef::parse(&b[6..])
                .map_err(|e| e.to_string())?
                .unsigned()
                .ok_or("invalid trie value")?;
            if parent as usize >= self.nodes.len()
                || !self.mapping.known(key)
                || self
                    .edges
                    .insert((parent, key, value), self.nodes.len() as u32)
                    .is_some()
            {
                return bad("invalid trie edge");
            }
            self.nodes.push(Node { parent, key, value });
            at += 8 + b.len() as u64;
        }
        self.committed_nodes = self.nodes.len();
        if at != f.size(TRI)? {
            return bad("incomplete committed trie");
        }
        self.ends[TRI as usize] = at;
        // The host first recovers both files to IndexedDB's committed ends.
        // Payload CRCs are checked lazily, once per block actually read.
        self.ends[SODS as usize] = f.size(SODS)?;
        Ok(())
    }
    pub fn put(&mut self, frame: &[u8]) -> Result<()> {
        let (uuid, chain, mut payload) = self.mapping.split(frame, &mut self.frame)?;
        if day(&uuid) != self.day {
            return bad("event routed to wrong day");
        }
        if self.pending_ids.contains(&uuid) {
            return Ok(());
        }
        let mut parent = 0;
        let mut missing = None;
        for (i, &(key, value)) in chain.iter().enumerate() {
            if let Some(id) = self.edges.get(&(parent, key, value)) {
                parent = *id;
            } else {
                missing = Some(i);
                break;
            }
        }
        // Check capacity before appending any nodes. At capacity, preserve the
        // complete lineage as payload fields; never wrap or drop an event.
        if parent > u16::MAX as u32
            || missing.is_some_and(|i| self.nodes.len() + chain.len() - i > 65536)
        {
            let value: ciborium::Value =
                pil_codec::object::decode_value(payload.as_slice()).map_err(|e| e.to_string())?;
            let ciborium::Value::Map(mut fields) = value else {
                return bad("invalid payload map");
            };
            for &(key, value) in &chain {
                let k = ciborium::Value::Integer(key.into());
                if fields.iter().any(|(old, _)| *old == k) {
                    return bad("duplicate lineage field");
                }
                fields.push((k, ciborium::Value::Integer(value.into())));
            }
            fields.sort_by_key(|(key, _)| key.as_integer().map(i128::from));
            payload = pil_codec::object::encode_value(&ciborium::Value::Map(fields))
                .map_err(|e| e.to_string())?;
            if payload.len() > 65535 {
                return bad("payload too large");
            }
            parent = 0;
        } else if let Some(first) = missing {
            for &(key, value) in &chain[first..] {
                let id = self.nodes.len() as u32;
                self.edges.insert((parent, key, value), id);
                self.nodes.push(Node { parent, key, value });
                parent = id;
            }
        }
        self.pending_ids.insert(uuid);
        self.pending.push(Pending {
            uuid,
            leaf: parent,
            payload,
        });
        Ok(())
    }
    /// Flush payload files and return packed pointer rows for an external index.
    /// Host deduplicates UUIDs before put, then atomically publishes rows and file ends.
    pub fn commit(&mut self, f: &mut impl Files) -> Result<Vec<u8>> {
        let mut rows = Vec::with_capacity(self.pending.len() * ROW_SIZE);
        let mut at = self.ends[TRI as usize];
        for n in &self.nodes[self.committed_nodes..] {
            let mut b = Vec::new();
            b.extend_from_slice(&n.parent.to_be_bytes());
            b.extend_from_slice(&n.key.to_be_bytes());
            b.extend_from_slice(
                &pil_codec::object::encode_value(&ciborium::Value::Integer(n.value.into()))
                    .map_err(|e| e.to_string())?,
            );
            let end = at + 8 + b.len() as u64;
            f.write(TRI, at, &block(&b)?)?;
            at = end;
        }
        f.flush(TRI)?;
        self.ends[TRI as usize] = at;
        self.committed_nodes = self.nodes.len();
        self.pending.sort_unstable_by_key(|r| r.uuid);
        let mut offset = 0;
        while offset < self.pending.len() {
            let mut finish = offset;
            let mut size = 0;
            while finish < self.pending.len() && size + self.pending[finish].payload.len() <= MAX {
                size += self.pending[finish].payload.len();
                finish += 1;
            }
            let mut payload = Vec::with_capacity(size);
            for r in &self.pending[offset..finish] {
                let start = self.ends[SODS as usize] + 8 + payload.len() as u64;
                payload.extend_from_slice(&r.payload);
                rows.extend_from_slice(&r.uuid);
                rows.extend_from_slice(
                    &u16::try_from(r.leaf)
                        .map_err(|_| "trie reference exceeds U16")?
                        .to_be_bytes(),
                );
                rows.extend_from_slice(&start.to_be_bytes());
                rows.extend_from_slice(&(start + r.payload.len() as u64).to_be_bytes());
                rows.extend_from_slice(&self.ends[SODS as usize].to_be_bytes());
            }
            f.write(SODS, self.ends[SODS as usize], &block(&payload)?)?;
            f.flush(SODS)?;
            self.ends[SODS as usize] += 8 + payload.len() as u64;
            offset = finish;
        }
        self.pending.clear();
        self.pending_ids.clear();
        Ok(rows)
    }
    pub fn lineage(&self, mut leaf: u32) -> Result<Vec<u8>> {
        let mut out = Vec::new();
        while leaf != 0 {
            let n = self.nodes.get(leaf as usize).ok_or("invalid leaf")?;
            out.extend_from_slice(&n.key.to_be_bytes());
            out.extend_from_slice(&n.value.to_be_bytes());
            leaf = n.parent;
            if out.len() > 320 {
                return bad("invalid lineage depth");
            }
        }
        Ok(out)
    }
    pub fn get(&mut self, row: &[u8], f: &impl Files) -> Result<Vec<u8>> {
        if row.len() != ROW_SIZE && row.len() != LEGACY_ROW_SIZE {
            return bad("invalid pointer row");
        }
        let uuid: [u8; 16] = row[..16].try_into().unwrap();
        if uuid[6] >> 4 != 7 || uuid[8] >> 6 != 2 || day(&uuid) != self.day {
            return bad("invalid event reference");
        }
        let ref_width = if row.len() == ROW_SIZE { 2 } else { 4 };
        let offsets = 16 + ref_width;
        let sods_at = u64::from_be_bytes(row[offsets + 16..offsets + 24].try_into().unwrap());
        let mut leaf = if ref_width == 2 {
            u32::from(u16::from_be_bytes(row[16..18].try_into().unwrap()))
        } else {
            u32::from_be_bytes(row[16..20].try_into().unwrap())
        };
        let mut chain = Vec::new();
        while leaf != 0 {
            let n = self.nodes.get(leaf as usize).ok_or("invalid leaf")?;
            chain.push((n.key, n.value));
            leaf = n.parent;
            if chain.len() > 32 {
                return bad("invalid lineage depth");
            }
        }
        chain.reverse();
        let start = u64::from_be_bytes(row[offsets..offsets + 8].try_into().unwrap());
        let end = u64::from_be_bytes(row[offsets + 8..offsets + 16].try_into().unwrap());
        if sods_at < 8
            || start < sods_at.checked_add(8).ok_or("invalid block offset")?
            || end <= start
            || end - start > 65535
            || end > self.ends[SODS as usize]
        {
            return bad("invalid payload range");
        }
        if self.payload.as_ref().map(|(at, _)| *at) != Some(sods_at) {
            self.payload = Some((
                sods_at,
                read_block(f, SODS, sods_at)?.ok_or("missing payload block")?,
            ));
        }
        let payload = &self.payload.as_ref().unwrap().1;
        let start = usize::try_from(start - sods_at - 8).map_err(|_| "invalid payload range")?;
        let end = usize::try_from(end - sods_at - 8).map_err(|_| "invalid payload range")?;
        self.mapping.join(
            uuid,
            &chain,
            payload.get(start..end).ok_or("invalid payload range")?,
            &mut self.encoder,
            &mut self.frame,
        )
    }
}
pub fn day(uuid: &[u8; 16]) -> u64 {
    let mut ts = [0; 8];
    ts[2..].copy_from_slice(&uuid[..6]);
    u64::from_be_bytes(ts) / 86_400_000
}
