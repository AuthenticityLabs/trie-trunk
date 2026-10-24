use super::*;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};

fn uuid(n: u64) -> Uuid {
    let mut k = [0; 16];
    k[..6].copy_from_slice(&(1_700_000_000_000 + n / 100).to_be_bytes()[2..]);
    k[6] = 0x70;
    k[8] = 0x80;
    k[9..].copy_from_slice(&n.to_be_bytes()[1..]);
    k
}
fn rows(start: u64, end: u64) -> Vec<Entry> {
    (start..end)
        .map(|i| Entry {
            key: uuid(i),
            address: i * 8,
        })
        .collect()
}
fn random(state: &mut u64) -> u64 {
    *state ^= *state << 13;
    *state ^= *state >> 7;
    *state ^= *state << 17;
    *state
}

#[test]
fn sorted_pages_and_v7_append_have_identical_results() {
    for kind in [KeyKind::Uuid, KeyKind::V7] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("objects.idx");
        let index = UuidIndex::create(&path, kind).unwrap();
        assert_eq!(index.get(&uuid(0)).unwrap(), None);
        for batch in rows(0, 10_000).chunks(MAX_BATCH) {
            index.upsert_batch(batch).unwrap();
        }
        for n in 0..10_000 {
            assert_eq!(index.get(&uuid(n)).unwrap(), Some(n * 8));
        }
        assert_eq!(index.get(&uuid(10_000)).unwrap(), None);
        let s = index.stats().unwrap();
        assert_eq!(s.entries, 10_000);
        assert_eq!(s.directory_bytes, s.pages as usize * 24);
        if kind == KeyKind::V7 {
            assert_eq!(s.pages, 10_000u64.div_ceil(PAGE_CAPACITY as u64));
            assert_eq!(s.splits, 0);
        } else {
            assert!(s.splits > 0);
        }
        drop(index);
        let index = UuidIndex::open(path).unwrap();
        for n in 0..10_000 {
            assert_eq!(index.get(&uuid(n)).unwrap(), Some(n * 8));
        }
    }
}

#[test]
fn out_of_order_and_same_millisecond_v7_match_ordered_map() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("objects.idx");
    let index = UuidIndex::create(&path, KeyKind::V7).unwrap();
    let mut rng = 551u64;
    let mut expected = BTreeMap::new();
    for _ in 0..40 {
        let batch: Vec<_> = (0..MAX_BATCH)
            .map(|_| {
                let n = random(&mut rng) % 30_000;
                let address = random(&mut rng);
                expected.insert(uuid(n), address);
                Entry {
                    key: uuid(n),
                    address,
                }
            })
            .collect();
        index.upsert_batch(&batch).unwrap();
    }
    assert_eq!(index.stats().unwrap().entries, expected.len() as u64);
    assert!(index.stats().unwrap().splits > 100);
    drop(index);
    let index = UuidIndex::open(path).unwrap();
    for n in 0..30_001 {
        assert_eq!(
            index.get(&uuid(n)).unwrap(),
            expected.get(&uuid(n)).copied()
        );
    }
}

#[test]
fn generic_uuids_handle_extremes_and_v8_is_validated_without_time_assumptions() {
    for kind in [KeyKind::Uuid, KeyKind::V8] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("objects.idx");
        let index = UuidIndex::create(&path, kind).unwrap();
        let mut keys = [[0; 16], [255; 16], [12; 16]];
        if kind == KeyKind::V8 {
            for k in &mut keys {
                k[6] = 0x80;
                k[8] = 0x80;
            }
        }
        for (n, k) in keys.iter().enumerate() {
            index.upsert(*k, n as u64).unwrap();
        }
        for (n, k) in keys.iter().enumerate() {
            assert_eq!(index.get(k).unwrap(), Some(n as u64));
        }
        drop(index);
        let index = UuidIndex::open(path).unwrap();
        assert_eq!(index.get(&keys[0]).unwrap(), Some(0));
        if kind == KeyKind::V8 {
            assert!(matches!(index.upsert(uuid(1), 9), Err(Error::InvalidUuid)));
        }
    }
}

#[test]
fn invalid_batch_is_rejected_before_any_change_and_updates_keep_count() {
    let dir = tempfile::tempdir().unwrap();
    let index = UuidIndex::create(dir.path().join("objects.idx"), KeyKind::V7).unwrap();
    let mut batch = rows(0, 100);
    batch[99].key[8] = 0;
    assert!(matches!(
        index.upsert_batch(&batch),
        Err(Error::InvalidUuid)
    ));
    assert_eq!(index.stats().unwrap().entries, 0);
    index.upsert_batch(&rows(0, 100)).unwrap();
    index.upsert(uuid(50), u64::MAX).unwrap();
    assert_eq!(index.get(&uuid(50)).unwrap(), Some(u64::MAX));
    assert_eq!(index.stats().unwrap().entries, 100);
    assert!(matches!(
        index.upsert_batch(&rows(0, MAX_BATCH as u64 + 1)),
        Err(Error::BatchTooLarge)
    ));
}

#[test]
fn interrupted_split_recovers_and_failed_instance_cannot_serve() {
    for stop in [1, 2, 3, usize::MAX] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("objects.idx");
        let index = UuidIndex::create(&path, KeyKind::V7).unwrap();
        index.upsert_batch(&rows(0, PAGE_CAPACITY as u64)).unwrap();
        index.fail_after.store(stop, Ordering::Relaxed);
        assert!(
            index
                .upsert_batch(&rows(PAGE_CAPACITY as u64, PAGE_CAPACITY as u64 * 2))
                .is_err()
        );
        assert!(matches!(index.get(&uuid(0)), Err(Error::NeedsReopen)));
        drop(index);
        let index = UuidIndex::open(&path).unwrap();
        for n in 0..PAGE_CAPACITY as u64 * 2 {
            assert_eq!(index.get(&uuid(n)).unwrap(), Some(n * 8));
        }
        assert_eq!(std::fs::metadata(wal_path(&path)).unwrap().len(), 0);
        drop(index);
        assert!(UuidIndex::open(path).is_ok());
    }
}

#[test]
fn incomplete_journal_never_applies_and_complete_corruption_fails_closed() {
    let mut dirty = BTreeMap::new();
    dirty.insert(0, file_header(KeyKind::V7, 1, 1));
    dirty.insert(1, make_page(1, &rows(0, 1)));
    let log = journal_bytes(&dirty);
    for cut in [1, 23, 24, 1000, log.len() - 1] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("objects.idx");
        drop(UuidIndex::create(&path, KeyKind::V7).unwrap());
        std::fs::write(wal_path(&path), &log[..cut]).unwrap();
        let index = UuidIndex::open(&path).unwrap();
        assert_eq!(index.stats().unwrap().entries, 0);
    }
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("objects.idx");
    drop(UuidIndex::create(&path, KeyKind::V7).unwrap());
    let mut bad = log;
    bad[30] ^= 1;
    std::fs::write(wal_path(&path), bad).unwrap();
    assert!(matches!(
        UuidIndex::open(path),
        Err(Error::Corrupt("journal checksum"))
    ));
}

#[test]
fn torn_main_header_is_repaired_from_durable_journal() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("objects.idx");
    let index = UuidIndex::create(&path, KeyKind::V7).unwrap();
    index.fail_after.store(1, Ordering::Relaxed);
    assert!(index.upsert(uuid(3), 55).is_err());
    index.file.write_all_at(&[255; 100], 0).unwrap();
    drop(index);
    let index = UuidIndex::open(path).unwrap();
    assert_eq!(index.get(&uuid(3)).unwrap(), Some(55));
}

#[test]
fn corrupt_page_rejected_and_second_open_excluded() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("objects.idx");
    let index = UuidIndex::create(&path, KeyKind::V7).unwrap();
    index.upsert(uuid(2), 10).unwrap();
    assert!(UuidIndex::open(&path).is_err());
    index
        .file
        .write_all_at(&[200], PAGE_BYTES as u64 + 100)
        .unwrap();
    assert!(matches!(index.get(&uuid(2)), Err(Error::Corrupt(_))));
    drop(index);
    assert!(matches!(UuidIndex::open(path), Err(Error::Corrupt(_))));
}

#[test]
fn readers_and_writer_share_index_without_partial_pages() {
    fn send_sync<T: Send + Sync>() {}
    send_sync::<UuidIndex>();
    let dir = tempfile::tempdir().unwrap();
    let index = Arc::new(UuidIndex::create(dir.path().join("objects.idx"), KeyKind::V7).unwrap());
    index.upsert_batch(&rows(0, 1000)).unwrap();
    let done = Arc::new(AtomicBool::new(false));
    std::thread::scope(|scope| {
        for _ in 0..4 {
            let index = Arc::clone(&index);
            let done = Arc::clone(&done);
            scope.spawn(move || {
                let mut n = 0;
                while !done.load(Ordering::Acquire) {
                    assert_eq!(index.get(&uuid(n % 1000)).unwrap(), Some((n % 1000) * 8));
                    n += 1;
                }
            });
        }
        for batch in rows(1000, 10_000).chunks(MAX_BATCH) {
            index.upsert_batch(batch).unwrap();
        }
        done.store(true, Ordering::Release);
    });
}

// Invoked as an isolated test process below. exit() deliberately bypasses Rust
// destructors after the fault point; this still is not a power-loss simulation.
#[test]
fn interruption_child() {
    let Ok(path) = std::env::var("TRUNK_UUID_FAULT_PATH") else {
        return;
    };
    let point = std::env::var("TRUNK_UUID_FAULT_POINT")
        .unwrap()
        .parse()
        .unwrap();
    let index = UuidIndex::open(path).unwrap();
    index.fail_after.store(point, Ordering::Relaxed);
    assert!(
        index
            .upsert_batch(&rows(PAGE_CAPACITY as u64, PAGE_CAPACITY as u64 * 2))
            .is_err()
    );
    std::process::exit(73);
}

#[test]
fn process_exit_during_publication_recovers_all_addresses() {
    for point in [1, 2, usize::MAX] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("objects.idx");
        let index = UuidIndex::create(&path, KeyKind::V7).unwrap();
        index.upsert_batch(&rows(0, PAGE_CAPACITY as u64)).unwrap();
        drop(index);
        let status = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "tests::interruption_child"])
            .env("TRUNK_UUID_FAULT_PATH", &path)
            .env("TRUNK_UUID_FAULT_POINT", point.to_string())
            .stdout(std::process::Stdio::null())
            .status()
            .unwrap();
        assert_eq!(status.code(), Some(73));
        let index = UuidIndex::open(path).unwrap();
        for n in 0..PAGE_CAPACITY as u64 * 2 {
            assert_eq!(index.get(&uuid(n)).unwrap(), Some(n * 8));
        }
    }
}
