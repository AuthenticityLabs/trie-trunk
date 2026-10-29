use base64::{Engine, engine::general_purpose::STANDARD};
use ciborium::Value;
use serde_json::{Map, Value as Json};
use std::collections::BTreeMap;
use std::io;

use crate::store::invalid;

type Fields = BTreeMap<u16, Field>;
type Domains = BTreeMap<String, BTreeMap<u16, String>>;

#[derive(Default)]
pub(crate) struct Field {
    pub name: String,
    pub enums: BTreeMap<u16, String>,
    object: Option<Fields>,
    array: Option<Fields>,
    pub encoding: String,
    unit: String,
}

pub(crate) struct Schema {
    pub id: u16,
    pub duration: Option<u16>,
    pub boundary: Option<u16>,
    pub fields: Fields,
}

pub(crate) fn decode(data: &[u8]) -> io::Result<Value> {
    pil_codec::object::decode_value(data).map_err(invalid)
}

pub(crate) fn unsigned(value: &Value) -> io::Result<u64> {
    match value {
        Value::Integer(n) => u64::try_from(*n).map_err(invalid),
        _ => Err(invalid("expected unsigned PIL integer")),
    }
}

pub(crate) fn record(value: Value) -> io::Result<BTreeMap<u16, Value>> {
    let Value::Map(pairs) = value else {
        return Err(invalid("expected PIL object"));
    };
    let mut fields = BTreeMap::new();
    for (key, value) in pairs {
        let key = u16::try_from(unsigned(&key)?).map_err(invalid)?;
        if fields.insert(key, value).is_some() {
            return Err(invalid("duplicate PIL field"));
        }
    }
    Ok(fields)
}

fn text(value: Value) -> io::Result<String> {
    match value {
        Value::Text(text) => Ok(text),
        _ => Err(invalid("expected PIL text")),
    }
}

fn labels(value: Value) -> io::Result<BTreeMap<u16, String>> {
    record(value)?
        .into_iter()
        .map(|(key, value)| Ok((key, text(value)?)))
        .collect()
}

fn parse_fields(value: Value, domains: &Domains) -> io::Result<Fields> {
    let mut fields = Fields::new();
    for (key, value) in record(value)? {
        let mut field = Field::default();
        let mut domain = String::new();
        for (key, value) in record(value)? {
            match key {
                0 => field.name = text(value)?,
                1 => domain = text(value)?,
                2 => field.enums = labels(value)?,
                3 => field.object = Some(parse_fields(value, domains)?),
                4 => field.array = Some(parse_fields(value, domains)?),
                5 => field.unit = text(value)?,
                6 => field.encoding = text(value)?,
                _ => {}
            }
        }
        if let Some(values) = domains.get(&domain) {
            for (&key, value) in values {
                field.enums.entry(key).or_insert_with(|| value.clone());
            }
        }
        fields.insert(key, field);
    }
    Ok(fields)
}

impl Schema {
    pub fn parse(data: &[u8], version: u32) -> io::Result<Self> {
        let mut definition = record(decode(data)?)?;
        if definition.get(&0).map(unsigned).transpose()? != Some(u64::from(version)) {
            return Err(invalid("schema version mismatch"));
        }
        let object = definition.remove(&8).map(text).transpose()?;
        if let Some(name) = object.as_deref()
            && (!crate::store::valid_tap(name) || version >> 20 == 0)
        {
            return Err(invalid("invalid object identity"));
        }
        let timezone = definition
            .remove(&4)
            .map(text)
            .transpose()?
            .ok_or_else(|| invalid("missing timezone"))?;
        let storage = definition.remove(&7).map(record).transpose()?;
        if timezone.is_empty() || storage.is_none() && timezone != "UTC" {
            return Err(invalid("unsupported timezone"));
        }
        let boundary = if let Some(mut policy) = storage {
            if !matches!(policy.get(&0).map(unsigned).transpose()?, Some(3 | 4)) {
                return Err(invalid("unsupported storage policy"));
            }
            let bucket = policy.remove(&2).map(text).transpose()?;
            if !matches!(bucket.as_deref(), Some("hour" | "day")) {
                return Err(invalid("unsupported bucket policy"));
            }
            let name = policy
                .remove(&1)
                .map(text)
                .transpose()?
                .filter(|s| !s.is_empty())
                .ok_or_else(|| invalid("missing partition boundary"))?;
            Some(name)
        } else {
            None
        };
        let mut domains = Domains::new();
        if let Some(value) = definition.remove(&3) {
            let Value::Map(pairs) = value else {
                return Err(invalid("invalid schema domains"));
            };
            for (name, values) in pairs {
                if domains.insert(text(name)?, labels(values)?).is_some() {
                    return Err(invalid("duplicate schema domain"));
                }
            }
        }
        let fields = parse_fields(
            definition
                .remove(&1)
                .ok_or_else(|| invalid("missing schema fields"))?,
            &domains,
        )?;
        let (mut id, mut duration) = (None, None);
        for (&key, field) in &fields {
            match field.name.as_str() {
                "id" => {
                    if id.is_some() || field.encoding != "uuidv7" {
                        return Err(invalid("schema requires one uuidv7 id"));
                    }
                    id = Some(key);
                }
                "duration" => {
                    if duration.is_some() || field.unit != "us" {
                        return Err(invalid("schema requires one duration in us"));
                    }
                    duration = Some(key);
                }
                _ => {}
            }
        }
        let mut owner_key = None;
        if let Some(name) = boundary
            && let Some(path) = definition.remove(&5)
        {
            let Value::Array(path) = path else {
                return Err(invalid("invalid trie path"));
            };
            let mut seen = std::collections::HashSet::new();
            if path.len() > 32 {
                return Err(invalid("trie path too long"));
            }
            for key in path {
                let key = u16::try_from(unsigned(&key)?).map_err(invalid)?;
                if !seen.insert(key) || Some(key) == id || Some(key) == duration {
                    return Err(invalid("invalid trie path field"));
                }
                let field = fields
                    .get(&key)
                    .ok_or_else(|| invalid("unknown trie path field"))?;
                if field.name == name && owner_key.replace(key).is_some() {
                    return Err(invalid("ambiguous partition boundary"));
                }
            }
        }
        Ok(Self {
            id: id.ok_or_else(|| invalid("missing uuidv7 id"))?,
            duration: if object.is_some() {
                duration
            } else {
                Some(duration.ok_or_else(|| invalid("missing duration"))?)
            },
            boundary: owner_key,
            fields,
        })
    }

    pub fn contains(&self, key: u16) -> bool {
        self.fields.contains_key(&key)
    }

    pub fn key(&self, name: &str) -> io::Result<Option<u16>> {
        let mut keys = self.fields.iter().filter(|(_, field)| field.name == name);
        let key = keys.next().map(|(&key, _)| key);
        if keys.next().is_some() {
            return Err(invalid(format!("ambiguous field name: {name}")));
        }
        Ok(key)
    }

    pub fn equals(&self, key: u16, value: &Value, expected: &Json) -> bool {
        if let Some(field) = self.fields.get(&key) {
            if let Ok(number) = unsigned(value)
                && let Ok(number) = u16::try_from(number)
                && let Some(label) = field.enums.get(&number)
            {
                return expected.as_str() == Some(label.as_str());
            }
            if field.encoding == "uuidv7"
                && let Value::Bytes(bytes) = value
                && bytes.len() == 16
            {
                return expected.as_str().is_some_and(|text| text == uuid(bytes));
            }
        }
        match (value, expected) {
            (Value::Text(value), Json::String(expected)) => value == expected,
            (Value::Bool(value), Json::Bool(expected)) => value == expected,
            (Value::Integer(value), Json::Number(expected)) => {
                let value = i128::from(*value);
                if let Some(expected) = expected.as_u64() {
                    value == i128::from(expected)
                } else if let Some(expected) = expected.as_i64() {
                    value == i128::from(expected)
                } else {
                    equal_integer_float(value, expected.as_f64().unwrap())
                }
            }
            (Value::Float(value), Json::Number(expected)) => {
                if let Some(expected) = expected.as_u64() {
                    equal_integer_float(i128::from(expected), *value)
                } else if let Some(expected) = expected.as_i64() {
                    equal_integer_float(i128::from(expected), *value)
                } else {
                    Some(*value) == expected.as_f64()
                }
            }
            (Value::Bytes(bytes), Json::String(expected)) => STANDARD.encode(bytes) == *expected,
            _ => false,
        }
    }

    pub fn expand(&self, values: BTreeMap<u16, Value>) -> io::Result<Map<String, Json>> {
        expand(values, &self.fields)
    }

    pub fn expand_field(&self, key: u16, value: Value) -> io::Result<(String, Json)> {
        expand_named_field(key, value, &self.fields)
    }
}

fn equal_integer_float(integer: i128, float: f64) -> bool {
    float >= i128::MIN as f64
        && float < -(i128::MIN as f64)
        && float.fract() == 0.0
        && float as i128 == integer
}

fn expand(values: BTreeMap<u16, Value>, fields: &Fields) -> io::Result<Map<String, Json>> {
    let mut result = Map::new();
    for (key, value) in values {
        let (name, value) = expand_named_field(key, value, fields)?;
        if result.insert(name, value).is_some() {
            return Err(invalid("duplicate projected field name"));
        }
    }
    Ok(result)
}

fn expand_named_field(key: u16, value: Value, fields: &Fields) -> io::Result<(String, Json)> {
    if let Some(field) = fields.get(&key) {
        Ok((field.name.clone(), expand_field(value, field)?))
    } else {
        Ok((key.to_string(), json(value)?))
    }
}

fn expand_field(value: Value, field: &Field) -> io::Result<Json> {
    if matches!(value, Value::Null) {
        return Ok(Json::Null);
    }
    if field.encoding == "uuidv7"
        && let Value::Bytes(bytes) = &value
        && bytes.len() == 16
        && bytes[6] >> 4 == 7
        && bytes[8] >> 6 == 2
    {
        return Ok(Json::String(uuid(bytes)));
    }
    if let Ok(n) = unsigned(&value)
        && let Ok(key) = u16::try_from(n)
        && let Some(label) = field.enums.get(&key)
    {
        return Ok(Json::String(label.clone()));
    }
    if let Some(fields) = &field.object {
        return Ok(Json::Object(expand(record(value)?, fields)?));
    }
    if let Some(fields) = &field.array {
        let Value::Array(items) = value else {
            return Err(invalid("expected object array"));
        };
        return items
            .into_iter()
            .map(|item| Ok(Json::Object(expand(record(item)?, fields)?)))
            .collect::<io::Result<Vec<_>>>()
            .map(Json::Array);
    }
    json(value)
}

fn uuid(bytes: &[u8]) -> String {
    use std::fmt::Write;
    let mut result = String::with_capacity(36);
    for (i, byte) in bytes.iter().enumerate() {
        if matches!(i, 4 | 6 | 8 | 10) {
            result.push('-');
        }
        write!(result, "{byte:02x}").unwrap();
    }
    result
}

pub(crate) fn json(value: Value) -> io::Result<Json> {
    Ok(match value {
        Value::Integer(n) => {
            if let Ok(n) = u64::try_from(n) {
                Json::from(n)
            } else {
                Json::from(i64::try_from(n).map_err(invalid)?)
            }
        }
        Value::Float(n) => Json::Number(
            serde_json::Number::from_f64(n).ok_or_else(|| invalid("non-finite JSON number"))?,
        ),
        Value::Text(text) => Json::String(text),
        Value::Bytes(bytes) => Json::String(STANDARD.encode(bytes)),
        Value::Bool(value) => Json::Bool(value),
        Value::Null => Json::Null,
        Value::Array(items) => Json::Array(items.into_iter().map(json).collect::<io::Result<_>>()?),
        Value::Map(pairs) => {
            let mut result = Map::new();
            for (key, value) in pairs {
                let key = match key {
                    Value::Text(key) => key,
                    Value::Integer(key) => i128::from(key).to_string(),
                    Value::Bool(key) => key.to_string(),
                    _ => return Err(invalid("unsupported JSON object key")),
                };
                if result.insert(key, json(value)?).is_some() {
                    return Err(invalid("duplicate JSON object key"));
                }
            }
            Json::Object(result)
        }
        _ => return Err(invalid("unsupported PIL value")),
    })
}
