use crate::{Error, Scalar, Type, cbor};
use ciborium::Value;
use std::collections::HashSet;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Field {
    pub name: String,
    pub kind: Type,
    pub values: Vec<String>,
}

impl Field {
    pub fn new(name: impl Into<String>, kind: impl Into<Type>) -> Self {
        Self {
            name: name.into(),
            kind: kind.into(),
            values: Vec::new(),
        }
    }

    pub fn mapped(
        name: impl Into<String>,
        values: impl IntoIterator<Item = impl Into<String>>,
    ) -> Self {
        Self {
            name: name.into(),
            kind: Scalar::String.into(),
            values: values.into_iter().map(Into::into).collect(),
        }
    }

    pub fn reference_width(&self) -> Option<usize> {
        let maximum = self.values.len().checked_sub(1)? as u64;
        Some(match maximum {
            0..=255 => 1,
            256..=65535 => 2,
            65536..=4294967295 => 4,
            _ => 8,
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Definition {
    identity: String,
    version: u64,
    fields: Vec<Field>,
}

impl Definition {
    pub fn new(
        identity: impl Into<String>,
        version: u64,
        fields: Vec<Field>,
    ) -> Result<Self, Error> {
        let identity = identity.into();
        if identity.is_empty() || fields.is_empty() {
            return Err(Error::Definition(
                "identity and field list must be nonempty".into(),
            ));
        }
        let mut names = HashSet::new();
        for field in &fields {
            if field.name.is_empty() || !names.insert(field.name.as_str()) {
                return Err(Error::Definition(
                    "field names must be nonempty and unique".into(),
                ));
            }
            if field.name.starts_with('@')
                && (field.name != "@fields" || field.kind != Scalar::Cbor.into())
            {
                return Err(Error::Definition(
                    "only @fields:CBOR is a supported reserved field".into(),
                ));
            }
            if !field.values.is_empty() && field.kind != Scalar::String.into() {
                return Err(Error::Definition("mapped values require STRING".into()));
            }
            let mut values = HashSet::new();
            if field.values.iter().any(|v| !values.insert(v)) {
                return Err(Error::Definition("mapped values must be unique".into()));
            }
        }
        Ok(Self {
            identity,
            version,
            fields,
        })
    }

    pub fn identity(&self) -> &str {
        &self.identity
    }
    pub fn version(&self) -> u64 {
        self.version
    }
    pub fn fields(&self) -> &[Field] {
        &self.fields
    }

    pub fn to_cbor(&self) -> Result<Vec<u8>, Error> {
        let fields = self
            .fields
            .iter()
            .map(|field| {
                let mut entries = vec![
                    (Value::from("name"), Value::from(field.name.clone())),
                    (Value::from("type"), Value::from(field.kind.to_string())),
                ];
                if !field.values.is_empty() {
                    entries.push((
                        Value::from("values"),
                        Value::Array(field.values.iter().cloned().map(Value::from).collect()),
                    ));
                }
                Value::Map(entries)
            })
            .collect();
        let value = Value::Map(vec![
            (Value::from("pil"), Value::from(0)),
            (
                Value::from("definition"),
                Value::from(self.identity.clone()),
            ),
            (Value::from("version"), Value::from(self.version)),
            (Value::from("fields"), Value::Array(fields)),
        ]);
        let mut bytes = Vec::new();
        ciborium::ser::into_writer(&value, &mut bytes)
            .map_err(|_| Error::Invalid("cannot encode Definition CBOR"))?;
        Ok(bytes)
    }

    pub fn from_cbor(bytes: &[u8]) -> Result<Self, Error> {
        let value = cbor::parse(bytes)?;
        let map = map(&value)?;
        if integer(required(map, "pil")?)? != 0 {
            return Err(Error::Definition("unsupported PIL version".into()));
        }
        let identity = text(required(map, "definition")?)?.to_owned();
        let version = integer(required(map, "version")?)?;
        let Value::Array(entries) = required(map, "fields")? else {
            return Err(Error::Definition("fields must be an array".into()));
        };
        let mut fields = Vec::with_capacity(entries.len());
        for entry in entries {
            let field_map = self::map(entry)?;
            let name = text(required(field_map, "name")?)?.to_owned();
            let kind = text(required(field_map, "type")?)?.parse()?;
            let mut values = Vec::new();
            if let Some(value) = get(field_map, "values") {
                let Value::Array(items) = value else {
                    return Err(Error::Definition("values must be an array".into()));
                };
                for item in items {
                    values.push(text(item)?.to_owned());
                }
                if kind != Type::Scalar(Scalar::String) {
                    return Err(Error::Definition("mapped values require STRING".into()));
                }
            }
            fields.push(Field { name, kind, values });
        }
        Self::new(identity, version, fields)
    }
}

fn map(value: &Value) -> Result<&[(Value, Value)], Error> {
    match value {
        Value::Map(entries) => {
            let mut keys = HashSet::new();
            for (key, _) in entries {
                if !keys.insert(text(key)?) {
                    return Err(Error::Definition("duplicate Definition map key".into()));
                }
            }
            Ok(entries)
        }
        _ => Err(Error::Definition("expected CBOR map".into())),
    }
}

fn get<'a>(entries: &'a [(Value, Value)], name: &str) -> Option<&'a Value> {
    entries
        .iter()
        .find_map(|(key, value)| (key.as_text() == Some(name)).then_some(value))
}

fn required<'a>(entries: &'a [(Value, Value)], name: &str) -> Result<&'a Value, Error> {
    get(entries, name).ok_or_else(|| Error::Definition(format!("missing {name}")))
}

fn text(value: &Value) -> Result<&str, Error> {
    value
        .as_text()
        .ok_or_else(|| Error::Definition("expected text".into()))
}

fn integer(value: &Value) -> Result<u64, Error> {
    value
        .as_integer()
        .and_then(|v| u64::try_from(v).ok())
        .ok_or_else(|| Error::Definition("expected unsigned integer".into()))
}
