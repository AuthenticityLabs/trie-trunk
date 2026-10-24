//! Export live pages and expected histories for independent browser decoding.
use std::{fs::File, io::Write};
use trunk_uuid_index::{
    KeyKind,
    slotted::{Command, Index},
};
fn key(n: u64) -> [u8; 16] {
    let mut k = [0; 16];
    k[8..].copy_from_slice(&n.to_be_bytes());
    k
}
fn main() {
    let out = std::env::args().nth(1).expect("output directory");
    std::fs::create_dir_all(&out).unwrap();
    let temp = tempfile::tempdir().unwrap();
    let index = Index::create(temp.path().join("objects.idx"), KeyKind::Uuid, Some(100)).unwrap();
    for batch in (0..2048u64)
        .rev()
        .map(|n| Command::put(key(n * 2), 0, n + 1))
        .collect::<Vec<_>>()
        .chunks(128)
    {
        index.apply(batch).unwrap();
    }
    let updates: Vec<_> = (0..2048u64)
        .step_by(5)
        .flat_map(|n| {
            (1..=130).map(move |r| Command::put(key(n * 2), r, ((n + 1) << 52) + r as u64))
        })
        .collect();
    for batch in updates.chunks(512) {
        index.apply(batch).unwrap();
    }
    index.apply(&[Command::retire_before(key(20), 95)]).unwrap();
    let snapshot = index.snapshot().unwrap();
    let mut bytes = File::create(format!("{out}/pages.bin")).unwrap();
    for (_, page) in snapshot.export_pages().unwrap() {
        bytes.write_all(&page.0).unwrap();
    }
    let mut json = File::create(format!("{out}/queries.json")).unwrap();
    write!(json, "[").unwrap();
    for n in 0..4100 {
        if n > 0 {
            write!(json, ",").unwrap();
        }
        let k = key(n);
        write!(json, "{{\"key\":\"").unwrap();
        for b in k {
            write!(json, "{b:02x}").unwrap();
        }
        write!(json, "\",\"history\":[").unwrap();
        let mut first = true;
        snapshot
            .history(&k, |r, a| {
                if !first {
                    write!(json, ",").unwrap();
                }
                first = false;
                write!(json, "[{r},\"{a}\"]").unwrap();
            })
            .unwrap();
        write!(json, "]}}").unwrap();
    }
    writeln!(json, "]").unwrap();
    println!("{} pages; 4100 present/absent queries", snapshot.pages());
}
