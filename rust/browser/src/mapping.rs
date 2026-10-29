use crate::{Result, bad};
use ciborium::Value as Cbor;
use pil_codec::{
    Array, ArrayView, Definition, Field, FrameEncoder, FrameIndex, ObjectMap, ObjectRef, Scalar,
    Type, Value,
};
use std::collections::{BTreeMap, HashSet};

pub struct Mapping {
    pub definition: Definition,
    document: pil_codec::DefinitionDocument,
    native: bool,
    keys: Vec<u16>,
    codes: Vec<Vec<u16>>,
    id: usize,
    path: Vec<u16>,
}
fn map(v: &Cbor) -> Result<BTreeMap<u16, &Cbor>> {
    let pairs = v.as_map().ok_or("expected schema map")?;
    let mut out = BTreeMap::new();
    for (k, v) in pairs {
        let k = k
            .as_integer()
            .and_then(|v| u16::try_from(v).ok())
            .ok_or("invalid schema key")?;
        if out.insert(k, v).is_some() {
            return bad("duplicate schema key");
        }
    }
    Ok(out)
}
fn text(v: &&Cbor) -> Result<String> {
    Ok(v.as_text().ok_or("expected text")?.into())
}
fn labels(v: &Cbor) -> Result<BTreeMap<u16, String>> {
    map(v)?
        .into_iter()
        .map(|(k, v)| Ok((k, text(&v)?)))
        .collect()
}
impl Mapping {
    pub fn new(schema: &[u8], pil: &[u8]) -> Result<Self> {
        let value = pil_codec::object::decode_value(schema).map_err(|e| e.to_string())?;
        if value
            .as_map()
            .is_some_and(|m| m.iter().any(|(k, _)| k.as_text() == Some("resource")))
        {
            if schema != pil {
                return bad("PIL/resource maps must be identical");
            }
            let document =
                pil_codec::DefinitionDocument::from_pil(pil).map_err(|e| e.to_string())?;
            let definition = document.definition().clone();
            let get = |v: &Cbor, name: &str| {
                v.as_map().and_then(|m| {
                    m.iter()
                        .find(|(k, _)| k.as_text() == Some(name))
                        .map(|(_, v)| v.clone())
                })
            };
            let resource = get(&value, "resource").ok_or("missing resource")?;
            let roles = get(&resource, "roles").ok_or("missing roles")?;
            let event = get(&roles, "event_id").ok_or("missing event role")?;
            let id = definition
                .fields()
                .iter()
                .position(|f| {
                    Some(f.name.as_str()) == event.as_text() && f.kind == Type::Scalar(Scalar::Uuid)
                })
                .ok_or("invalid event role")?;
            return Ok(Self {
                definition,
                document,
                native: true,
                keys: Vec::new(),
                codes: Vec::new(),
                id,
                path: Vec::new(),
            });
        }
        let root = map(&value)?;
        if root.get(&4).and_then(|v| v.as_text()) != Some("UTC") {
            let modern = matches!(
                root.get(&7)
                    .map(|v| map(v))
                    .transpose()?
                    .and_then(|m| m.get(&0).and_then(|v| v.as_integer()).map(i128::from)),
                Some(3 | 4)
            );
            if !modern
                || root
                    .get(&4)
                    .and_then(|v| v.as_text())
                    .is_none_or(str::is_empty)
            {
                return bad("schema requires a timezone");
            }
        }
        let version = root
            .get(&0)
            .and_then(|v| v.as_integer())
            .and_then(|v| u32::try_from(v).ok())
            .filter(|v| *v > 0)
            .ok_or("invalid schema version")?;
        let mut domains = BTreeMap::new();
        if let Some(v) = root.get(&3) {
            for (k, v) in v.as_map().ok_or("invalid domains")? {
                if domains
                    .insert(k.as_text().ok_or("invalid domain")?, labels(v)?)
                    .is_some()
                {
                    return bad("duplicate domain");
                }
            }
        }
        let mut keys = Vec::new();
        let mut codes = Vec::new();
        let mut fields = Vec::new();
        let mut id = None;
        let mut duration = None;
        for (key, v) in map(root.get(&1).ok_or("missing fields")?)? {
            let f = map(v)?;
            let name = text(f.get(&0).ok_or("missing field name")?)?;
            let uuid = f.get(&6).and_then(|v| v.as_text()) == Some("uuidv7");
            if name == "id" {
                if !uuid || id.replace(keys.len()).is_some() {
                    return bad("invalid id field");
                }
            }
            let dur = name == "duration";
            if dur {
                if f.get(&5).and_then(|v| v.as_text()) != Some("us")
                    || duration.replace(key).is_some()
                {
                    return bad("invalid duration field");
                }
            }
            let mut enums = f
                .get(&2)
                .map(|v| labels(v))
                .transpose()?
                .unwrap_or_default();
            if let Some(domain) = f
                .get(&1)
                .and_then(|v| v.as_text())
                .and_then(|n| domains.get(n))
            {
                for (k, v) in domain {
                    enums.entry(*k).or_insert_with(|| v.clone());
                }
            }
            let mapped = !uuid
                && !dur
                && !enums.is_empty()
                && enums.values().collect::<HashSet<_>>().len() == enums.len();
            fields.push(if mapped {
                Field::mapped(name, enums.values().cloned())
            } else {
                Field::new(
                    name,
                    if uuid {
                        Scalar::Uuid
                    } else if dur {
                        Scalar::U64
                    } else {
                        Scalar::Object
                    },
                )
            });
            codes.push(if mapped {
                enums.keys().copied().collect()
            } else {
                Vec::new()
            });
            keys.push(key);
        }
        let id = id.ok_or("missing id")?;
        let duration = duration.ok_or("missing duration")?;
        let mut path = Vec::new();
        if let Some(v) = root.get(&5) {
            for k in v.as_array().ok_or("invalid path")? {
                let k = k
                    .as_integer()
                    .and_then(|n| u16::try_from(n).ok())
                    .ok_or("invalid path key")?;
                if !keys.contains(&k) || k == keys[id] || k == duration || path.contains(&k) {
                    return bad("invalid path");
                }
                path.push(k);
            }
            if path.len() > 32 {
                return bad("path too long");
            }
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
        let document = pil_codec::DefinitionDocument::from_pil(pil).map_err(|e| e.to_string())?;
        let definition = document.definition().clone();
        if definition.version() != u64::from(version) || definition.fields() != fields {
            return bad("PIL definition does not match tap schema");
        }
        Ok(Self {
            definition,
            document,
            native: false,
            keys,
            codes,
            id,
            path,
        })
    }
    pub fn known(&self, key: u16) -> bool {
        self.path.contains(&key)
    }
    pub fn split(
        &self,
        bytes: &[u8],
        index: &mut FrameIndex,
    ) -> Result<([u8; 16], Vec<(u16, u64)>, Vec<u8>)> {
        if self
            .document
            .scan(index, bytes, 64 * 1024 * 1024)
            .map_err(|e| e.to_string())?
            != bytes.len()
        {
            return bad("trailing PIL bytes");
        }
        let spans = index.spans();
        let [start, end] = spans[self.id];
        let uuid: [u8; 16] = bytes
            .get(start as usize..end as usize)
            .ok_or("missing UUID")?
            .try_into()
            .map_err(|_| "invalid UUID")?;
        if uuid[6] >> 4 != 7 || uuid[8] >> 6 != 2 {
            return bad("id must be UUIDv7");
        }
        if self.native {
            return Ok((uuid, Vec::new(), bytes.to_vec()));
        }
        let frame = pil_codec::decode_exact(&self.definition, bytes).map_err(|e| e.to_string())?;
        let mut values = BTreeMap::new();
        for (i, key) in self.keys.iter().enumerate() {
            if i == self.id {
                continue;
            }
            let value = match frame.values()[i] {
                Value::Absent => continue,
                Value::Null => Cbor::Null,
                Value::Object(b) => {
                    pil_codec::object::decode_value(b).map_err(|e| e.to_string())?
                }
                Value::Uuid(b) => Cbor::Bytes(b.to_vec()),
                Value::U64(n) => Cbor::Integer(n.into()),
                Value::Mapped(n) => Cbor::Integer(self.codes[i][n as usize].into()),
                Value::String(s) => Cbor::Text(s.into()),
                _ => return bad("unsupported PIL storage field"),
            };
            values.insert(*key, value);
        }
        match frame.values()[self.keys.len() + 1] {
            Value::Absent => (),
            Value::Object(b) => {
                for (key, value) in
                    map(&pil_codec::object::decode_value(b).map_err(|e| e.to_string())?)?
                {
                    if key == self.keys[self.id] || values.insert(key, value.clone()).is_some() {
                        return bad("duplicate fallback key");
                    }
                }
            }
            _ => return bad("invalid fallback"),
        }
        let Value::Array(Array::Encoded(lineage)) = frame.values()[self.keys.len()] else {
            return bad("missing lineage");
        };
        let mut chain = Vec::new();
        let mut next = 0;
        for value in lineage.iter() {
            let Value::U16(key) = value.map_err(|e| e.to_string())? else {
                return bad("invalid lineage key");
            };
            let position = self.path[next..]
                .iter()
                .position(|k| *k == key)
                .ok_or("lineage differs from schema path")?;
            next += position + 1;
            let n = values
                .remove(&key)
                .and_then(|v| v.as_integer())
                .and_then(|n| u64::try_from(n).ok())
                .ok_or("lineage must be unsigned")?;
            chain.push((key, n));
        }
        values.remove(&self.keys[self.id]);
        let payload = pil_codec::object::encode_value(&Cbor::Map(
            values
                .into_iter()
                .map(|(k, v)| (Cbor::Integer(k.into()), v))
                .collect(),
        ))
        .map_err(|e| e.to_string())?;
        if payload.len() > 65535 {
            return bad("payload exceeds native store limit");
        }
        Ok((uuid, chain, payload))
    }
    fn value<'a>(&self, i: usize, v: ObjectRef<'a>) -> Option<Value<'a>> {
        if v.is_null() {
            return Some(Value::Null);
        }
        Some(match self.definition.fields()[i].kind {
            Type::Scalar(Scalar::Object) => Value::Object(v.as_bytes()),
            Type::Scalar(Scalar::Uuid) => Value::Uuid(v.bytes()?.try_into().ok()?),
            Type::Scalar(Scalar::U64) => Value::U64(v.unsigned()?),
            Type::Scalar(Scalar::String) => {
                if let Some(s) = v.text() {
                    Value::String(s)
                } else {
                    Value::Mapped(
                        self.codes[i]
                            .binary_search(&u16::try_from(v.unsigned()?).ok()?)
                            .ok()? as u64,
                    )
                }
            }
            _ => return None,
        })
    }
    pub fn join(
        &self,
        uuid: [u8; 16],
        chain: &[(u16, u64)],
        payload: &[u8],
        encoder: &mut FrameEncoder,
        index: &mut FrameIndex,
    ) -> Result<Vec<u8>> {
        if self.native {
            if !chain.is_empty() {
                return bad("unexpected native lineage");
            }
            // Validate offsets once per requested frame; never reconstruct values.
            if self
                .document
                .scan(index, payload, 64 * 1024 * 1024)
                .map_err(|e| e.to_string())?
                != payload.len()
            {
                return bad("invalid native frame");
            }
            let [start, end] = index.spans()[self.id];
            if payload.get(start as usize..end as usize) != Some(uuid.as_slice()) {
                return bad("event identity mismatch");
            }
            return Ok(payload.to_vec());
        }
        let mut values = BTreeMap::new();
        for item in ObjectMap::new(payload, 64).map_err(|e| e.to_string())? {
            let (k, v) = item.map_err(|e| e.to_string())?;
            let key = u16::try_from(k.unsigned().ok_or("invalid payload key")?)
                .map_err(|_| "invalid payload key")?;
            if key == self.keys[self.id] || values.insert(key, v).is_some() {
                return bad("duplicate payload field");
            }
        }
        let raw: Vec<_> = chain
            .iter()
            .map(|(_, v)| {
                pil_codec::object::encode_value(&Cbor::Integer((*v).into())).expect("trie integer")
            })
            .collect();
        for ((key, _), v) in chain.iter().zip(&raw) {
            if values.insert(*key, ObjectRef::parse(v).unwrap()).is_some() {
                return bad("duplicate lineage field");
            }
        }
        let mut extra = Vec::new();
        let fallback: Vec<_> = values
            .iter()
            .filter(|(key, v)| {
                self.keys
                    .binary_search(key)
                    .ok()
                    .and_then(|i| self.value(i, **v))
                    .is_none()
            })
            .collect();
        if !fallback.is_empty() {
            let pairs = fallback
                .into_iter()
                .map(|(k, v)| {
                    Ok((
                        Cbor::Integer((*k).into()),
                        pil_codec::object::decode_value(v.as_bytes()).map_err(|e| e.to_string())?,
                    ))
                })
                .collect::<Result<Vec<_>>>()?;
            extra =
                pil_codec::object::encode_value(&Cbor::Map(pairs)).map_err(|e| e.to_string())?;
        }
        let mut lineage = Vec::new();
        pil_codec::encode_extended(chain.len() as u64, &mut lineage);
        for (k, _) in chain {
            lineage.extend_from_slice(&k.to_le_bytes());
        }
        let array = ArrayView::parse(Scalar::U16, &lineage).map_err(|e| e.to_string())?;
        encoder
            .encode_with(&self.definition, |i| {
                Ok(if i == self.id {
                    Value::Uuid(uuid)
                } else if i == self.keys.len() {
                    Value::Array(Array::Encoded(array))
                } else if i == self.keys.len() + 1 {
                    if extra.is_empty() {
                        Value::Absent
                    } else {
                        Value::Object(&extra)
                    }
                } else {
                    values
                        .get(&self.keys[i])
                        .and_then(|v| self.value(i, *v))
                        .unwrap_or(Value::Absent)
                })
            })
            .map(|b| b.to_vec())
            .map_err(|e| e.to_string())
    }
}
