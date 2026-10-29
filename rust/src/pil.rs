use crate::{
    schema::{self, Schema},
    search,
    store::{self, Catalog, RawRecord, invalid},
};
use ciborium::Value as Cbor;
use pil_codec::{
    Array, Definition, Field, FrameEncoder, ObjectRef, Scalar, Type, Value, decode_exact, encode,
    read_frame_bytes,
};
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, HashMap, HashSet};
use std::io::{self, BufReader, BufWriter, Read, Write};
use std::path::Path;

pub const MAGIC: &[u8] = b"TPIL\x01";
pub const MAGIC_V2: &[u8] = b"TPIL\x02";
pub const END: u8 = 255;
const DEFINE: u8 = 1;
const BIND: u8 = 2;
const EVENT: u8 = 3;
const MAX_FRAME: usize = 64 * 1024 * 1024;

fn binding(v2: bool) -> Definition {
    if !v2 {
        return control(BIND);
    }
    Definition::new(
        "trunk-pil-control",
        2,
        vec![
            Field::new("stream", Scalar::U32),
            Field::new("digest", Scalar::Bytes),
            Field::new("tap", Scalar::String),
        ],
    )
    .unwrap()
}

fn control(kind: u8) -> Definition {
    let fields = match kind {
        DEFINE => vec![
            Field::new("digest", Scalar::Bytes),
            Field::new("store", Scalar::Bytes),
            Field::new("pil", Scalar::Bytes),
        ],
        BIND => vec![
            Field::new("stream", Scalar::U32),
            Field::new("digest", Scalar::Bytes),
        ],
        _ => unreachable!(),
    };
    Definition::new("trunk-pil-control", u64::from(kind), fields).unwrap()
}

fn digest(bytes: &[u8]) -> Vec<u8> {
    Sha256::digest(bytes).to_vec()
}
fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

struct Mapping {
    definition: Definition,
    schema: Schema,
    keys: Vec<u16>,
    codes: Vec<Vec<u16>>,
}

impl Mapping {
    fn new(bytes: &[u8]) -> io::Result<Self> {
        let root = schema::record(schema::decode(bytes)?)?;
        let version = u32::try_from(schema::unsigned(
            root.get(&0)
                .ok_or_else(|| invalid("missing schema version"))?,
        )?)
        .map_err(invalid)?;
        let schema = Schema::parse(bytes, version)?;
        let mut fields = Vec::new();
        let mut keys = Vec::new();
        let mut codes = Vec::new();
        for (&key, field) in &schema.fields {
            keys.push(key);
            let unique: HashSet<_> = field.enums.values().collect();
            let mapped = !field.enums.is_empty()
                && unique.len() == field.enums.len()
                && field.encoding != "uuidv7"
                && Some(key) != schema.duration;
            let kind = if field.encoding == "uuidv7" {
                Scalar::Uuid
            } else if Some(key) == schema.duration {
                Scalar::U64
            } else if mapped {
                Scalar::String
            } else {
                Scalar::Object
            };
            fields.push(if mapped {
                Field::mapped(&field.name, field.enums.values().cloned())
            } else {
                Field::new(&field.name, kind)
            });
            codes.push(if mapped {
                field.enums.keys().copied().collect()
            } else {
                Vec::new()
            });
        }
        for (base, kind) in [
            ("trunk_lineage", Type::Array(Scalar::U16)),
            ("trunk_fallback", Type::Scalar(Scalar::Object)),
        ] {
            let mut name = base.to_owned();
            while fields.iter().any(|f| f.name == name) {
                name.push('_');
            }
            fields.push(Field::new(name, kind));
        }
        let definition = Definition::new(
            format!("trunk:{}", hex(&digest(bytes))),
            u64::from(version),
            fields,
        )
        .map_err(invalid)?;
        Ok(Self {
            definition,
            schema,
            keys,
            codes,
        })
    }

    fn value<'a>(&self, index: usize, value: ObjectRef<'a>) -> Option<Value<'a>> {
        if value.is_null() {
            return Some(Value::Null);
        }
        Some(match self.definition.fields()[index].kind {
            Type::Scalar(Scalar::Object) => Value::Object(value.as_bytes()),
            Type::Scalar(Scalar::Uuid) => Value::Uuid(value.bytes()?.try_into().ok()?),
            Type::Scalar(Scalar::U64) => Value::U64(value.unsigned()?),
            Type::Scalar(Scalar::String) => {
                if let Some(text) = value.text() {
                    Value::String(text)
                } else {
                    let code = u16::try_from(value.unsigned()?).ok()?;
                    Value::Mapped(self.codes[index].binary_search(&code).ok()? as u64)
                }
            }
            _ => return None,
        })
    }

    fn frame<'b>(
        &self,
        record: &RawRecord<'_>,
        encoder: &'b mut FrameEncoder,
        fallback: &mut Vec<u8>,
        lineage: &mut Vec<u8>,
    ) -> io::Result<&'b [u8]> {
        // Usually empty. Preserve unknown keys/codes and unusual representations
        // in the existing fallback field without coercing or dropping values.
        let mut extra = Vec::new();
        for &(key, value) in &record.fields {
            if key == record.id || record.lineage.keys.contains(&key) {
                continue;
            }
            if self
                .keys
                .binary_search(&key)
                .ok()
                .and_then(|i| self.value(i, value))
                .is_none()
            {
                extra.push((key, value));
            }
        }
        for &key in &record.lineage.keys {
            let value = record.field(key).unwrap();
            if self
                .keys
                .binary_search(&key)
                .ok()
                .and_then(|i| self.value(i, value))
                .is_none()
            {
                extra.push((key, value));
            }
        }
        fallback.clear();
        if !extra.is_empty() {
            let pairs = extra
                .into_iter()
                .map(|(key, value)| {
                    Ok((Cbor::Integer(key.into()), schema::decode(value.as_bytes())?))
                })
                .collect::<io::Result<Vec<_>>>()?;
            *fallback = pil_codec::object::encode_value(&Cbor::Map(pairs)).map_err(invalid)?;
        }
        let extra = if fallback.is_empty() {
            Value::Absent
        } else {
            Value::Object(fallback)
        };
        lineage.clear();
        pil_codec::encode_extended(record.lineage.keys.len() as u64, lineage);
        for key in &record.lineage.keys {
            lineage.extend_from_slice(&key.to_le_bytes());
        }
        let order = pil_codec::ArrayView::parse(Scalar::U16, lineage).map_err(invalid)?;
        encoder
            .encode_with(&self.definition, |index| {
                Ok(if index == self.keys.len() {
                    Value::Array(Array::Encoded(order))
                } else if index == self.keys.len() + 1 {
                    extra
                } else {
                    let key = self.keys[index];
                    if key == record.id {
                        Value::Uuid(record.uuid)
                    } else {
                        record
                            .field(key)
                            .and_then(|value| self.value(index, value))
                            .unwrap_or(Value::Absent)
                    }
                })
            })
            .map_err(invalid)
    }

    fn render(
        &self,
        bytes: &[u8],
        headers: &[&str],
        pretty: bool,
        output: &mut impl Write,
    ) -> io::Result<()> {
        let frame = decode_exact(&self.definition, bytes).map_err(invalid)?;
        let mut fields = BTreeMap::new();
        for (index, key) in self.keys.iter().enumerate() {
            let value = match frame.values()[index] {
                Value::Absent => continue,
                Value::Null => Cbor::Null,
                Value::Mapped(id) => Cbor::Integer(self.codes[index][id as usize].into()),
                Value::String(text) => Cbor::Text(text.to_owned()),
                Value::Uuid(bytes) => Cbor::Bytes(bytes.to_vec()),
                Value::U64(number) => Cbor::Integer(number.into()),
                Value::Object(bytes) => schema::decode(bytes)?,
                _ => return Err(invalid("unexpected PIL event value")),
            };
            fields.insert(*key, value);
        }
        match frame.values()[self.keys.len() + 1] {
            Value::Absent => {}
            Value::Object(bytes) => {
                for (key, value) in schema::record(schema::decode(bytes)?)? {
                    if fields.insert(key, value).is_some() {
                        return Err(invalid("duplicate fallback field"));
                    }
                }
            }
            _ => return Err(invalid("invalid fallback field")),
        }
        let id = fields
            .get(&self.schema.id)
            .and_then(Cbor::as_bytes)
            .filter(|bytes| bytes.len() == 16 && bytes[6] >> 4 == 7 && bytes[8] >> 6 == 2)
            .ok_or_else(|| invalid("missing UUIDv7"))?;
        let millis = u64::from_be_bytes(id[..8].try_into().unwrap()) >> 16;
        let Value::Array(Array::Encoded(order)) = frame.values()[self.keys.len()] else {
            return Err(invalid("missing lineage order"));
        };
        let mut lineage = Vec::new();
        for key in order.iter() {
            let Value::U16(key) = key.map_err(invalid)? else {
                return Err(invalid("invalid lineage key"));
            };
            let value = fields
                .remove(&key)
                .ok_or_else(|| invalid("duplicate or missing lineage field"))?;
            lineage.push(self.schema.expand_field(key, value)?);
        }
        let mut expanded = self.schema.expand(fields)?;
        expanded.insert(
            "time".into(),
            serde_json::Value::String(search::render_time(millis * 1000)?),
        );
        let mut bytes = Vec::new();
        search::encode(&mut bytes, &expanded, &lineage, headers, pretty)?;
        output.write_all(&bytes)
    }
}

fn write_definition(data: &[u8], mapping: &Mapping, output: &mut impl Write) -> io::Result<()> {
    let definition = mapping.definition.to_pil().map_err(invalid)?;
    let hash = digest(data);
    output.write_all(&[DEFINE])?;
    output.write_all(
        &encode(
            &control(DEFINE),
            &[
                Value::Bytes(&hash),
                Value::Bytes(data),
                Value::Bytes(&definition),
            ],
        )
        .map_err(invalid)?,
    )
}

pub struct Encoder {
    v2: bool,
    known: HashSet<String>,
    mappings: HashMap<usize, Mapping>,
    frame: FrameEncoder,
    fallback: Vec<u8>,
    lineage: Vec<u8>,
}

impl Encoder {
    pub fn new(known: &[String], v2: bool) -> Self {
        Self {
            v2,
            known: known.iter().cloned().collect(),
            mappings: HashMap::new(),
            frame: FrameEncoder::default(),
            fallback: Vec::new(),
            lineage: Vec::new(),
        }
    }
    pub(crate) fn write(
        &mut self,
        catalog: &Catalog,
        record: &RawRecord<'_>,
        output: &mut impl Write,
    ) -> io::Result<()> {
        if let std::collections::hash_map::Entry::Vacant(entry) =
            self.mappings.entry(catalog.stream)
        {
            let hash = digest(&catalog.definition_bytes);
            let mapping = Mapping::new(&catalog.definition_bytes)?;
            if self.known.insert(hex(&hash)) {
                write_definition(&catalog.definition_bytes, &mapping, output)?;
            }
            output.write_all(&[BIND])?;
            let id = u32::try_from(catalog.stream).map_err(invalid)?;
            output.write_all(
                &encode(
                    &binding(self.v2),
                    &if self.v2 {
                        vec![
                            Value::U32(id),
                            Value::Bytes(&hash),
                            Value::String(&catalog.tap),
                        ]
                    } else {
                        vec![Value::U32(id), Value::Bytes(&hash)]
                    },
                )
                .map_err(invalid)?,
            )?;
            entry.insert(mapping);
        }
        output.write_all(&[EVENT])?;
        output.write_all(
            &u32::try_from(catalog.stream)
                .map_err(invalid)?
                .to_le_bytes(),
        )?;
        output.write_all(self.mappings[&catalog.stream].frame(
            record,
            &mut self.frame,
            &mut self.fallback,
            &mut self.lineage,
        )?)
    }
}

pub fn schemas(
    root: &Path,
    tap: &str,
    known: &[String],
    v2: bool,
    output: &mut impl Write,
) -> io::Result<()> {
    let paths = match store::versions(root, tap) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => Vec::new(),
        result => result?,
    };
    let mut sent: HashSet<_> = known.iter().cloned().collect();
    output.write_all(if v2 { MAGIC_V2 } else { MAGIC })?;
    for (id, path) in paths.into_iter().enumerate() {
        let bytes = std::fs::read(store::schema_file(&path, "rx")?)?;
        if sent.insert(hex(&digest(&bytes))) {
            write_definition(&bytes, &Mapping::new(&bytes)?, output)?;
        }
        if v2 {
            let (tap, _) = store::validate_schema_path(&path, &bytes)?;
            output.write_all(&[BIND])?;
            output.write_all(
                &encode(
                    &binding(true),
                    &[
                        Value::U32(u32::try_from(id).map_err(invalid)?),
                        Value::Bytes(&digest(&bytes)),
                        Value::String(&tap),
                    ],
                )
                .map_err(invalid)?,
            )?;
        }
    }
    output.write_all(&[END])
}

fn required_frame(def: &Definition, input: &mut impl Read) -> io::Result<Vec<u8>> {
    read_frame_bytes(def, input, MAX_FRAME)?
        .ok_or_else(|| io::Error::new(io::ErrorKind::UnexpectedEof, "missing PIL frame"))
}

pub fn decode(
    input: &mut BufReader<impl Read>,
    output: &mut impl Write,
    headers: &[&str],
    pretty: bool,
) -> io::Result<()> {
    let mut definitions = HashMap::<Vec<u8>, Mapping>::new();
    let mut version = None;
    let mut segments = 0;
    let mut has_events = false;
    loop {
        let mut magic = [0; 5];
        output.flush()?;
        if input.read(&mut magic[..1])? == 0 {
            return if version == Some(true) {
                Err(invalid("missing PIL success marker"))
            } else {
                Ok(())
            };
        }
        if magic[0] == 254 && version == Some(true) {
            if input.read(&mut magic[..1])? != 0 {
                return Err(invalid("data after PIL completion"));
            }
            return Ok(());
        }
        input.read_exact(&mut magic[1..])?;
        let v2 = magic == MAGIC_V2;
        if magic != MAGIC && !v2
            || version.is_some_and(|prior| prior != v2)
            || v2 && (has_events || segments >= 2)
        {
            return Err(invalid("unsupported trunk PIL stream"));
        }
        version = Some(v2);
        segments += 1;
        let mut streams = HashMap::<u32, Vec<u8>>::new();
        loop {
            if input.buffer().is_empty() {
                output.flush()?;
            }
            let mut kind = [0];
            input.read_exact(&mut kind)?;
            match kind[0] {
                END => break,
                DEFINE => {
                    let def = control(DEFINE);
                    let bytes = required_frame(&def, input)?;
                    let frame = decode_exact(&def, &bytes).map_err(invalid)?;
                    let [Value::Bytes(hash), Value::Bytes(data), Value::Bytes(pil)] =
                        frame.values()
                    else {
                        return Err(invalid("invalid PIL definition control"));
                    };
                    if digest(data) != *hash {
                        return Err(invalid("schema digest mismatch"));
                    }
                    let mapping = Mapping::new(data)?;
                    if mapping.definition != Definition::from_pil(pil).map_err(invalid)? {
                        return Err(invalid("incompatible event definition"));
                    }
                    definitions.insert(hash.to_vec(), mapping);
                }
                BIND => {
                    let def = binding(v2);
                    let bytes = required_frame(&def, input)?;
                    let frame = decode_exact(&def, &bytes).map_err(invalid)?;
                    let (id, hash) = match frame.values() {
                        [Value::U32(id), Value::Bytes(hash)] if !v2 => (id, hash),
                        [Value::U32(id), Value::Bytes(hash), Value::String(tap)] if v2 => {
                            if !store::valid_tap(tap) {
                                return Err(invalid("invalid tap binding"));
                            }
                            (id, hash)
                        }
                        _ => return Err(invalid("invalid PIL binding")),
                    };
                    if !definitions.contains_key(*hash)
                        || streams.insert(*id, hash.to_vec()).is_some()
                    {
                        return Err(invalid("unknown definition or duplicate stream binding"));
                    }
                }
                EVENT => {
                    has_events = true;
                    let mut id = [0; 4];
                    input.read_exact(&mut id)?;
                    let hash = streams
                        .get(&u32::from_le_bytes(id))
                        .ok_or_else(|| invalid("unbound event stream"))?;
                    let mapping = &definitions[hash];
                    let bytes = required_frame(&mapping.definition, input)?;
                    mapping.render(&bytes, headers, pretty, output)?;
                }
                _ => return Err(invalid("unknown trunk PIL record")),
            }
        }
    }
}

pub fn command(args: Vec<String>) -> io::Result<()> {
    let mut root = "./logs";
    let mut tap = "";
    let mut pretty = false;
    let mut v2 = false;
    let mut headers = Vec::new();
    let mut known = Vec::new();
    let mut index = 1;
    while index < args.len() {
        if args[index] == "--pil-v2" {
            v2 = true;
            index += 1;
            continue;
        }
        if args[index] == "--pretty" {
            pretty = true;
            index += 1;
            continue;
        }
        let value = args
            .get(index + 1)
            .ok_or_else(|| invalid("missing argument"))?;
        match args[index].as_str() {
            "--dir" => root = value,
            "--tap" => tap = value,
            "--known-pil" => {
                known = value
                    .split(',')
                    .filter(|s| !s.is_empty())
                    .map(str::to_owned)
                    .collect()
            }
            "--entry-headers" => {
                for name in value.split(',').map(str::trim).filter(|s| !s.is_empty()) {
                    if !headers.contains(&name) {
                        headers.push(name);
                    }
                }
            }
            _ => return Err(invalid("unknown PIL command argument")),
        }
        index += 2;
    }
    let mut output = BufWriter::new(io::stdout().lock());
    let result = if args[0] == "--pil-schemas" {
        schemas(Path::new(root), tap, &known, v2, &mut output)
    } else {
        decode(
            &mut BufReader::new(io::stdin().lock()),
            &mut output,
            &headers,
            pretty,
        )
    };
    result.and(output.flush())
}
