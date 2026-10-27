use super::*;
fn cell(k: usize, texts: &[&str]) -> Cell {
    Cell {
        k,
        t: texts.iter().map(|s| s.to_string()).collect(),
        ..Default::default()
    }
}
#[test]
fn multiword_prefix_keeps_all_expansions_without_unbounded_allocation() -> Result<()> {
    let mut r = Request {
        binding: "multiword-prefix".into(),
        fields: vec!["s".into()],
        ranges: vec![(0, 120)],
        tail: 120,
        text: "payment token".into(),
        mode: "prefix".into(),
        ..Default::default()
    };
    let mut h = Handle::open(&r, true)?;
    let docs: Vec<_> = (0..120)
        .map(|row| Doc {
            source_row: row,
            row,
            cells: vec![cell(0, &[&format!("payment token{row:03}")])],
            ..Default::default()
        })
        .collect();
    h.append(&docs, 120, true)?;
    assert_eq!(h.search(&r)?, (0..120).collect::<Vec<_>>());
    r.text = "payment absent".into();
    assert!(h.search(&r)?.is_empty());
    Ok(())
}
#[test]
fn prefix_does_not_silently_truncate_matching_terms() -> Result<()> {
    let r = Request {
        binding: "prefix".into(),
        fields: vec!["s".into()],
        ranges: vec![(0, 120)],
        tail: 120,
        text: "token".into(),
        mode: "prefix".into(),
        ..Default::default()
    };
    let mut h = Handle::open(&r, true)?;
    let docs: Vec<_> = (0..120)
        .map(|row| Doc {
            id: String::new(),
            source_row: row,
            row,
            cells: vec![cell(0, &[&format!("token{row:03}")])],
        })
        .collect();
    h.append(&docs, 120, true)?;
    assert_eq!(h.search(&r)?.len(), 120);
    Ok(())
}
#[test]
fn text_modes_respect_values_and_do_not_store_payloads() -> Result<()> {
    let mut r = Request {
        binding: "test".into(),
        fields: vec!["s".into(), "s".into(), "u".into(), "b".into()],
        ranges: vec![(0, 3)],
        tail: 3,
        ..Default::default()
    };
    let mut h = Handle::open(&r, true)?;
    h.append(
        &[
            Doc {
                id: String::new(),
                source_row: 0,
                row: 0,
                cells: vec![
                    cell(0, &["connection", "timeout"]),
                    Cell {
                        k: 2,
                        u: vec![u64::MAX],
                        ..Default::default()
                    },
                ],
            },
            Doc {
                id: String::new(),
                source_row: 1,
                row: 1,
                cells: vec![
                    cell(0, &["CONNECTION timeout"]),
                    Cell {
                        k: 3,
                        b: vec![false],
                        ..Default::default()
                    },
                ],
            },
            Doc {
                id: String::new(),
                source_row: 2,
                row: 2,
                cells: vec![cell(0, &["connection"]), cell(1, &["timeout café"])],
            },
        ],
        3,
        true,
    )?;
    for entry in h.index.schema().fields().map(|(_, e)| e) {
        assert!(!entry.is_stored());
    }
    r.text = "connection timeout".into();
    r.mode = "terms".into();
    assert_eq!(h.search(&r)?, vec![0, 1, 2]);
    r.mode = "phrase".into();
    assert_eq!(h.search(&r)?, vec![1]);
    r.mode = "prefix".into();
    r.text = "connection time".into();
    assert_eq!(h.search(&r)?, vec![1]);
    r.mode = "fuzzy".into();
    r.text = "timout".into();
    assert_eq!(h.search(&r)?, vec![0, 1, 2]);
    r.mode = "terms".into();
    r.text = "CAFÉ".into();
    assert_eq!(h.search(&r)?, vec![2]);
    r.text.clear();
    r.filters = vec![Filter {
        code: None,
        k: 2,
        kind: "u".into(),
        op: "==".into(),
        value: u64::MAX.to_string(),
    }];
    assert_eq!(h.search(&r)?, vec![0]);
    r.filters = vec![Filter {
        code: None,
        k: 3,
        kind: "b".into(),
        op: "==".into(),
        value: "false".into(),
    }];
    assert_eq!(h.search(&r)?, vec![1]);
    Ok(())
}
