//! Reproducible native-to-WASM reader parity fixture; no running server needed.
//! Uses the production schema/document builder and the native Tantivy features.
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{fs, path::PathBuf, time::Instant};
use tantivy::{Index, ReloadPolicy};
use trunk_search_core::{Cell, Filter, QueryPlan, Result};

fn main() -> Result<()> {
    let mut args = std::env::args().skip(1);
    let root = PathBuf::from(args.next().ok_or("output directory required")?);
    let hours: u64 = args.next().unwrap_or("8".into()).parse()?;
    let per_hour: u64 = args.next().unwrap_or("2000".into()).parse()?;
    if root.exists() {
        return Err("output directory already exists".into());
    }
    if hours == 0 || hours > 1000 || per_hour > 1 << 24 {
        return Err("invalid fixture dimensions".into());
    }
    fs::create_dir_all(&root)?;
    let start = Instant::now();
    let kinds = ["s", "u", "m", "f", "b"].map(String::from);
    let mut packs = Vec::new();
    let queries = json!([
        {"name":"all", "text":"", "mode":"contains"},
        {"name":"contains", "text":"timeout", "mode":"contains"},
        {"name":"terms", "text":"payment timeout", "mode":"terms"},
        {"name":"phrase", "text":"payment timeout", "mode":"phrase"},
        {"name":"prefix", "text":"payment time", "mode":"prefix"},
        {"name":"fuzzy", "text":"paymnt", "mode":"fuzzy"},
        {"name":"duration", "filters":[{"k":1,"kind":"u","op":">","value":"1000000"}]},
        {"name":"large-integer", "filters":[{"k":1,"kind":"u","op":"==","value":"18446744073709551615"}]},
        {"name":"mapped", "filters":[{"k":2,"kind":"m","op":"==","value":"error","code":1}]},
        {"name":"not-null", "filters":[{"k":1,"kind":"p","op":"==","value":"2"}]},
        {"name":"float", "filters":[{"k":3,"kind":"f","op":">=","value":"0.5"}]},
        {"name":"finite-not-equal", "filters":[{"k":3,"kind":"f","op":"!=","value":"0.5"}]},
        {"name":"bool", "filters":[{"k":4,"kind":"b","op":"==","value":"true"}]},
        {"name":"partial-hour", "text":"payment", "mode":"terms", "partial":true}
    ]);
    for hour in 0..hours {
        let directory = root.join(hour.to_string());
        fs::create_dir(&directory)?;
        let schema = trunk_search_core::schema(&kinds);
        let index = Index::create_in_dir(&directory, schema.clone())?;
        let mut writer = index.writer_with_num_threads(1, 15_000_000)?;
        let first = (hour + 256) << 24;
        for n in 0..per_hour {
            let text = match n % 4 {
                0 => "payment timeout",
                1 => "timeout during payment",
                2 => "payment accepted",
                _ => "identity session opened",
            };
            let mut cells = vec![
                Cell {
                    k: 0,
                    state: 2,
                    t: vec![text.into()],
                    c: vec![text.into()],
                    ..Default::default()
                },
                Cell {
                    k: 1,
                    state: if n % 7 == 0 { 1 } else { 2 },
                    u: if n % 7 == 0 {
                        vec![]
                    } else {
                        vec![if n == 1 { u64::MAX } else { (n % 3000) * 1000 }]
                    },
                    ..Default::default()
                },
                Cell {
                    k: 2,
                    state: 2,
                    u: vec![n % 2],
                    ..Default::default()
                },
                Cell {
                    k: 3,
                    state: 2,
                    f: vec![match n % 7 {
                        0 => f64::NAN,
                        1 => f64::INFINITY,
                        _ => (n % 100) as f64 / 100.0,
                    }],
                    ..Default::default()
                },
            ];
            if n % 3 != 0 {
                cells.push(Cell {
                    k: 4,
                    state: 2,
                    b: vec![n % 2 == 0],
                    ..Default::default()
                });
            }
            writer.add_document(trunk_search_core::document(&schema, first + n, &cells)?)?;
        }
        writer.commit()?;
        writer.wait_merging_threads()?;
        let reader = index
            .reader_builder()
            .reload_policy(ReloadPolicy::Manual)
            .try_into()?;
        let mut checks = Vec::new();
        for q in queries.as_array().unwrap() {
            let filters: Vec<Filter> =
                serde_json::from_value(q.get("filters").cloned().unwrap_or(json!([])))?;
            let ranges = if q["partial"] == true {
                vec![(first + per_hour / 3, first + per_hour * 2 / 3)]
            } else {
                vec![(first, first + per_hour)]
            };
            let text = q["text"].as_str().unwrap_or("");
            let mode = q["mode"].as_str().unwrap_or("contains");
            let t = Instant::now();
            let rows = trunk_search_core::search(
                &index,
                &reader,
                &QueryPlan {
                    ranges: &ranges,
                    filters: &filters,
                    text,
                    mode,
                },
                None,
            )?;
            let elapsed = t.elapsed().as_secs_f64() * 1000.0;
            if q["name"] == "all" {
                assert_eq!(rows.len() as u64, per_hour);
            }
            if q["name"] == "contains" || q["name"] == "terms" {
                assert_eq!(rows.len() as u64, per_hour / 4 * 2 + (per_hour % 4).min(2));
            }
            if q["name"] == "large-integer" && per_hour > 1 {
                assert_eq!(rows, vec![first + 1]);
            }
            let mut digest = Sha256::new();
            for row in &rows {
                digest.update(row.to_le_bytes());
            }
            checks.push(json!({"name":q["name"], "query":{"ranges":ranges,"filters":filters,"text":text,"mode":mode},"count":rows.len(),"sha256":format!("{:x}",digest.finalize()),"nativeMs":elapsed}));
        }
        let mut files: Vec<Value> = Vec::new();
        // Only committed files referenced by this immutable metadata snapshot.
        // Writer locks and .managed.json are not replication dependencies.
        let meta = index.load_metas()?;
        let mut names: std::collections::BTreeSet<PathBuf> = meta
            .segments
            .iter()
            .flat_map(|s| s.list_files())
            .filter(|p| directory.join(p).exists())
            .collect();
        names.insert("meta.json".into());
        for name in names {
            let bytes = fs::read(directory.join(&name))?;
            files.push(json!({"id":files.len(),"name":name.to_str().unwrap(),"len":bytes.len(),"sha256":format!("{:x}",Sha256::digest(&bytes))}));
        }
        packs.push(json!({"hour":hour,"rows":per_hour,"files":files,"checks":checks}));
    }
    fs::write(
        root.join("fixture.json"),
        serde_json::to_vec(&json!({"hours":packs,"buildMs":start.elapsed().as_secs_f64()*1000.0}))?,
    )?;
    println!("{} hours × {} rows: {}", hours, per_hour, root.display());
    Ok(())
}
