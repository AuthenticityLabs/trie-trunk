use crate::{Definition, Error};
use ciborium::Value;
use std::collections::HashSet;

pub(crate) fn parse(bytes: &[u8]) -> Result<Value, Error> {
    let mut remaining = bytes;
    let value: Value = ciborium::de::from_reader_with_recursion_limit(&mut remaining, 64)
        .map_err(|_| Error::Invalid("malformed CBOR or nesting exceeds 64"))?;
    if !remaining.is_empty() {
        return Err(Error::Invalid("CBOR range contains trailing bytes"));
    }
    Ok(value)
}

pub(crate) fn dynamic_fields(bytes: &[u8], definition: &Definition) -> Result<(), Error> {
    let Value::Map(entries) = parse(bytes)? else {
        return Err(Error::Invalid("@fields must contain a CBOR map"));
    };
    let mut names = HashSet::new();
    for (key, _) in entries {
        let Value::Text(name) = key else {
            return Err(Error::Invalid("@fields keys must be text"));
        };
        if !names.insert(name.clone()) {
            return Err(Error::Invalid("duplicate @fields key"));
        }
        if name.starts_with('@') || definition.fields().iter().any(|field| field.name == name) {
            return Err(Error::Invalid(
                "dynamic attribute collides with a defined or reserved field",
            ));
        }
    }
    Ok(())
}
