use chrono::{DateTime, Timelike};
use serde::{
    Serialize, Serializer,
    ser::{SerializeMap, SerializeSeq},
};
use serde_json::{Map, Value};
use std::cmp::Reverse;
use std::collections::{BinaryHeap, HashSet};
use std::io::{self, Write};
use std::path::PathBuf;
use std::time::Duration;

use crate::filter::Filter;
use crate::schema;
use crate::store::{self, Cursor, Reader, Source, invalid};

pub struct Config {
    pub root: PathBuf,
    pub tap: String,
    pub start: u64,
    pub end: u64,
    pub headers: Vec<String>,
    pub pretty: bool,
    pub compact: bool,
    pub pil: bool,
    pub pil_v2: bool,
    pub pil_known: Vec<String>,
    pub limit: Option<usize>,
    pub tail: bool,
    pub follow: bool,
    pub clauses: Vec<String>,
}

pub fn run(config: &Config, output: &mut impl Write) -> io::Result<()> {
    if config.root.as_os_str().is_empty() || config.start > config.end || config.end > 1 << 48 {
        return Err(invalid(
            "search requires a root and an increasing window within UUIDv7 time range",
        ));
    }
    let mut renderer = Renderer::new(config);
    if config.pil {
        output.write_all(if config.pil_v2 {
            crate::pil::MAGIC_V2
        } else {
            crate::pil::MAGIC
        })?;
    }
    let mut reader = Reader::new(
        config.follow,
        config.compact || config.pil,
        Filter::parse(&config.clauses)?,
    );
    let mut scratch = vec![0; 65536];
    let days = discover(config)?;
    let mut days: Vec<_> = days.into_iter().collect();
    if config.tail {
        days.reverse();
    }
    let mut remaining = config.limit.unwrap_or(usize::MAX);
    let mut selected = Vec::new();
    let mut sources = Vec::new();
    for (day, versions) in days {
        crate::runtime::check()?;
        if remaining == 0 && !config.follow {
            break;
        }
        let bounds = if remaining == 0 {
            0..0
        } else {
            config.start..config.end
        };
        let mut runs = select_day(
            &mut reader,
            day,
            versions,
            bounds,
            config.tail,
            config.limit.map(|_| remaining),
            &mut sources,
        )?;
        while remaining > 0
            && let Some(mut current) = runs.peek_mut()
        {
            crate::runtime::check()?;
            let cursor = &mut current.0;
            let source = &sources[cursor.source];
            let matched = if config.tail {
                if source.matches(cursor, &mut scratch)? {
                    cursor.validate(source, &mut scratch)?;
                    selected.push(cursor.clone());
                    true
                } else {
                    false
                }
            } else {
                renderer.write(source, cursor, &mut scratch, output)?
            };
            if matched {
                remaining -= 1;
            }
            if remaining == 0 {
                break;
            }
            if !cursor.advance(source)? {
                std::collections::binary_heap::PeekMut::pop(current);
            }
        }
        if !config.tail {
            sources.clear();
        }
    }
    for cursor in selected.iter_mut().rev() {
        renderer.write(&sources[cursor.source], cursor, &mut scratch, output)?;
    }
    drop(selected);
    sources.clear();
    output.flush()?;
    if !config.follow {
        if config.pil {
            output.write_all(&[crate::pil::END])?;
            output.flush()?;
        }
        return Ok(());
    }
    loop {
        crate::runtime::check()?;
        std::thread::sleep(Duration::from_millis(250));
        for (day, versions) in discover(config)? {
            let mut runs = select_day(
                &mut reader,
                day,
                versions,
                config.start..config.end,
                false,
                None,
                &mut sources,
            )?;
            let mut wrote = false;
            while let Some(mut current) = runs.peek_mut() {
                crate::runtime::check()?;
                let cursor = &mut current.0;
                let source = &sources[cursor.source];
                wrote |= renderer.write(source, cursor, &mut scratch, output)?;
                if !cursor.advance(source)? {
                    std::collections::binary_heap::PeekMut::pop(current);
                }
            }
            if wrote {
                output.flush()?;
            }
            sources.clear();
        }
    }
}

fn discover(
    config: &Config,
) -> io::Result<std::collections::BTreeMap<u64, Vec<crate::routing::Partition>>> {
    match crate::routing::discover(&config.root, &config.tap, config.start, config.end) {
        Err(error) if config.follow && error.kind() == io::ErrorKind::NotFound => {
            Ok(Default::default())
        }
        result => result,
    }
}

struct Renderer<'a> {
    headers: Vec<&'a str>,
    pretty: bool,
    compact: bool,
    pil: Option<crate::pil::Encoder>,
    active: Option<usize>,
    sent: HashSet<usize>,
    encoded: Vec<u8>,
}

impl<'a> Renderer<'a> {
    fn new(config: &'a Config) -> Self {
        let mut headers = Vec::new();
        for name in &config.headers {
            let name = name.trim();
            if !name.is_empty() && !headers.contains(&name) {
                headers.push(name);
            }
        }
        Self {
            headers,
            pretty: config.pretty,
            compact: config.compact,
            pil: config
                .pil
                .then(|| crate::pil::Encoder::new(&config.pil_known, config.pil_v2)),
            active: None,
            sent: HashSet::new(),
            encoded: Vec::new(),
        }
    }

    fn write(
        &mut self,
        source: &Source,
        cursor: &mut Cursor,
        scratch: &mut [u8],
        output: &mut impl Write,
    ) -> io::Result<bool> {
        if let Some(encoder) = &mut self.pil {
            let Some(record) = source.read_pil(cursor, scratch)? else {
                return Ok(false);
            };
            encoder.write(&source.catalog.read().unwrap(), &record, output)?;
            return Ok(true);
        }
        let Some(record) = source.read(cursor, scratch)? else {
            return Ok(false);
        };
        self.encoded.clear();
        if self.compact {
            let catalog = source.catalog.read().unwrap();
            if self.active != Some(catalog.stream) {
                let mut serializer = serde_json::Serializer::new(&mut self.encoded);
                let mut control = serializer.serialize_map(None)?;
                control.serialize_entry("schema", &catalog.stream)?;
                if self.sent.insert(catalog.stream) {
                    control.serialize_entry("tap", &catalog.tap)?;
                    control.serialize_entry("version", &catalog.version)?;
                    control.serialize_entry("headers", &self.headers)?;
                }
                SerializeMap::end(control)?;
                self.encoded.push(b'\n');
                self.active = Some(catalog.stream);
            }
            encode_compact(&mut self.encoded, record)?;
            self.encoded.push(b'\n');
            output.write_all(&self.encoded)?;
            return Ok(true);
        }
        let mut fields = source
            .catalog
            .read()
            .unwrap()
            .schema
            .expand(record.fields)?;
        if record
            .lineage
            .fields
            .iter()
            .any(|(name, _)| fields.contains_key(name))
        {
            return Err(invalid("duplicate projected field name"));
        }
        fields.insert(
            "time".into(),
            Value::String(render_time(record.millis * 1000)?),
        );
        encode(
            &mut self.encoded,
            &fields,
            &record.lineage.fields,
            &self.headers,
            self.pretty,
        )?;
        output.write_all(&self.encoded)?;
        Ok(true)
    }
}

fn encode_compact(output: &mut Vec<u8>, mut record: store::Record) -> io::Result<()> {
    let mut serializer = serde_json::Serializer::new(output);
    let mut values = serializer.serialize_seq(Some(
        2 * (record.fields.len() + record.lineage.fields.len()),
    ))?;
    if let Some(value) = record.fields.remove(&record.id) {
        values.serialize_element(&record.id)?;
        values.serialize_element(&schema::json(value)?)?;
    }
    for (key, (_, value)) in record.lineage.keys.iter().zip(&record.lineage.fields) {
        values.serialize_element(key)?;
        values.serialize_element(value)?;
    }
    for (key, value) in record.fields {
        values.serialize_element(&key)?;
        values.serialize_element(&schema::json(value)?)?;
    }
    SerializeSeq::end(values)?;
    Ok(())
}

pub(crate) fn render_time(micros: u64) -> io::Result<String> {
    use std::fmt::Write;
    let time = DateTime::from_timestamp_micros(micros as i64)
        .ok_or_else(|| invalid("invalid timestamp"))?;
    let mut text = time.format("%Y-%m-%dT%H:%M:%S").to_string();
    if time.nanosecond() != 0 {
        write!(text, ".{:06}", time.nanosecond() / 1000).unwrap();
        while text.ends_with('0') {
            text.pop();
        }
    }
    text.push('Z');
    Ok(text)
}

struct Ordered<'a> {
    record: &'a Map<String, Value>,
    lineage: &'a [(String, Value)],
    headers: &'a [&'a str],
}

impl Ordered<'_> {
    fn get(&self, name: &str) -> Option<&Value> {
        self.record.get(name).or_else(|| {
            self.lineage
                .iter()
                .find_map(|(key, value)| (key == name).then_some(value))
        })
    }
}

impl Serialize for Ordered<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let count = self.record.len()
            + self
                .lineage
                .iter()
                .filter(|(name, _)| !self.record.contains_key(name))
                .count();
        let mut map = serializer.serialize_map(Some(count))?;
        for name in self.headers {
            if let Some(value) = self.get(name) {
                map.serialize_entry(name, value)?;
            }
        }
        for name in ["id", "time"] {
            if !self.headers.contains(&name)
                && let Some(value) = self.get(name)
            {
                map.serialize_entry(name, value)?;
            }
        }
        for (name, value) in self.lineage {
            if !matches!(name.as_str(), "id" | "time")
                && !self.headers.contains(&name.as_str())
                && !self.record.contains_key(name)
            {
                map.serialize_entry(name, value)?;
            }
        }
        for (name, value) in self.record {
            if !matches!(name.as_str(), "id" | "time") && !self.headers.contains(&name.as_str()) {
                map.serialize_entry(name, value)?;
            }
        }
        map.end()
    }
}

pub(crate) fn encode(
    output: &mut Vec<u8>,
    record: &Map<String, Value>,
    lineage: &[(String, Value)],
    headers: &[&str],
    pretty: bool,
) -> io::Result<()> {
    let value = Ordered {
        record,
        lineage,
        headers,
    };
    if pretty {
        serde_json::to_writer_pretty(&mut *output, &value)?;
    } else {
        serde_json::to_writer(&mut *output, &value)?;
    }
    output.push(b'\n');
    Ok(())
}

fn select_day(
    reader: &mut Reader,
    _day: u64,
    versions: Vec<crate::routing::Partition>,
    bounds: std::ops::Range<u64>,
    backwards: bool,
    limit: Option<usize>,
    sources: &mut Vec<Source>,
) -> io::Result<BinaryHeap<Reverse<Cursor>>> {
    let mut runs = Vec::new();
    for (source, cursors) in reader
        .select_many(versions, bounds, backwards, limit)?
        .into_iter()
        .flatten()
    {
        let before = runs.len();
        for mut cursor in cursors {
            if cursor.seek(&source)? {
                cursor.source = sources.len();
                runs.push(Reverse(cursor));
            }
        }
        if before != runs.len() {
            sources.push(source);
        }
    }
    Ok(BinaryHeap::from(runs))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn ordered_json_preserves_values_and_indentation() {
        let value =
            json!({"z": "line\n<quoted> & 雪", "id": "uuid", "a": {"z": null, "a": [true, 12]}});
        for pretty in [true, false] {
            let mut bytes = Vec::new();
            encode(
                &mut bytes,
                value.as_object().unwrap(),
                &[],
                &["missing", "id"],
                pretty,
            )
            .unwrap();
            assert_eq!(serde_json::from_slice::<Value>(&bytes).unwrap(), value);
            let text = String::from_utf8(bytes).unwrap();
            assert!(text.starts_with(if pretty { "{\n  \"id\":" } else { "{\"id\":" }));
            assert!(text.ends_with('\n'));
            assert!(!text.contains("\\u003c"));
            if !pretty {
                assert_eq!(text.bytes().filter(|&b| b == b'\n').count(), 1);
            }
        }
    }

    #[test]
    fn cached_lineage_keeps_traversal_order() {
        let population = json!({"a": 1, "id": "uuid", "time": "derived", "z": [true, null]});
        let lineage = vec![
            ("service".into(), json!("vault")),
            ("level".into(), json!("ERROR")),
            ("time".into(), json!("stored")),
        ];
        let mut combined: Map<_, _> = lineage.iter().cloned().collect();
        combined.extend(population.as_object().unwrap().clone());
        for pretty in [false, true] {
            for (headers, names) in [
                (&[][..], ["id", "time", "service", "level", "a", "z"]),
                (
                    &["level", "absent", "id"][..],
                    ["level", "id", "time", "service", "a", "z"],
                ),
            ] {
                let mut bytes = Vec::new();
                encode(
                    &mut bytes,
                    population.as_object().unwrap(),
                    &lineage,
                    headers,
                    pretty,
                )
                .unwrap();
                assert_eq!(
                    serde_json::from_slice::<Value>(&bytes).unwrap(),
                    Value::Object(combined.clone())
                );
                let text = String::from_utf8(bytes).unwrap();
                let positions: Vec<_> = names
                    .iter()
                    .map(|name| {
                        let key = format!("\"{name}\":");
                        assert_eq!(text.matches(&key).count(), 1);
                        text.find(&key).unwrap()
                    })
                    .collect();
                assert!(positions.windows(2).all(|pair| pair[0] < pair[1]));
            }
        }
    }

    #[test]
    fn time_uses_only_significant_fractional_digits() {
        for (micros, expected) in [
            (0, "1970-01-01T00:00:00Z"),
            (123400, "1970-01-01T00:00:00.1234Z"),
            (999999, "1970-01-01T00:00:00.999999Z"),
            (86400000001, "1970-01-02T00:00:00.000001Z"),
        ] {
            assert_eq!(render_time(micros).unwrap(), expected);
        }
    }
}
