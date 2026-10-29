use super::*;
use std::{
    ops::Bound,
    sync::{Arc, atomic::AtomicBool},
    time::Instant,
};
use tantivy::{
    Index, IndexReader, Term,
    collector::DocSetCollector,
    query::{BooleanQuery, Occur, Query, RangeQuery},
};

// Independent control: the old address-term query, kept only for parity and
// explicit performance evaluation. Production never builds this range bitset.
fn previous(index: &Index, reader: &IndexReader, plan: &QueryPlan<'_>) -> Result<Vec<u64>> {
    let s = reader.searcher();
    let schema = index.schema();
    let field = schema.get_field("r")?;
    let ranges: Vec<_> = plan
        .ranges
        .iter()
        .filter(|(a, b)| a < b)
        .map(|&(a, b)| {
            (
                Occur::Should,
                Box::new(RangeQuery::new(
                    Bound::Included(Term::from_field_u64(field, a)),
                    Bound::Excluded(Term::from_field_u64(field, b)),
                )) as Box<dyn Query>,
            )
        })
        .collect();
    let mut clauses = vec![(
        Occur::Must,
        Box::new(BooleanQuery::new(ranges)) as Box<dyn Query>,
    )];
    for f in plan.filters {
        clauses.push((Occur::Must, predicate(&schema, f)?));
    }
    if !plan.text.is_empty() {
        clauses.push((Occur::Must, text_query(index, &s, plan)?));
    }
    let docs = s.search(&BooleanQuery::new(clauses), &DocSetCollector)?;
    let columns = s
        .segment_readers()
        .iter()
        .map(|s| Ok((s.fast_fields().u64("rh")?, s.fast_fields().u64("rn")?)))
        .collect::<tantivy::Result<Vec<_>>>()?;
    let mut rows: Vec<_> = docs
        .into_iter()
        .map(|d| {
            let (h, n) = &columns[d.segment_ord as usize];
            h.first(d.doc_id).unwrap() << 32 | n.first(d.doc_id).unwrap()
        })
        .collect();
    rows.sort_unstable();
    rows.dedup();
    Ok(rows)
}
fn reference(n: u64) -> u64 {
    ((n / 1000) << 32) | (n % 1000 + 1)
}
fn fixture(count: u64, deleted: Option<u64>) -> Result<(Index, IndexReader)> {
    let index = Index::create_in_ram(schema(&["s".into(), "u".into()]));
    let mut writer = index.writer_with_num_threads(1, 15_000_000)?;
    // Deliberately reverse insertion order: document IDs cannot stand in for time.
    for n in (0..count).rev() {
        let token = if n % 100 == 0 { "needle" } else { "ordinary" };
        writer.add_document(document(
            &index.schema(),
            reference(n),
            &[
                Cell {
                    k: 0,
                    t: vec![format!("common {token}")],
                    ..Default::default()
                },
                Cell {
                    k: 1,
                    u: vec![n],
                    ..Default::default()
                },
            ],
        )?)?;
    }
    writer.commit()?;
    if let Some(n) = deleted {
        writer.delete_term(Term::from_field_u64(
            index.schema().get_field("r")?,
            reference(n),
        ));
        writer.commit()?;
    }
    let reader = index.reader()?;
    Ok((index, reader))
}
#[test]
fn clips_disjoint_unsorted_ranges_and_respects_deletions() -> Result<()> {
    let (index, reader) = fixture(2500, Some(1100))?;
    let ranges = [
        (reference(2000), reference(2200)),
        (reference(99), reference(103)),
        (reference(1000), reference(1101)),
        (reference(100), reference(102)),
        (8, 8),
        (9, 2),
    ];
    let mut p = QueryPlan {
        ranges: &ranges,
        filters: &[],
        text: "common",
        mode: "terms",
    };
    let expected: Vec<_> = (0..2500)
        .filter(|&n| {
            n != 1100
                && ((99..103).contains(&n)
                    || (1000..1101).contains(&n)
                    || (2000..2200).contains(&n))
        })
        .map(reference)
        .collect();
    assert_eq!(search(&index, &reader, &p, None)?, expected);
    assert_eq!(
        search(&index, &reader, &p, None)?,
        previous(&index, &reader, &p)?
    );
    p.text = "needle";
    assert_eq!(
        search(&index, &reader, &p, None)?,
        vec![
            reference(100),
            reference(1000),
            reference(2000),
            reference(2100)
        ]
    );
    let filters = [Filter {
        k: 1,
        kind: "u".into(),
        op: ">=".into(),
        value: "2100".into(),
        ..Default::default()
    }];
    p.filters = &filters;
    assert_eq!(search(&index, &reader, &p, None)?, vec![reference(2100)]);
    p.text = "";
    assert_eq!(
        search(&index, &reader, &p, None)?,
        previous(&index, &reader, &p)?
    );
    p.filters = &[];
    assert_eq!(search(&index, &reader, &p, None)?, expected);
    p.ranges = &[];
    assert!(search(&index, &reader, &p, None)?.is_empty());
    p.ranges = &[(7, 7), (9, 2)];
    assert!(search(&index, &reader, &p, None)?.is_empty());
    p.ranges = &ranges;
    assert!(search(&index, &reader, &p, Some(Arc::new(AtomicBool::new(true)))).is_err());
    Ok(())
}
#[test]
#[ignore = "explicit release-mode comparison of candidate clipping and old address-term ranges"]
fn benchmark_time_clipping() -> Result<()> {
    let (index, reader) = fixture(100_000, None)?;
    for (label, ranges) in [
        ("full", vec![(reference(0), reference(99_999) + 1)]),
        ("narrow", vec![(reference(50_000), reference(50_100))]),
    ] {
        for text in ["needle", "common"] {
            let p = QueryPlan {
                ranges: &ranges,
                filters: &[],
                text,
                mode: "terms",
            };
            let expected = previous(&index, &reader, &p)?;
            assert_eq!(search(&index, &reader, &p, None)?, expected);
            let mut old = Vec::new();
            let mut new = Vec::new();
            for i in 0..21 {
                // Alternate order to avoid always warming the same path first.
                for which in [i % 2, 1 - i % 2] {
                    let start = Instant::now();
                    let rows = if which == 0 {
                        previous(&index, &reader, &p)?
                    } else {
                        search(&index, &reader, &p, None)?
                    };
                    let elapsed = start.elapsed().as_micros();
                    assert_eq!(rows, expected);
                    if which == 0 {
                        old.push(elapsed);
                    } else {
                        new.push(elapsed);
                    }
                }
            }
            old.sort_unstable();
            new.sort_unstable();
            println!(
                "100000 docs {label} {text}: {} matches; old={}us candidate={}us (median/21)",
                expected.len(),
                old[10],
                new[10]
            );
        }
    }
    Ok(())
}
