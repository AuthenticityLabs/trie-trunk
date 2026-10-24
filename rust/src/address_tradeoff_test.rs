//! Opt-in, deterministic comparison using production schema/indexing/search code.
use super::query::predicate;
use super::*;
use std::path::Path;
use std::{collections::BTreeMap, fs, hint::black_box, time::Instant};
use tantivy::collector::DocSetCollector;
use tantivy::query::{BooleanQuery, Occur};

#[derive(Deserialize)]
struct Fixture {
    fields: Vec<String>,
    docs: Vec<Doc>,
    headers: Vec<u64>,
}

fn file_sizes(path: &Path) -> Result<BTreeMap<String, u64>> {
    let mut out = BTreeMap::new();
    for f in fs::read_dir(path)? {
        let f = f?;
        if f.file_type()?.is_file() {
            let name = f.file_name().to_string_lossy().to_string();
            let ext = Path::new(&name)
                .extension()
                .and_then(|s| s.to_str())
                .unwrap_or("other")
                .to_string();
            *out.entry(ext).or_default() += f.metadata()?.len();
        }
    }
    Ok(out)
}
fn median(mut v: Vec<f64>) -> f64 {
    v.sort_by(f64::total_cmp);
    v[v.len() / 2]
}

#[test]
#[ignore = "controlled storage/latency experiment; TRUNK_ADDRESS_BENCH required"]
fn compare_address_layouts() -> Result<()> {
    let root = std::env::var("TRUNK_ADDRESS_BENCH")?;
    let mut report = Vec::new();
    for name in [
        "dense50k",
        "dense250k",
        "sparse250k",
        "dense250k_small_segments",
    ] {
        for reference_only in [false, true] {
            // Each mode builds with exactly the same document order and commit batches.
            for tuple in [false, true] {
                let dir = Path::new(&root).join(name);
                let mut fixture: Fixture =
                    ciborium::from_reader(fs::File::open(dir.join("fixture.cbor"))?)?;
                let count = fixture.docs.len();
                let mode = if tuple { "tuple24" } else { "direct" };
                let scope = if reference_only {
                    "reference_only"
                } else {
                    "broad"
                };
                let path = dir.join(format!("tantivy_{scope}_{mode}"));
                if path.exists() {
                    fs::remove_dir_all(&path)?;
                }
                let fields = if reference_only {
                    vec![]
                } else {
                    fixture.fields.clone()
                };
                let threshold = (count as u64 / 2) * 101;
                let mut expected = Vec::new();
                let mut numeric = Vec::new();
                let mut compound = Vec::new();
                for (i, d) in fixture.docs.iter_mut().enumerate() {
                    let header = fixture.headers[i];
                    let row = d.row;
                    if tuple {
                        d.row = (header << 24) | (row - header);
                    }
                    // Assert the reference carries sufficient information to recover its row.
                    let restored = if tuple {
                        (d.row >> 24) + (d.row & 0xffffff)
                    } else {
                        d.row
                    };
                    assert_eq!(restored, row);
                    if d.cells.iter().any(|c| {
                        c.t.iter()
                            .any(|s| s.split_whitespace().any(|t| t == "timeout"))
                    }) {
                        expected.push(d.row);
                        if d.cells
                            .iter()
                            .any(|c| c.k == 8 && c.u.iter().any(|&n| n > threshold))
                        {
                            compound.push(d.row);
                        }
                    }
                    if d.cells
                        .iter()
                        .any(|c| c.k == 8 && c.u.iter().any(|&n| n > threshold))
                    {
                        numeric.push(d.row);
                    }
                    if reference_only {
                        d.cells.clear();
                    }
                }
                let mut request = Request {
                    path: path.to_string_lossy().into(),
                    binding: "address-experiment".into(),
                    fields,
                    ..Default::default()
                };
                let mut handle = Handle::open(&request, false)?;
                handle
                    .writer
                    .set_merge_policy(Box::new(tantivy::merge_policy::NoMergePolicy));
                let begin = Instant::now();
                for docs in fixture.docs.chunks(8192) {
                    handle.append(docs, docs.last().unwrap().source_row + 1, true)?;
                }
                let build_ms = begin.elapsed().as_secs_f64() * 1000.;
                let begin = Instant::now();
                let mut ordered = handle
                    .reader
                    .searcher()
                    .segment_readers()
                    .iter()
                    .map(|s| Ok((s.fast_fields().u64("r")?.min_value(), s.segment_id())))
                    .collect::<Result<Vec<_>>>()?;
                ordered.sort_by_key(|p| p.0);
                let segments: Vec<_> = ordered.into_iter().map(|p| p.1).collect();
                if segments.len() > 1 {
                    handle.writer.merge(&segments).wait()?;
                }
                handle.reader.reload()?;
                let merge_ms = begin.elapsed().as_secs_f64() * 1000.;
                // Close/reopen ensures old segments and merger threads are gone before sizing.
                handle.writer.wait_merging_threads()?;
                handle = Handle::open(&request, false)?;
                assert_eq!(handle.index.searchable_segment_ids()?.len(), 1);
                let sizes = file_sizes(&path)?;
                request.ranges = vec![(fixture.docs[0].row, fixture.docs.last().unwrap().row + 1)];
                let mut queries = BTreeMap::new();
                if !reference_only {
                    for kind in ["text", "numeric", "compound"] {
                        request.text = if kind == "numeric" {
                            String::new()
                        } else {
                            "timeout".into()
                        };
                        request.mode = "terms".into();
                        request.filters = if kind == "text" {
                            vec![]
                        } else {
                            vec![Filter {
                                k: 8,
                                kind: "u".into(),
                                op: ">".into(),
                                value: threshold.to_string(),
                                ..Default::default()
                            }]
                        };
                        let want = match kind {
                            "text" => &expected,
                            "numeric" => &numeric,
                            _ => &compound,
                        };
                        assert_eq!(&handle.search(&request)?, want);
                        for _ in 0..5 {
                            black_box(handle.search(&request)?);
                        }
                        let mut timings = Vec::new();
                        for _ in 0..31 {
                            let now = Instant::now();
                            let got = handle.search(&request)?;
                            timings.push(now.elapsed().as_secs_f64() * 1e6);
                            assert_eq!(got.len(), want.len());
                            black_box(got);
                        }
                        queries.insert(
                            kind,
                            serde_json::json!({"median_us":median(timings),"matches":want.len()}),
                        );
                    }
                }
                // Fast-field-only retrieval isolates address decoding from search execution.
                let searcher = handle.reader.searcher();
                let reader = &searcher.segment_readers()[0];
                let col = reader.fast_fields().u64("r")?;
                for (i, doc) in fixture.docs.iter().enumerate() {
                    assert_eq!(
                        col.first(i as u32),
                        Some(doc.row),
                        "merge reordered documents"
                    );
                }
                let mut access = BTreeMap::new();
                for pattern in ["sequential", "scattered"] {
                    let mut timings = Vec::new();
                    for _ in 0..7 {
                        let begin = Instant::now();
                        let mut sum = 0u64;
                        for j in 0..1_000_000usize {
                            let n = if pattern == "sequential" {
                                j % count
                            } else {
                                j * 7919 % count
                            };
                            sum = sum.wrapping_add(col.first(n as u32).unwrap());
                        }
                        black_box(sum);
                        timings.push(begin.elapsed().as_secs_f64() * 1000.);
                    }
                    access.insert(pattern, median(timings)); // milliseconds per million lookups == ns/lookup
                }
                drop(searcher);
                handle.writer.wait_merging_threads()?;
                let item = serde_json::json!({"case":name,"scope":scope,"mode":mode,"events":count,"bytes":sizes.values().sum::<u64>(),"files":sizes,"build_ms":build_ms,"merge_ms":merge_ms,"queries":queries,"fast_field_ns":access});
                println!("{item}");
                report.push(item);
            }
        }
    }
    fs::write(
        Path::new(&root).join("tantivy.json"),
        serde_json::to_vec_pretty(&report)?,
    )?;
    Ok(())
}

// UUID lookup comparison: current range-filtered path versus an exact-ID-only
// control. The control is valid for this full-source query, not arbitrary ranges.
#[test]
#[ignore = "object lookup experiment; TRUNK_ADDRESS_BENCH required"]
fn compare_object_lookup() -> Result<()> {
    let root = std::env::var("TRUNK_ADDRESS_BENCH")?;
    let mut report = Vec::new();
    for name in ["dense50k", "dense250k"] {
        let dir = Path::new(&root).join(name);
        let fixture: Fixture = ciborium::from_reader(fs::File::open(dir.join("fixture.cbor"))?)?;
        let mut objects = BTreeMap::<String, (Vec<u64>, u64)>::new();
        let mut ordered = Vec::new();
        for d in &fixture.docs {
            let id = &d.cells.iter().find(|c| c.k == 1).unwrap().e[0];
            let rev = d.cells.iter().find(|c| c.k == 2).unwrap().u[0];
            if !objects.contains_key(id) {
                ordered.push(id.clone());
            }
            let o = objects.entry(id.clone()).or_default();
            o.0.push(d.row);
            if rev == 7 {
                o.1 = d.row;
            }
        }
        let request = Request {
            path: dir.join("tantivy_broad_direct").to_string_lossy().into(),
            binding: "address-experiment".into(),
            fields: fixture.fields,
            ranges: vec![(fixture.docs[0].row, fixture.docs.last().unwrap().row + 1)],
            ..Default::default()
        };
        let handle = Handle::open(&request, false)?;
        for kind in ["history", "revision7", "missing"] {
            let mut requests = Vec::new();
            let mut expected = Vec::new();
            for id in &ordered {
                let mut value = id.clone();
                let want = if kind == "missing" {
                    let first = u8::from_str_radix(&value[..2], 16)? ^ 128;
                    value.replace_range(..2, &format!("{first:02x}"));
                    assert!(!objects.contains_key(&value));
                    vec![]
                } else if kind == "revision7" {
                    vec![objects[id].1]
                } else {
                    objects[id].0.clone()
                };
                let mut filters = vec![Filter {
                    k: 1,
                    kind: "e".into(),
                    op: "==".into(),
                    value,
                    ..Default::default()
                }];
                if kind == "revision7" {
                    filters.push(Filter {
                        k: 2,
                        kind: "u".into(),
                        op: "==".into(),
                        value: "7".into(),
                        ..Default::default()
                    });
                }
                requests.push(Request {
                    filters,
                    ranges: request.ranges.clone(),
                    ..Default::default()
                });
                expected.push(want);
            }
            for mode in ["current", "exact_only_control"] {
                let search = |r: &Request| -> Result<Vec<u64>> {
                    if mode == "current" {
                        return handle.search(r);
                    }
                    let schema = handle.index.schema();
                    let clauses = r
                        .filters
                        .iter()
                        .map(|f| Ok((Occur::Must, predicate(&schema, f)?)))
                        .collect::<Result<Vec<_>>>()?;
                    let searcher = handle.reader.searcher();
                    let hits = searcher.search(&BooleanQuery::new(clauses), &DocSetCollector)?;
                    let columns = searcher
                        .segment_readers()
                        .iter()
                        .map(|s| s.fast_fields().u64("r"))
                        .collect::<tantivy::Result<Vec<_>>>()?;
                    let mut rows: Vec<u64> = hits
                        .into_iter()
                        .filter_map(|d| columns[d.segment_ord as usize].first(d.doc_id))
                        .collect();
                    rows.sort_unstable();
                    rows.dedup();
                    Ok(rows)
                };
                for j in 0..128 {
                    let n = j * 7919 % ordered.len();
                    assert_eq!(search(&requests[n])?, expected[n]);
                }
                let mut samples = Vec::new();
                // Rotating UUIDs across 5 passes; excludes open/fixture/validation work.
                for pass in 0..5 {
                    for j in 0..256 {
                        let n = (pass * 256 + j) * 7919 % ordered.len();
                        let start = Instant::now();
                        let got = search(&requests[n])?;
                        samples.push(start.elapsed().as_secs_f64() * 1e6);
                        assert_eq!(got, expected[n]);
                        black_box(got);
                    }
                }
                samples.sort_by(f64::total_cmp);
                let item = serde_json::json!({"case":name,"events":fixture.docs.len(),"objects":ordered.len(),"kind":kind,"mode":mode,"queries":samples.len(),"median_us":samples[samples.len()/2],"p95_us":samples[samples.len()*95/100],"mean_us":samples.iter().sum::<f64>()/samples.len() as f64});
                println!("{item}");
                report.push(item);
            }
        }
        handle.writer.wait_merging_threads()?;
    }
    fs::write(
        Path::new(&root).join("object_lookup_native.json"),
        serde_json::to_vec_pretty(&report)?,
    )?;
    Ok(())
}
