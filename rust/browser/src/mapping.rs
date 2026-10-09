use crate::{Result, bad};
use ciborium::Value as Cbor;
use pil_codec::{
    ABSENT, Array, ArrayView, CborMap, CborRef, Definition, Field, FrameEncoder, FrameIndex, NULL,
    Scalar, Type, Value,
};
use std::collections::{BTreeMap, HashSet};

pub struct Mapping {
    pub definition: Definition,
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
        let mut source = schema;
        let value: Cbor = ciborium::de::from_reader_with_recursion_limit(&mut source, 32)
            .map_err(|e| e.to_string())?;
        if !source.is_empty() {
            return bad("trailing schema bytes");
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
                        Scalar::Cbor
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
            ("trunk_fallback", Type::Scalar(Scalar::Cbor)),
        ] {
            let mut name = base.to_owned();
            while fields.iter().any(|f| f.name == name) {
                name.push('_');
            }
            fields.push(Field::new(name, kind));
        }
        let definition = Definition::from_cbor(pil).map_err(|e| e.to_string())?;
        if definition.version() != u64::from(version) || definition.fields() != fields {
            return bad("PIL definition does not match tap schema");
        }
        Ok(Self {
            definition,
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
        if index
            .scan(&self.definition, bytes, 64 * 1024 * 1024)
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
        // Only offsets and small direct values are materialized. CBOR payloads
        // remain borrowed slices and are copied directly into the population map.
        let mut scalars = Vec::with_capacity(self.keys.len());
        for (i, f) in self.definition.fields()[..self.keys.len()]
            .iter()
            .enumerate()
        {
            if i == self.id {
                scalars.push(None);
                continue;
            }
            let [s, e] = spans[i];
            let mut value = Vec::new();
            if s == ABSENT {
                scalars.push(None);
                continue;
            }
            if s == NULL {
                value.push(0xf6);
            } else {
                let b = &bytes[s as usize..e as usize];
                match f.kind {
                    Type::Scalar(Scalar::Cbor) => {
                        scalars.push(Some((b, Vec::new())));
                        continue;
                    }
                    Type::Scalar(Scalar::Uuid) => {
                        head(2, b.len() as u64, &mut value);
                        value.extend_from_slice(b);
                    }
                    Type::Scalar(Scalar::U64) => {
                        head(0, u64::from_le_bytes(b.try_into().unwrap()), &mut value)
                    }
                    Type::Scalar(Scalar::String) => {
                        let state = (bytes[i / 4] >> (6 - 2 * (i % 4))) & 3;
                        if state == 2 {
                            let mut n = [0; 8];
                            n[..b.len()].copy_from_slice(b);
                            head(
                                0,
                                u64::from(self.codes[i][u64::from_le_bytes(n) as usize]),
                                &mut value,
                            );
                        } else {
                            head(3, b.len() as u64, &mut value);
                            value.extend_from_slice(b);
                        }
                    }
                    _ => return bad("unsupported PIL storage field"),
                }
            }
            scalars.push(Some((&[][..], value)));
        }
        let mut values = BTreeMap::new();
        for (i, v) in scalars.iter().enumerate() {
            if let Some((raw, owned)) = v {
                values.insert(
                    self.keys[i],
                    if owned.is_empty() {
                        *raw
                    } else {
                        owned.as_slice()
                    },
                );
            }
        }
        let [s, e] = spans[self.keys.len() + 1];
        if s != ABSENT {
            if s == NULL {
                return bad("null fallback");
            }
            for entry in
                CborMap::new(&bytes[s as usize..e as usize], 64).map_err(|e| e.to_string())?
            {
                let (key, value) = entry.map_err(|e| e.to_string())?;
                let key = u16::try_from(key.unsigned().ok_or("invalid fallback key")?)
                    .map_err(|_| "invalid fallback key")?;
                if key == self.keys[self.id] || values.insert(key, value.as_bytes()).is_some() {
                    return bad("duplicate fallback key");
                }
            }
        }
        let [s, e] = spans[self.keys.len()];
        if s >= NULL {
            return bad("missing lineage");
        }
        let lineage = ArrayView::parse(Scalar::U16, &bytes[s as usize..e as usize])
            .map_err(|e| e.to_string())?;
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
            let raw = values.remove(&key).ok_or("missing lineage value")?;
            let value = CborRef::parse(raw)
                .map_err(|e| e.to_string())?
                .unsigned()
                .ok_or("lineage must be unsigned")?;
            chain.push((key, value));
        }
        values.remove(&self.keys[self.id]);
        let mut pop = Vec::new();
        head(5, values.len() as u64, &mut pop);
        for (k, v) in values {
            head(0, u64::from(k), &mut pop);
            pop.extend_from_slice(v);
        }
        if pop.len() > 65535 {
            return bad("population exceeds native store limit");
        }
        Ok((uuid, chain, pop))
    }
    fn value<'a>(&self, i: usize, v: CborRef<'a>) -> Option<Value<'a>> {
        if v.is_null() {
            return Some(Value::Null);
        }
        Some(match self.definition.fields()[i].kind {
            Type::Scalar(Scalar::Cbor) => Value::ValidatedCbor(v),
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
        pop: &[u8],
        encoder: &mut FrameEncoder,
    ) -> Result<Vec<u8>> {
        let mut values = BTreeMap::new();
        for item in CborMap::new(pop, 64).map_err(|e| e.to_string())? {
            let (k, v) = item.map_err(|e| e.to_string())?;
            let key = u16::try_from(k.unsigned().ok_or("invalid population key")?)
                .map_err(|_| "invalid population key")?;
            if key == self.keys[self.id] || values.insert(key, v).is_some() {
                return bad("duplicate population field");
            }
        }
        let raw: Vec<_> = chain
            .iter()
            .map(|(_, v)| {
                let mut b = Vec::new();
                head(0, *v, &mut b);
                b
            })
            .collect();
        for ((key, _), v) in chain.iter().zip(&raw) {
            if values.insert(*key, CborRef::parse(v).unwrap()).is_some() {
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
            head(5, fallback.len() as u64, &mut extra);
            for (k, v) in fallback {
                head(0, u64::from(*k), &mut extra);
                extra.extend_from_slice(v.as_bytes());
            }
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
                        Value::ValidatedCbor(CborRef::parse(&extra)?)
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
pub fn head(major: u8, n: u64, out: &mut Vec<u8>) {
    let p = major << 5;
    if n < 24 {
        out.push(p | n as u8)
    } else if n <= u8::MAX as u64 {
        out.extend_from_slice(&[p | 24, n as u8])
    } else if n <= u16::MAX as u64 {
        out.push(p | 25);
        out.extend_from_slice(&(n as u16).to_be_bytes())
    } else if n <= u32::MAX as u64 {
        out.push(p | 26);
        out.extend_from_slice(&(n as u32).to_be_bytes())
    } else {
        out.push(p | 27);
        out.extend_from_slice(&n.to_be_bytes())
    }
}
