use ciborium::Value;
use serde_json::Value as Json;
use std::cell::RefCell;
use std::cmp::Ordering;
use std::collections::{BTreeMap, HashMap};
use std::fs::{self, File};
use std::io::{self, Read};
use std::os::unix::fs::{FileExt, MetadataExt};
use std::path::{Path, PathBuf};
use std::sync::{
    Arc, Mutex, RwLock,
    atomic::{AtomicBool, Ordering as AtomicOrdering},
};

use crate::disk_rows::Rows;
use crate::filter::{BoundFilter, Decision, Filter};
use crate::postings::{Positions, Postings};
use crate::routing::Partition;
use crate::schema::{self, Schema};
use crate::{IndexRow, timestamp};

const MAX_BLOCK: u64 = 16 << 20;
const MAX_RECORD: u64 = 65535;
const ROW_BUFFER: usize = 256;
const LINEAGE_CACHE_BYTES: usize = 4 << 20;
pub(crate) const DAY_MILLIS: u64 = 86_400_000;
pub(crate) type Catalogs = BTreeMap<PathBuf, Arc<RwLock<Catalog>>>;

pub(crate) fn invalid(error: impl std::fmt::Display) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, error.to_string())
}

pub(crate) fn u32be(bytes: &[u8]) -> u32 {
    u32::from_be_bytes(bytes[..4].try_into().unwrap())
}

pub(crate) fn u64be(bytes: &[u8]) -> u64 {
    u64::from_be_bytes(bytes[..8].try_into().unwrap())
}

pub(crate) fn valid_tap(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= 64
        && name.bytes().enumerate().all(|(i, c)| {
            c.is_ascii_lowercase() || c.is_ascii_digit() || i > 0 && matches!(c, b'.' | b'_' | b'-')
        })
}

fn object_major(name: &str) -> Option<(&str, u32)> {
    let (object, major) = name.rsplit_once(".v")?;
    let n = major.parse::<u32>().ok()?;
    (valid_tap(object) && n > 0 && n < 4096 && n.to_string() == major).then_some((object, n))
}

pub(crate) fn schema_identity(path: &Path) -> io::Result<(String, Option<String>, u32)> {
    let parent = path
        .parent()
        .ok_or_else(|| invalid("missing tap directory"))?;
    let name = path
        .file_name()
        .and_then(|s| s.to_str())
        .ok_or_else(|| invalid("invalid schema directory"))?;
    let n = name.parse::<u32>().map_err(invalid)?;
    if n.to_string() != name {
        return Err(invalid("noncanonical schema directory"));
    }
    let parent_name = parent
        .file_name()
        .and_then(|s| s.to_str())
        .ok_or_else(|| invalid("invalid tap directory"))?;
    // An object directory lives underneath a tap; a named definition is verified
    // against events.map below, rather than inferred from its version alone.
    if let Some((object, major)) = object_major(parent_name) {
        if n >= (1 << 20) {
            return Err(invalid("invalid definition minor"));
        }
        let tap = parent
            .parent()
            .and_then(Path::file_name)
            .and_then(|s| s.to_str())
            .ok_or_else(|| invalid("missing tap"))?;
        return Ok((tap.to_owned(), Some(object.to_owned()), major << 20 | n));
    }
    if n == 0 {
        return Err(invalid("invalid legacy definition version"));
    }
    Ok((parent_name.to_owned(), None, n))
}

pub(crate) fn validate_schema_path(path: &Path, data: &[u8]) -> io::Result<(String, u32)> {
    let definition = schema::record(schema::decode(data)?)?;
    let declared = match definition.get(&8) {
        Some(Value::Text(name)) => Some(name.clone()),
        None => None,
        _ => return Err(invalid("invalid object identity")),
    };
    let (tap, object, version) = if declared.is_some() {
        schema_identity(path)?
    } else {
        let tap = path
            .parent()
            .and_then(Path::file_name)
            .and_then(|s| s.to_str())
            .ok_or_else(|| invalid("invalid legacy tap"))?;
        let number = path
            .file_name()
            .and_then(|s| s.to_str())
            .and_then(|s| s.parse::<u32>().ok())
            .filter(|&n| n > 0)
            .ok_or_else(|| invalid("invalid legacy version"))?;
        (tap.to_owned(), None, number)
    };
    if declared != object {
        return Err(invalid("object identity disagrees with schema directory"));
    }
    Ok((tap, version))
}

pub(crate) fn versions(root: &Path, tap: &str) -> io::Result<Vec<PathBuf>> {
    let mut taps = Vec::new();
    if tap.is_empty() {
        for entry in fs::read_dir(root)? {
            let entry = entry?;
            if entry.file_type()?.is_dir() && valid_tap(&entry.file_name().to_string_lossy()) {
                taps.push(entry.path());
            }
        }
    } else {
        if !valid_tap(tap) {
            return Err(invalid("invalid tap name"));
        }
        let dir = root.join(tap);
        if !fs::symlink_metadata(&dir)?.is_dir() {
            return Err(invalid(
                "tap must be a directory, not a file or symbolic link",
            ));
        }
        taps.push(dir);
    }
    let mut versions = Vec::new();
    for tap in taps {
        for entry in fs::read_dir(tap)? {
            let entry = entry?;
            let name = entry.file_name();
            let name = name.to_string_lossy();
            if entry.file_type()?.is_dir() && object_major(&name).is_some() {
                for minor in fs::read_dir(entry.path())? {
                    let minor = minor?;
                    let label = minor.file_name();
                    let label = label.to_string_lossy();
                    if minor.file_type()?.is_dir()
                        && let Ok(n) = label.parse::<u32>()
                        && n < (1 << 20)
                        && n.to_string() == label
                    {
                        versions.push(minor.path());
                    }
                }
            } else if entry.file_type()?.is_dir()
                && let Ok(n) = name.parse::<u32>()
                && n != 0
                && n.to_string() == name
            {
                versions.push(entry.path());
            }
        }
    }
    versions.sort_unstable();
    Ok(versions)
}

pub(crate) struct Snapshot {
    pub(crate) format: u32,
    compact: bool,
    is_index: bool,
    pub(crate) file: std::sync::Arc<File>,
    size: u64,
    identity: (u64, u64),
}

impl Snapshot {
    pub(crate) fn open(path: &Path, magic: &[u8; 4]) -> io::Result<Self> {
        let file = File::open(path)?;
        let metadata = file.metadata()?;
        let mut header = [0; 8];
        file.read_exact_at(&mut header, 0)?;
        let format = u32be(&header[4..]);
        let index3 = magic == b"TIDX"
            && matches!(format, 3 | 4)
            && (&header[..4] == b"TIDA" || &header[..4] == b"TIDC");
        if !index3
            && (&header[..4] != magic
                || !matches!(format, 2 | 3 | 4)
                || magic == b"TIDX" && format != 2)
        {
            return Err(invalid(format!("file header mismatch: {}", path.display())));
        }
        Ok(Self {
            format,
            is_index: magic == b"TIDX",
            compact: index3 && &header[..4] == b"TIDC",
            file: std::sync::Arc::new(file),
            size: metadata.len(),
            identity: (metadata.dev(), metadata.ino()),
        })
    }

    fn scan(
        &self,
        mut offset: u64,
        buffer: &mut Vec<u8>,
        mut visit: impl FnMut(u64, &[u8], bool) -> io::Result<()>,
    ) -> io::Result<u64> {
        if offset > self.size {
            return Err(invalid("file truncated while following"));
        }
        while self.size - offset >= 8 {
            crate::runtime::check()?;
            if self.is_index
                && let Some(data) = crate::index_cache::get(self.identity, offset, self.size)
            {
                visit(offset, &data, true)?;
                offset += data.len() as u64 + 8;
                continue;
            }
            let mut header = [0; 8];
            self.file.read_exact_at(&mut header, offset)?;
            let size = u64::from(u32be(&header));
            if size == 0 || size > MAX_BLOCK {
                return Err(invalid(format!("invalid disk block length at {offset}")));
            }
            if self.size - offset - 8 < size {
                break;
            }
            buffer.resize(size as usize, 0);
            self.file.read_exact_at(buffer, offset + 8)?;
            if crc32c::crc32c(buffer) != u32be(&header[4..]) {
                return Err(invalid(format!("checksum failure at {offset}")));
            }
            visit(offset, buffer, false)?;
            if self.is_index
                && !(matches!(self.format, 3 | 4) && offset == 8)
                && let Ok(rows) = Rows::parse(buffer, self.compact, self.format == 4)
            {
                crate::index_cache::insert(
                    &self.file,
                    self.identity,
                    offset,
                    rows.time(rows.len() - 1),
                    buffer,
                )?;
            }
            offset += size + 8;
        }
        Ok(offset)
    }
}

struct Node {
    parent: u32,
    key: u16,
    value: u64,
}

pub(crate) struct Catalog {
    pub stream: usize,
    compact: bool,
    pub tap: String,
    pub version: u32,
    pub schema: Schema,
    pub definition_bytes: Vec<u8>,
    nodes: Vec<Node>,
    owners: Vec<u32>,
    trie: Snapshot,
    offset: u64,
    filter: Option<BoundFilter>,
    lineages: HashMap<(u32, bool), Arc<Lineage>>,
    lineage_bytes: usize,
}

pub(crate) struct Lineage {
    pub fields: Vec<(String, Json)>,
    pub keys: Box<[u16]>,
    pub cbor_values: Vec<Vec<u8>>,
    duration: Option<u64>,
}

impl Catalog {
    fn load(version: &Path, filter: &Filter, stream: usize, compact: bool) -> io::Result<Self> {
        let file = File::open(schema_file(version, "map")?)?;
        let size = file.metadata()?.len();
        if size == 0 || size > MAX_RECORD {
            return Err(invalid("invalid schema length"));
        }
        let mut data = Vec::with_capacity(size as usize);
        file.take(MAX_RECORD + 1).read_to_end(&mut data)?;
        if data.len() as u64 != size {
            return Err(invalid("schema length changed"));
        }
        let (tap, number) = validate_schema_path(version, &data)?;
        let schema = Schema::parse(&data, number)?;
        Ok(Self {
            stream,
            compact,
            tap,
            version: number,
            lineages: HashMap::new(),
            lineage_bytes: 0,
            filter: filter.bind(&schema)?,
            schema,
            definition_bytes: data,
            owners: vec![0],
            nodes: vec![Node {
                parent: 0,
                key: 0,
                value: 0,
            }],
            trie: Snapshot::open(&schema_file(version, "tri")?, b"TTRI")?,
            offset: 8,
        })
    }

    fn rebind(&mut self, filter: &Filter, stream: usize, compact: bool) -> io::Result<()> {
        self.stream = stream;
        if self.compact != compact {
            self.lineages.clear();
            self.lineage_bytes = 0;
            self.compact = compact;
        }
        self.filter = filter.bind(&self.schema)?;
        if let Some(filter) = &mut self.filter {
            for node in self.nodes.iter().skip(1) {
                filter.push(node.parent, node.key, node.value, &self.schema);
            }
        }
        Ok(())
    }

    fn refresh(&mut self) -> io::Result<()> {
        self.trie.size = self.trie.file.metadata()?.len();
        let old_len = self.nodes.len();
        let next = self.trie.scan(self.offset, &mut Vec::new(), |_, data, _| {
            if data.len() < 7 || self.trie.format == 4 && self.nodes.len() >= 1 << 16 {
                return Err(invalid("short trie node"));
            }
            let parent = u32be(data);
            let key = u16::from_be_bytes(data[4..6].try_into().unwrap());
            if parent as usize >= self.nodes.len() || !self.schema.contains(key) {
                return Err(invalid("invalid trie parent or field"));
            }
            let value = schema::unsigned(&schema::decode(&data[6..])?)?;
            if let Some(filter) = &mut self.filter {
                filter.push(parent, key, value, &self.schema);
            }
            self.owners.push(if self.schema.boundary == Some(key) {
                self.nodes.len() as u32
            } else {
                self.owners[parent as usize]
            });
            self.nodes.push(Node { parent, key, value });
            Ok(())
        });
        match next {
            Ok(offset) => self.offset = offset,
            Err(error) => {
                self.nodes.truncate(old_len);
                self.owners.truncate(old_len);
                return Err(error);
            }
        }
        Ok(())
    }

    fn lineage(&mut self, leaf: u32, raw: bool) -> io::Result<Arc<Lineage>> {
        if let Some(lineage) = self.lineages.get(&(leaf, raw)) {
            return Ok(Arc::clone(lineage));
        }
        let mut fields = Vec::new();
        let mut keys = Vec::new();
        let mut cbor_values = Vec::new();
        let mut duration = None;
        let mut parent = leaf;
        while parent != 0 {
            let node = self
                .nodes
                .get(parent as usize)
                .ok_or_else(|| invalid("invalid trie leaf"))?;
            if keys.contains(&node.key) {
                return Err(invalid("duplicate trie field"));
            }
            if raw {
                let mut encoded = Vec::new();
                ciborium::ser::into_writer(&node.value, &mut encoded).map_err(invalid)?;
                cbor_values.push(encoded);
            } else {
                let (name, value) = if self.compact {
                    (node.key.to_string(), Json::from(node.value))
                } else {
                    self.schema
                        .expand_field(node.key, Value::Integer(node.value.into()))?
                };
                if fields.iter().any(|(existing, _)| existing == &name) {
                    return Err(invalid("duplicate projected field name"));
                }
                fields.push((name, value));
            }
            keys.push(node.key);
            if Some(node.key) == self.schema.duration {
                duration = Some(node.value);
            }
            parent = node.parent;
        }
        fields.reverse();
        keys.reverse();
        cbor_values.reverse();
        let keys = keys.into_boxed_slice();
        let bytes = cbor_values.capacity() * std::mem::size_of::<Vec<u8>>()
            + cbor_values.iter().map(Vec::capacity).sum::<usize>()
            + std::mem::size_of::<Lineage>()
            + std::mem::size_of_val(keys.as_ref())
            + fields.capacity() * std::mem::size_of::<(String, Json)>()
            + fields
                .iter()
                .map(|(name, value)| name.len() + value.as_str().map_or(0, str::len))
                .sum::<usize>();
        let lineage = Arc::new(Lineage {
            fields,
            keys,
            cbor_values,
            duration,
        });
        if bytes <= LINEAGE_CACHE_BYTES.saturating_sub(self.lineage_bytes) {
            self.lineage_bytes += bytes;
            self.lineages.insert((leaf, raw), Arc::clone(&lineage));
        }
        Ok(lineage)
    }
}

#[derive(Clone, Copy)]
struct Row {
    bytes: IndexRow,
    leaf: u32,
    start: u64,
    end: u64,
}

impl Row {
    fn decode(bytes: IndexRow) -> io::Result<Self> {
        let row = Self {
            leaf: u32be(&bytes[16..]),
            start: u64be(&bytes[20..]),
            end: u64be(&bytes[28..]),
            bytes,
        };
        if bytes[6] >> 4 != 7
            || bytes[8] >> 6 != 2
            || row.start < 16
            || row.end <= row.start
            || row.end - row.start > MAX_RECORD
            || row.end > i64::MAX as u64
        {
            return Err(invalid("invalid index row"));
        }
        Ok(row)
    }
}

#[derive(Clone)]
pub(crate) struct Cursor {
    row: Row,
    pub source: usize,
    offset: u64,
    run: Arc<Run>,
    positions: Arc<Positions>,
    position: usize,
    backwards: bool,
    needs_filter: bool,
}

struct Run {
    cached: ((u64, u64), u64, u64),
    offset: u64,
    width: usize,
    count: usize,
    buffer: Mutex<RowBuffer>,
    population_start: u64,
    population_end: u64,
    validated: AtomicBool,
}

struct RowBuffer {
    first: usize,
    rows: Vec<IndexRow>,
}

impl Run {
    fn row(&self, file: &File, position: usize) -> io::Result<Row> {
        let mut buffer = self.buffer.lock().unwrap();
        if position < buffer.first || position >= buffer.first + buffer.rows.len() {
            buffer.first = position / ROW_BUFFER * ROW_BUFFER;
            let count = ROW_BUFFER.min(self.count - buffer.first);
            buffer.rows.resize(count, [0; 36]);
            let offset = self.offset + buffer.first as u64 * self.width as u64;
            if let Some(bytes) =
                crate::index_cache::get(self.cached.0, self.cached.1, self.cached.2)
            {
                let rows = Rows::parse(
                    &bytes,
                    matches!(self.width, 26 | 28),
                    matches!(self.width, 26 | 34),
                )?;
                let first = buffer.first;
                for (i, out) in buffer.rows.iter_mut().enumerate() {
                    *out = rows.row(first + i);
                }
            } else if self.width == 36 {
                file.read_exact_at(buffer.rows.as_flattened_mut(), offset)?;
            } else {
                let mut raw = vec![0; count * self.width];
                file.read_exact_at(&mut raw, offset)?;
                let base = if self.width == 34 {
                    0
                } else if buffer.first == 0 {
                    self.population_start
                } else {
                    let mut b = [0; 8];
                    file.read_exact_at(&mut b, offset - 8)?;
                    u64be(&b)
                };
                let rows = Rows {
                    data: &raw,
                    width: self.width,
                    base,
                    header: if self.width == 34 { 4 } else { 12 },
                };
                for (i, out) in buffer.rows.iter_mut().enumerate() {
                    *out = rows.row(i)
                }
            }
        }
        Row::decode(buffer.rows[position - buffer.first])
    }
}

impl Cursor {
    pub fn validate(&mut self, source: &Source, scratch: &mut [u8]) -> io::Result<()> {
        crate::runtime::check()?;
        if !self.run.validated.load(AtomicOrdering::Relaxed) {
            check_population(
                &source.pop,
                self.run.population_start,
                self.run.population_end,
                scratch,
            )?;
            self.run.validated.store(true, AtomicOrdering::Relaxed);
        }
        Ok(())
    }

    pub fn advance(&mut self, source: &Source) -> io::Result<bool> {
        loop {
            if self.backwards {
                if self.position == 0 {
                    return Ok(false);
                }
                self.position -= 1;
            } else {
                self.position += 1;
                if self.position == self.positions.len() {
                    return Ok(false);
                }
            }
            let position = self.positions.at(self.position);
            self.offset = self.run.offset + position as u64 * self.run.width as u64;
            self.row = self.run.row(&source.idx.file, position)?;
            if !self.needs_filter || source.decision(self)? != Decision::Exclude {
                return Ok(true);
            }
        }
    }

    pub fn seek(&mut self, source: &Source) -> io::Result<bool> {
        if self.needs_filter && source.decision(self)? == Decision::Exclude {
            self.advance(source)
        } else {
            Ok(true)
        }
    }
}
impl Ord for Cursor {
    fn cmp(&self, other: &Self) -> Ordering {
        let order = (&self.row.bytes[..16], self.source, self.offset).cmp(&(
            &other.row.bytes[..16],
            other.source,
            other.offset,
        ));
        if self.backwards {
            order.reverse()
        } else {
            order
        }
    }
}
impl PartialOrd for Cursor {
    fn partial_cmp(&self, other: &Self) -> Option<Ordering> {
        Some(self.cmp(other))
    }
}
impl PartialEq for Cursor {
    fn eq(&self, other: &Self) -> bool {
        self.cmp(other) == Ordering::Equal
    }
}
impl Eq for Cursor {}

pub(crate) struct Source {
    idx: Snapshot,
    pop: Snapshot,
    pub catalog: Arc<RwLock<Catalog>>,
}

pub(crate) struct Record {
    pub fields: BTreeMap<u16, Value>,
    pub lineage: Arc<Lineage>,
    pub millis: u64,
    pub id: u16,
}

pub(crate) struct RawRecord<'a> {
    pub fields: Vec<(u16, pil_codec::CborRef<'a>)>,
    pub lineage: Arc<Lineage>,
    pub uuid: [u8; 16],
    pub id: u16,
}
impl RawRecord<'_> {
    pub fn field(&self, key: u16) -> Option<pil_codec::CborRef<'_>> {
        if let Some(index) = self.lineage.keys.iter().position(|&k| k == key) {
            // These small integer encodings are cached with the lineage.
            return Some(
                pil_codec::CborRef::parse(&self.lineage.cbor_values[index])
                    .expect("encoded trie integer"),
            );
        }
        self.fields
            .binary_search_by_key(&key, |&(k, _)| k)
            .ok()
            .map(|i| self.fields[i].1)
    }
}

impl Source {
    fn decision(&self, cursor: &Cursor) -> io::Result<Decision> {
        let catalog = self.catalog.read().unwrap();
        match &catalog.filter {
            Some(filter) => filter.decision(cursor.row.leaf),
            None => Ok(Decision::Include),
        }
    }

    fn population(&self, cursor: &Cursor, buffer: &mut [u8]) -> io::Result<BTreeMap<u16, Value>> {
        let row = &cursor.row;
        let data = &mut buffer[..(row.end - row.start) as usize];
        self.pop.file.read_exact_at(data, row.start)?;
        let mut fields = schema::record(schema::decode(data)?)?;
        fields.insert(
            self.catalog.read().unwrap().schema.id,
            Value::Bytes(row.bytes[..16].to_vec()),
        );
        Ok(fields)
    }

    fn accepts(&self, cursor: &Cursor, fields: &BTreeMap<u16, Value>) -> bool {
        let catalog = self.catalog.read().unwrap();
        catalog
            .filter
            .as_ref()
            .is_none_or(|filter| filter.matches(cursor.row.leaf, &catalog.schema, fields))
    }

    pub fn matches(&self, cursor: &mut Cursor, buffer: &mut [u8]) -> io::Result<bool> {
        match self.decision(cursor)? {
            Decision::Exclude => Ok(false),
            Decision::Include => Ok(true),
            Decision::Population => {
                cursor.validate(self, buffer)?;
                Ok(self.accepts(cursor, &self.population(cursor, buffer)?))
            }
        }
    }

    pub fn read_pil<'a>(
        &self,
        cursor: &mut Cursor,
        buffer: &'a mut [u8],
    ) -> io::Result<Option<RawRecord<'a>>> {
        let decision = self.decision(cursor)?;
        if decision == Decision::Exclude {
            return Ok(None);
        }
        cursor.validate(self, buffer)?;
        let row = &cursor.row;
        let data = &mut buffer[..(row.end - row.start) as usize];
        self.pop.file.read_exact_at(data, row.start)?;
        // Population predicates retain the existing evaluator. Unfiltered and
        // trie-only searches never construct population objects on the PIL path.
        if decision == Decision::Population {
            let mut fields = schema::record(schema::decode(data)?)?;
            fields.insert(
                self.catalog.read().unwrap().schema.id,
                Value::Bytes(row.bytes[..16].to_vec()),
            );
            if !self.accepts(cursor, &fields) {
                return Ok(None);
            }
        }
        let pairs = pil_codec::CborMap::new(data, 32).map_err(invalid)?;
        let mut fields = Vec::with_capacity(pairs.size_hint().1.unwrap_or(0));
        for pair in pairs {
            let (key, value) = pair.map_err(invalid)?;
            let key = match key.unsigned() {
                Some(n) => n,
                None => schema::unsigned(&schema::decode(key.as_bytes())?)?,
            };
            fields.push((u16::try_from(key).map_err(invalid)?, value));
        }
        fields.sort_unstable_by_key(|&(key, _)| key);
        if fields.windows(2).any(|pair| pair[0].0 == pair[1].0) {
            return Err(invalid("duplicate CBOR field"));
        }
        let mut catalog = self.catalog.write().unwrap();
        let lineage = catalog.lineage(row.leaf, true)?;
        if let Some(duration_key) = catalog.schema.duration {
            let duration = if let Some(value) = lineage.duration {
                value
            } else {
                let index = fields
                    .binary_search_by_key(&duration_key, |&(key, _)| key)
                    .map_err(|_| invalid("missing duration"))?;
                let value = fields[index].1;
                match value.unsigned() {
                    Some(n) => n,
                    None => schema::unsigned(&schema::decode(value.as_bytes())?)?,
                }
            };
            if duration > 7 * DAY_MILLIS * 1000 {
                return Err(invalid("invalid duration"));
            }
        }
        Ok(Some(RawRecord {
            fields,
            lineage,
            uuid: row.bytes[..16].try_into().unwrap(),
            id: catalog.schema.id,
        }))
    }

    pub fn read(&self, cursor: &mut Cursor, buffer: &mut [u8]) -> io::Result<Option<Record>> {
        let decision = self.decision(cursor)?;
        if decision == Decision::Exclude {
            return Ok(None);
        }
        cursor.validate(self, buffer)?;
        let mut fields = self.population(cursor, buffer)?;
        if decision == Decision::Population && !self.accepts(cursor, &fields) {
            return Ok(None);
        }
        let row = &cursor.row;
        let mut catalog = self.catalog.write().unwrap();
        let lineage = catalog.lineage(row.leaf, false)?;
        if let Some(duration_key) = catalog.schema.duration {
            let duration = if let Some(duration) = lineage.duration {
                duration
            } else {
                schema::unsigned(
                    fields
                        .get(&duration_key)
                        .ok_or_else(|| invalid("missing duration"))?,
                )?
            };
            if duration > 7 * DAY_MILLIS * 1000 {
                return Err(invalid("invalid duration"));
            }
        }
        for key in &lineage.keys {
            fields.remove(key);
        }
        Ok(Some(Record {
            fields,
            lineage,
            millis: timestamp(&row.bytes),
            id: catalog.schema.id,
        }))
    }
}

fn check_population(pop: &Snapshot, start: u64, end: u64, buffer: &mut [u8]) -> io::Result<()> {
    if end <= start || end - start > MAX_BLOCK || end > pop.size {
        return Err(invalid("invalid population block range"));
    }
    let mut header = [0; 8];
    pop.file.read_exact_at(&mut header, start - 8)?;
    if u64::from(u32be(&header)) != end - start {
        return Err(invalid("population length mismatch"));
    }
    let mut offset = start;
    let mut checksum = 0;
    while offset < end {
        crate::runtime::check()?;
        let size = buffer.len().min((end - offset) as usize);
        pop.file.read_exact_at(&mut buffer[..size], offset)?;
        checksum = crc32c::crc32c_append(checksum, &buffer[..size]);
        offset += size as u64;
    }
    if checksum != u32be(&header[4..]) {
        return Err(invalid("population checksum mismatch"));
    }
    Ok(())
}

pub(crate) struct Progress {
    offset: u64,
    population_end: u64,
    identity: Option<(u64, u64)>,
    compact: bool,
    digest: u32,
}
impl Default for Progress {
    fn default() -> Self {
        Self {
            offset: 8,
            population_end: 8,
            identity: None,
            compact: false,
            digest: 0,
        }
    }
}

type Selected = Option<(Source, Vec<Cursor>)>;

pub(crate) struct Reader {
    catalogs: Catalogs,
    progress: BTreeMap<PathBuf, Progress>,
    buffer: Vec<u8>,
    follow: bool,
    compact: bool,
    filter: Arc<Filter>,
    prepared: bool,
}

impl Reader {
    pub fn new(follow: bool, compact: bool, filter: Filter) -> Self {
        Self {
            prepared: false,
            catalogs: Catalogs::new(),
            progress: BTreeMap::new(),
            buffer: Vec::new(),
            follow,
            compact,
            filter: Arc::new(filter),
        }
    }

    pub fn select_many(
        &mut self,
        partitions: Vec<Partition>,
        bounds: std::ops::Range<u64>,
        backwards: bool,
        limit: Option<usize>,
    ) -> io::Result<Vec<Selected>> {
        let workers = crate::runtime::parallelism().min(partitions.len());
        if workers <= 1 {
            return partitions
                .iter()
                .map(|p| self.select(p, bounds.clone(), backwards, limit))
                .collect();
        }
        // Pin index descriptors/ends before refreshing catalogs: every referenced
        // node was synced before its index run became visible.
        let snapshots: Vec<_> = partitions
            .iter()
            .map(|p| {
                if self.follow && fs::metadata(&p.path)?.len() < 8 {
                    Ok(None)
                } else {
                    Snapshot::open(&p.path, b"TIDX").map(Some)
                }
            })
            .collect::<io::Result<_>>()?;
        let mut seen = std::collections::HashSet::new();
        for p in &partitions {
            if seen.insert(p.version.clone()) {
                load_catalog(&mut self.catalogs, &p.version, &self.filter, self.compact)?;
            }
        }
        let jobs: Mutex<std::collections::VecDeque<_>> = Mutex::new(
            partitions
                .into_iter()
                .zip(snapshots)
                .enumerate()
                .map(|(i, (p, snapshot))| {
                    let progress = self.progress.remove(&p.path).unwrap_or_default();
                    (i, p, progress, snapshot)
                })
                .collect(),
        );
        let count = jobs.lock().unwrap().len();
        let mut results: Vec<_> = (0..count).map(|_| None).collect();
        let cancel = crate::runtime::cancellation();
        std::thread::scope(|scope| -> io::Result<()> {
            let (send, recv) = std::sync::mpsc::sync_channel(workers);
            for _ in 0..workers {
                let send = send.clone();
                let jobs = &jobs;
                let bounds = bounds.clone();
                let catalogs = self.catalogs.clone();
                let filter = self.filter.clone();
                let follow = self.follow;
                let compact = self.compact;
                let cancel = cancel.clone();
                scope.spawn(move || {
                    if let Some(cancel) = cancel {
                        crate::runtime::install(cancel);
                    }
                    loop {
                        let Some((i, p, progress, snapshot)) = jobs.lock().unwrap().pop_front()
                        else {
                            break;
                        };
                        let mut reader = Reader {
                            catalogs: catalogs.clone(),
                            progress: BTreeMap::from([(p.path.clone(), progress)]),
                            buffer: Vec::new(),
                            follow,
                            compact,
                            filter: filter.clone(),
                            prepared: true,
                        };
                        let result = match snapshot {
                            Some(idx) => {
                                reader.select_snapshot(&p, bounds.clone(), backwards, limit, idx)
                            }
                            None => Ok(None),
                        };
                        let progress = reader.progress.remove(&p.path).unwrap();
                        if send.send((i, p.path, progress, result)).is_err() {
                            break;
                        }
                    }
                    crate::runtime::clear();
                });
            }
            drop(send);
            for (i, path, progress, result) in recv {
                self.progress.insert(path, progress);
                results[i] = Some(result);
            }
            Ok(())
        })?;
        results
            .into_iter()
            .map(|r| r.ok_or_else(|| invalid("partition worker stopped"))?)
            .collect()
    }

    pub fn select(
        &mut self,
        partition: &Partition,
        bounds: std::ops::Range<u64>,
        backwards: bool,
        limit: Option<usize>,
    ) -> io::Result<Selected> {
        if self.follow && fs::metadata(&partition.path)?.len() < 8 {
            return Ok(None);
        }
        let idx = Snapshot::open(&partition.path, b"TIDX")?;
        self.select_snapshot(partition, bounds, backwards, limit, idx)
    }
    fn select_snapshot(
        &mut self,
        partition: &Partition,
        bounds: std::ops::Range<u64>,
        backwards: bool,
        limit: Option<usize>,
        idx: Snapshot,
    ) -> io::Result<Selected> {
        let version = &partition.version;
        let path = &partition.path;
        let dir = path
            .parent()
            .ok_or_else(|| invalid("missing index directory"))?;
        let progress = self.progress.entry(path.clone()).or_default();
        let converted = progress.identity.is_some_and(|id| id != idx.identity);
        let consumed = if converted {
            if !matches!(idx.format, 3 | 4) || !idx.compact || progress.compact {
                return Err(invalid("unexpected index replacement"));
            }
            progress.population_end
        } else {
            0
        };
        if !converted && idx.size < progress.offset {
            return Err(invalid("index truncated while following"));
        }
        if !converted && progress.identity.is_some() && idx.size == progress.offset {
            return Ok(None);
        }
        let expected_digest = progress.digest;
        let mut digest = if converted { 0 } else { progress.digest };
        let mut verified_conversion = !converted || consumed == 8;
        let scan_start = if converted { 8 } else { progress.offset };
        let catalog = if self.prepared {
            self.catalogs.get(version).cloned()
        } else if partition.owner.is_some() || !self.filter.is_empty() && !bounds.is_empty() {
            Some(load_catalog(
                &mut self.catalogs,
                version,
                &self.filter,
                self.compact,
            )?)
        } else {
            None
        };
        if let (Some(owner), Some(catalog)) = (partition.owner, catalog.as_ref()) {
            let catalog = catalog.read().unwrap();
            if catalog.owners.get(owner as usize) != Some(&owner) {
                return Err(invalid("unknown index owner"));
            }
            if let Some(filter) = &catalog.filter
                && filter.decision(owner)? == Decision::Exclude
            {
                return Ok(None);
            }
        }
        let mut pop = None;
        let mut cursors = Vec::new();
        let mut population_end = if converted {
            8
        } else {
            progress.population_end
        };
        let next = idx
            .scan(scan_start, &mut self.buffer, |offset, data, cached| {
                if matches!(idx.format, 3 | 4) && offset == 8 {
                    if data.len() != 20
                        || u64be(data) != partition.start
                        || u64be(&data[8..]) != partition.end
                        || Some(u32be(&data[16..])) != partition.owner
                    {
                        return Err(invalid("index scope mismatch"));
                    }
                    return Ok(());
                }
                let rows = Rows::parse(data, idx.compact, idx.format == 4)?;
                if cached && !self.follow {
                    if u64be(&rows.row(0)[20..]) != population_end + 8 {
                        return Err(invalid("noncontiguous population block"));
                    }
                    population_end = rows.end(rows.len() - 1);
                }
                let mut previous: Option<[u8; 16]> = None;
                for i in 0..if cached && !self.follow {
                    0
                } else {
                    rows.len()
                } {
                    if i % 256 == 0 {
                        crate::runtime::check()?;
                    }
                    let bytes = rows.row(i);
                    let row = Row::decode(bytes)?;
                    if rows.time(i) < partition.start || rows.time(i) >= partition.end {
                        return Err(invalid("index UUID outside bucket"));
                    }
                    if let Some(owner) = partition.owner {
                        let cat = catalog.as_ref().unwrap().read().unwrap();
                        if cat.owners.get(row.leaf as usize) != Some(&owner) {
                            return Err(invalid("index leaf/owner mismatch"));
                        }
                    }
                    if let Some(previous) = previous {
                        if row.start != population_end || previous.as_slice() >= rows.uuid(i) {
                            return Err(invalid("unordered or noncontiguous index run"));
                        }
                    } else if row.start != population_end + 8 {
                        return Err(invalid("noncontiguous population block"));
                    }
                    previous = Some(bytes[..16].try_into().unwrap());
                    population_end = row.end;
                    if self.follow {
                        digest = crc32c::crc32c_append(digest, &bytes[..20]);
                        digest = crc32c::crc32c_append(digest, &bytes[28..]);
                    }
                }
                if converted && population_end <= consumed {
                    if population_end == consumed {
                        if digest != expected_digest {
                            return Err(invalid("converted index changed consumed rows"));
                        }
                        verified_conversion = true;
                    }
                    return Ok(());
                }
                if converted && !verified_conversion {
                    return Err(invalid("converted index lost committed boundary"));
                }
                if bounds.is_empty() {
                    return Ok(());
                }
                let range = rows.window(bounds.start, bounds.end);
                if range.is_empty() {
                    return Ok(());
                }
                let bound_catalog = catalog.as_ref().map(|catalog| catalog.read().unwrap());
                let filter = bound_catalog
                    .as_ref()
                    .and_then(|catalog| catalog.filter.as_ref());
                let use_postings =
                    filter.is_some() && limit.is_none_or(|limit| limit >= range.len());
                let positions = if use_postings {
                    Postings::from_leaves((0..rows.len()).map(|i| rows.leaf(i)))
                        .select(range, filter.unwrap())?
                } else {
                    Positions::Range(range)
                };
                if positions.len() == 0 {
                    return Ok(());
                }
                let run = Run {
                    cached: (idx.identity, offset, idx.size),
                    offset: offset + 8 + rows.header as u64,
                    width: rows.width,
                    count: rows.len(),
                    buffer: Mutex::new(RowBuffer {
                        first: 0,
                        rows: Vec::new(),
                    }),
                    population_start: u64be(&rows.row(0)[20..]),
                    population_end,
                    validated: AtomicBool::new(false),
                };
                if pop.is_none() {
                    pop = Some(Snapshot::open(
                        &dir.join(if matches!(idx.format, 3 | 4) {
                            "events.pop"
                        } else {
                            "log.pop"
                        }),
                        b"TPOP",
                    )?);
                }
                let position = if backwards { positions.len() - 1 } else { 0 };
                let row = positions.at(position);
                let first = row / ROW_BUFFER * ROW_BUFFER;
                *run.buffer.lock().unwrap() = RowBuffer {
                    first,
                    rows: (first..rows.len().min(first + ROW_BUFFER))
                        .map(|i| rows.row(i))
                        .collect(),
                };
                cursors.push(Cursor {
                    needs_filter: filter.is_some() && !use_postings,
                    row: Row::decode(rows.row(row))?,
                    source: 0,
                    offset: run.offset + row as u64 * run.width as u64,
                    run: Arc::new(run),
                    positions: Arc::new(positions),
                    position,
                    backwards,
                });
                Ok(())
            })
            .map_err(|error| {
                io::Error::new(error.kind(), format!("{}: {error}", path.display()))
            })?;
        if !verified_conversion {
            return Err(invalid("converted index lost consumed prefix"));
        }
        progress.compact = idx.compact;
        progress.digest = digest;
        progress.offset = next;
        progress.population_end = population_end;
        progress.identity = Some(idx.identity);
        let Some(pop) = pop else {
            return Ok(None);
        };
        let catalog = match catalog {
            Some(catalog) => catalog,
            None => load_catalog(&mut self.catalogs, version, &self.filter, self.compact)?,
        };
        Ok(Some((Source { idx, pop, catalog }, cursors)))
    }
}

fn load_catalog(
    catalogs: &mut Catalogs,
    version: &Path,
    filter: &Filter,
    compact: bool,
) -> io::Result<Arc<RwLock<Catalog>>> {
    let stream = catalogs.len();
    let catalog = match catalogs.entry(version.to_path_buf()) {
        std::collections::btree_map::Entry::Occupied(entry) => entry.into_mut(),
        std::collections::btree_map::Entry::Vacant(entry) => {
            let cached = if crate::runtime::resident() {
                CATALOG_CACHE.with(|cache| cache.borrow_mut().remove(version))
            } else {
                None
            };
            let cached = cached.filter(|c| {
                fs::metadata(schema_file(version, "tri").unwrap_or_default()).is_ok_and(|m| {
                    let c = c.read().unwrap();
                    (m.dev(), m.ino()) == c.trie.identity && m.len() >= c.offset
                })
            });
            let value = match cached {
                Some(c) => {
                    c.write().unwrap().rebind(filter, stream, compact)?;
                    c
                }
                None => Arc::new(RwLock::new(Catalog::load(
                    version, filter, stream, compact,
                )?)),
            };
            entry.insert(value)
        }
    };
    catalog.write().unwrap().refresh()?;
    Ok(Arc::clone(catalog))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn scan_resumes_after_last_complete_block() {
        let path = std::env::temp_dir().join(format!(
            "trunk-follow-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let mut file = File::create_new(&path).unwrap();
        file.write_all(b"TIDX\0\0\0\x02").unwrap();
        let block = |data: &[u8]| {
            let mut block = Vec::new();
            block.extend_from_slice(&(data.len() as u32).to_be_bytes());
            block.extend_from_slice(&crc32c::crc32c(data).to_be_bytes());
            block.extend_from_slice(data);
            block
        };
        file.write_all(&block(b"first")).unwrap();
        let second = block(b"second");
        file.write_all(&second[..10]).unwrap();
        let mut snapshot = Snapshot::open(&path, b"TIDX").unwrap();
        let mut buffer = Vec::new();
        let mut seen = Vec::new();
        let offset = snapshot
            .scan(8, &mut buffer, |_, data, _| {
                seen.push(data.to_vec());
                Ok(())
            })
            .unwrap();
        assert_eq!(seen, [b"first".to_vec()]);
        assert_eq!(offset, 21);
        file.write_all(&second[10..]).unwrap();
        snapshot.size = file.metadata().unwrap().len();
        seen.clear();
        let end = snapshot
            .scan(offset, &mut buffer, |_, data, _| {
                seen.push(data.to_vec());
                Ok(())
            })
            .unwrap();
        assert_eq!(seen, [b"second".to_vec()]);
        assert_eq!(end, snapshot.size);
        assert_eq!(
            snapshot
                .scan(end, &mut buffer, |_, _, _| panic!("replayed data"))
                .unwrap(),
            end
        );
        file.set_len(8).unwrap();
        snapshot.size = 8;
        assert!(snapshot.scan(end, &mut buffer, |_, _, _| Ok(())).is_err());
        drop(snapshot);
        drop(file);
        fs::remove_file(path).unwrap();
    }
}

pub(crate) fn schema_file(version: &Path, ext: &str) -> io::Result<PathBuf> {
    let modern = version.join(format!("events.{ext}"));
    let legacy = version.join(format!("log.{ext}"));
    if modern.try_exists()? {
        if legacy.try_exists()? {
            return Err(invalid("ambiguous legacy/events files"));
        }
        Ok(modern)
    } else {
        Ok(legacy)
    }
}

thread_local! {
    static CATALOG_CACHE: RefCell<Catalogs> = const { RefCell::new(Catalogs::new()) };
}
impl Drop for Reader {
    fn drop(&mut self) {
        if self.prepared || !crate::runtime::resident() {
            return;
        }
        CATALOG_CACHE.with(|cache| {
            let mut cache = cache.borrow_mut();
            for catalog in self.catalogs.values() {
                catalog.write().unwrap().filter = None;
            }
            cache.extend(std::mem::take(&mut self.catalogs));
            let bytes = |c: &Arc<RwLock<Catalog>>| {
                let c = c.read().unwrap();
                c.nodes.capacity() * std::mem::size_of::<Node>()
                    + c.owners.capacity() * 4
                    + c.lineage_bytes
                    + c.definition_bytes.len() * 4
            };
            while cache.len() > 8 || cache.values().map(bytes).sum::<usize>() > 32 << 20 {
                cache.pop_first();
            }
        });
    }
}
