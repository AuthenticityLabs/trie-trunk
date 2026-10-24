use super::*;
use std::sync::{
    Barrier,
    atomic::{AtomicBool, Ordering},
};

fn key(n: u64) -> Uuid {
    let mut k = [0; 16];
    k[8..].copy_from_slice(&n.to_be_bytes());
    k
}
fn put(n: u64, revision: u16) -> Command {
    Command::put(key(n), revision, (n + 1) * 100000 + revision as u64 + 1)
}
fn sealed(mut p: Page) -> Page {
    p.seal();
    p.validate().unwrap();
    p
}

#[test]
fn exact_layout_and_reserved_bytes() {
    assert_eq!(page::HEADER + page::UNITS + page::UNITS * 32, 4096);
    let mut p = Page::default();
    for i in 0..123 {
        p.apply(&put(i, 0), None).unwrap();
    }
    assert_eq!(p.objects(), 123);
    assert_eq!(p.used(), 123);
    assert!(matches!(p.apply(&put(124, 0), None), Err(Error::PageFull)));
    let p = sealed(p);
    assert_eq!(&p.0[32..37], &[0; 5]);
    for i in 0..123 {
        let at = (p.pointer(i) & 127) as usize * 32;
        assert_eq!(&p.0[at + 20..at + 24], &[0; 4]);
    }
}

#[test]
fn search_every_key_and_gap_with_adversarial_extensions() {
    for large in [1, 5, 9, 61, 241, 481] {
        let mut p = Page::default();
        p.apply(&put(10, 0), None).unwrap();
        p.apply(&put(30, 0), None).unwrap();
        if large < 481 {
            p.apply(&put(50, 0), None).unwrap();
        }
        for r in 1..large {
            p.apply(&put(30, r), None).unwrap();
        }
        let p = sealed(p);
        for n in 0..65 {
            let got = p.find(&key(n), &mut Stats::default());
            assert_eq!(
                got.is_some(),
                n == 10 || n == 30 || (n == 50 && large < 481),
                "n={n} large={large}"
            );
        }
    }
}

#[test]
fn randomized_page_against_reference() {
    let mut rng = 7u64;
    for _ in 0..100 {
        let mut p = Page::default();
        let mut reference = BTreeMap::new();
        for _ in 0..300 {
            rng ^= rng << 13;
            rng ^= rng >> 7;
            rng ^= rng << 17;
            let n = rng % 40;
            let rev = ((rng >> 16) % 30) as u16;
            let result = p.apply(&put(n, rev), None);
            if result.is_ok() {
                reference.insert((n, rev), put(n, rev).address);
            }
        }
        let p = sealed(p);
        for n in 0..45 {
            let at = p.find(&key(n), &mut Stats::default());
            assert_eq!(at.is_some(), reference.keys().any(|&(k, _)| k == n));
            if let Some(i) = at {
                for rev in 0..30 {
                    assert_eq!(p.reference(i, rev), reference.get(&(n, rev)).copied());
                }
            }
        }
    }
}

#[test]
fn scatter_growth_does_not_move_existing_units() {
    let mut p = Page::default();
    for n in 0..100 {
        p.apply(&put(n, 0), None).unwrap();
    }
    let before: Vec<_> = (0..100).map(|i| p.pointer(i)).collect();
    p.apply(&put(0, 1), None).unwrap();
    assert_eq!(p.pointer(0), before[0]);
    assert_ne!(p.pointer(1) & 127, (before[0] & 127) + 1);
    for n in 1..100 {
        let at = p.find(&key(n), &mut Stats::default()).unwrap();
        assert_eq!(p.pointer(at), before[n as usize]);
    }
    sealed(p);
}

#[test]
fn full_page_object_retention_gaps_and_duplicates() {
    let mut p = Page::default();
    p.apply(&put(1, 0), None).unwrap();
    p.apply(&put(1, 488), None).unwrap();
    assert_eq!(p.used(), 123);
    assert_eq!(p.reference(0, 200), None);
    assert_eq!(p.latest(0), Some((488, put(1, 488).address)));
    assert!(matches!(
        p.apply(&put(1, 489), None),
        Err(Error::ObjectFull)
    ));
    assert!(p.apply(&put(1, 488), None).is_ok());
    assert!(matches!(
        p.apply(&Command::put(key(1), 488, 123), None),
        Err(Error::DuplicateRevision)
    ));
    p.apply(&Command::retire_before(key(1), 480), None).unwrap();
    assert_eq!(p.used(), 3);
    assert_eq!(p.base(0), 480);
    assert_eq!(p.reference(0, 488), Some(put(1, 488).address));
    p.apply(&Command::retire_before(key(1), 489), None).unwrap();
    assert_eq!(p.used(), 0);
    sealed(p);
}

#[test]
fn ring_wraps_without_revision_wrap_or_missing_values() {
    for keep in [1, 2, 5, 10, 255, 489] {
        let mut p = Page::default();
        for r in 0..1200u16 {
            p.apply(&put(7, r), Some(keep)).unwrap();
            assert_eq!(p.latest(0), Some((r, put(7, r).address)));
            let first = r.saturating_sub(keep - 1);
            assert_eq!(p.base(0), first);
            if first > 0 {
                assert_eq!(p.reference(0, first - 1), None);
            }
            for revision in first..=r {
                assert_eq!(p.reference(0, revision), Some(put(7, revision).address));
            }
        }
        sealed(p);
    }
    let mut p = Page::default();
    p.apply(&put(1, 65534), None).unwrap();
    p.apply(&put(1, 65535), None).unwrap();
    sealed(p);
}

#[test]
fn reject_corrupt_pointers_and_checksum() {
    let mut p = Page::default();
    p.apply(&put(1, 0), None).unwrap();
    p.seal();
    p.0[1000] ^= 1;
    assert!(p.validate().is_err());
    p.0[1000] ^= 1;
    p.0[37] = 0;
    p.seal();
    assert!(p.validate().is_err());
    p.0[37] = 128 | 5;
    p.seal();
    assert!(p.validate().is_err());
}

#[test]
fn durable_split_snapshot_and_reopen() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("collection.idx");
    let index = Index::create(&path, KeyKind::Uuid, None).unwrap();
    index
        .apply(&(0..123).map(|n| put(n, 0)).collect::<Vec<_>>())
        .unwrap();
    let old = index.snapshot().unwrap();
    assert_eq!(old.pages(), 1);
    index
        .apply(&(0..123).map(|n| put(n, 20)).collect::<Vec<_>>())
        .unwrap();
    assert!(index.snapshot().unwrap().pages() > 1);
    for n in 0..123 {
        assert_eq!(old.latest(&key(n)).unwrap(), Some((0, put(n, 0).address)));
        assert_eq!(
            index.latest(&key(n)).unwrap(),
            Some((20, put(n, 20).address))
        );
    }
    drop(old);
    drop(index);
    let reopened = Index::open(&path).unwrap();
    for n in 0..123 {
        assert_eq!(reopened.get(&key(n), 20).unwrap(), Some(put(n, 20).address));
    }
}

#[test]
fn page_reuse_never_overwrites_pinned_reader() {
    let dir = tempfile::tempdir().unwrap();
    let index = Index::create(dir.path().join("i"), KeyKind::Uuid, Some(5)).unwrap();
    index.apply(&[put(1, 0)]).unwrap();
    let old = index.snapshot().unwrap();
    for r in 1..30 {
        index.apply(&[put(1, r)]).unwrap();
        assert_eq!(old.get(&key(1), 0).unwrap(), Some(put(1, 0).address));
    }
    assert!(index.stats().reused_pages > 0);
    drop(old);
    for r in 30..40 {
        index.apply(&[put(1, r)]).unwrap();
    }
    assert!(index.file_bytes().unwrap() < 20 * 4096);
}

#[test]
fn readers_continue_during_seals() {
    let dir = tempfile::tempdir().unwrap();
    let index = Arc::new(Index::create(dir.path().join("i"), KeyKind::Uuid, Some(20)).unwrap());
    index
        .apply(&(0..150).map(|n| put(n, 0)).collect::<Vec<_>>())
        .unwrap();
    let start = Arc::new(Barrier::new(2));
    let done = Arc::new(AtomicBool::new(false));
    let handle = {
        let index = index.clone();
        let start = start.clone();
        let done = done.clone();
        std::thread::spawn(move || {
            start.wait();
            let mut count = 0;
            while !done.load(Ordering::Relaxed) {
                let s = index.snapshot().unwrap();
                let revision = s.latest(&key(0)).unwrap().unwrap().0;
                for n in 0..150 {
                    assert_eq!(s.latest(&key(n)).unwrap().unwrap().0, revision);
                }
                count += 1;
            }
            count
        })
    };
    start.wait();
    for r in 1..20 {
        index
            .apply(&(0..150).map(|n| put(n, r)).collect::<Vec<_>>())
            .unwrap();
    }
    done.store(true, Ordering::Relaxed);
    assert!(handle.join().unwrap() > 0);
}

#[test]
fn discard_uncommitted_and_replay_seed_at_every_boundary() {
    for fault in 1..=6 {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("i");
        let index = Index::create(&path, KeyKind::Uuid, None).unwrap();
        index
            .apply(&(0..120).map(|n| put(n, 0)).collect::<Vec<_>>())
            .unwrap();
        index.inject_fault(fault);
        assert!(matches!(
            index.apply(&(0..140).map(|n| put(n, 4)).collect::<Vec<_>>()),
            Err(Error::Injected(_))
        ));
        drop(index);
        let reopened = Index::open(&path).unwrap();
        for n in 0..140 {
            assert_eq!(
                reopened.get(&key(n), 4).unwrap(),
                Some(put(n, 4).address),
                "fault {fault}"
            );
        }
        assert_eq!(reopened.snapshot().unwrap().checkpoint(), 260);
        if fault <= 4 {
            assert_eq!(reopened.stats().recovery_replayed, 140);
        } else {
            assert_eq!(reopened.stats().recovery_replayed, 0);
        }
        reopened.apply(&[put(1, 5)]).unwrap();
        drop(reopened);
        assert_eq!(
            Index::open(&path).unwrap().get(&key(1), 5).unwrap(),
            Some(put(1, 5).address)
        );
    }
}

#[test]
fn invalid_batch_not_written_to_seed() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("i");
    let index = Index::create(&path, KeyKind::Uuid, None).unwrap();
    index.apply(&[put(1, 0)]).unwrap();
    assert!(
        index
            .apply(&[put(2, 0), Command::put(key(1), 0, 9)])
            .is_err()
    );
    assert_eq!(index.seed.metadata().unwrap().len(), 32);
    assert_eq!(index.latest(&key(2)).unwrap(), None);
    index.apply(&[put(3, 0)]).unwrap();
    drop(index);
    let index = Index::open(&path).unwrap();
    assert_eq!(index.latest(&key(2)).unwrap(), None);
}

#[test]
fn committed_corruption_fails_closed() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("i");
    let index = Index::create(&path, KeyKind::Uuid, None).unwrap();
    index.apply(&[put(1, 0)]).unwrap();
    let page = index.snapshot().unwrap().directory.fences[0].page;
    index
        .file
        .write_all_at(&[0xff], page as u64 * 4096 + 200)
        .unwrap();
    drop(index);
    assert!(matches!(Index::open(&path), Err(Error::Corrupt(_))));
}

#[test]
fn older_snapshot_pins_pages_shared_across_generations() {
    let dir = tempfile::tempdir().unwrap();
    let index = Index::create(dir.path().join("objects.idx"), KeyKind::Uuid, None).unwrap();
    index
        .apply(&(0..400).map(|n| put(n, 0)).collect::<Vec<_>>())
        .unwrap();
    let oldest = index.snapshot().unwrap();
    // The low page remains shared by multiple views before it is replaced.
    for r in 1..5 {
        index.apply(&[put(399, r)]).unwrap();
    }
    index.apply(&[put(0, 1)]).unwrap();
    for r in 5..30 {
        index.apply(&[put(399, r)]).unwrap();
    }
    for n in 0..400 {
        assert_eq!(
            oldest.latest(&key(n)).unwrap(),
            Some((0, put(n, 0).address))
        );
    }
}

#[test]
fn checked_v7_append_and_late_inserts() {
    fn v7(n: u64) -> Uuid {
        let mut k = key(n);
        k[6] = 112;
        k[8] = 128;
        k
    }
    let dir = tempfile::tempdir().unwrap();
    let index = Index::create(dir.path().join("objects.idx"), KeyKind::V7, None).unwrap();
    for batch in (0..1000)
        .map(|n| Command::put(v7(n * 2), 0, n + 1))
        .collect::<Vec<_>>()
        .chunks(100)
    {
        index.apply(batch).unwrap();
    }
    assert_eq!(index.snapshot().unwrap().pages(), 1000usize.div_ceil(123));
    for batch in (0..1000)
        .rev()
        .map(|n| Command::put(v7(n * 2 + 1), 0, n + 2000))
        .collect::<Vec<_>>()
        .chunks(100)
    {
        index.apply(batch).unwrap();
    }
    for n in 0..1000 {
        assert_eq!(index.get(&v7(n * 2), 0).unwrap(), Some(n + 1));
        assert_eq!(index.get(&v7(n * 2 + 1), 0).unwrap(), Some(n + 2000));
    }
}

#[test]
fn abrupt_exit_child() {
    let Ok(path) = std::env::var("SLOTTED_CRASH_PATH") else {
        return;
    };
    let point: u8 = std::env::var("SLOTTED_CRASH_POINT")
        .unwrap()
        .parse()
        .unwrap();
    let index = Index::open(path).unwrap();
    index.inject_fault(point);
    let result = index.apply(&(150..350).map(|n| put(n, 0)).collect::<Vec<_>>());
    assert!(matches!(result, Err(Error::Injected(_))));
    // No destructors; reopen must not rely on graceful close or repair.
    std::process::exit(47);
}

#[test]
fn process_exit_replays_durable_input_without_repair() {
    for point in 1..=6 {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("objects.idx");
        let index = Index::create(&path, KeyKind::Uuid, None).unwrap();
        index
            .apply(&(0..150).map(|n| put(n, 0)).collect::<Vec<_>>())
            .unwrap();
        drop(index);
        let status = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "slotted::tests::abrupt_exit_child"])
            .env("SLOTTED_CRASH_PATH", &path)
            .env("SLOTTED_CRASH_POINT", point.to_string())
            .stdout(std::process::Stdio::null())
            .status()
            .unwrap();
        assert_eq!(status.code(), Some(47));
        let index = Index::open(&path).unwrap();
        for n in 0..350 {
            assert_eq!(index.get(&key(n), 0).unwrap(), Some(put(n, 0).address));
        }
    }
}

#[test]
fn durable_random_histories_and_retention_match_oracle() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("objects.idx");
    let mut index = Index::create(&path, KeyKind::Uuid, Some(9)).unwrap();
    let mut expected = BTreeMap::<Uuid, BTreeMap<u16, u64>>::new();
    let mut rng = 17u64;
    for round in 0..40 {
        let mut batch = vec![];
        for _ in 0..100 {
            rng ^= rng << 13;
            rng ^= rng >> 7;
            rng ^= rng << 17;
            let k = key(rng % 500);
            let h = expected.entry(k).or_default();
            let rev = h
                .last_key_value()
                .map(|(r, _)| *r + 1 + (rng % 3) as u16)
                .unwrap_or(0);
            let address = u64::MAX - rng % 100_000;
            h.insert(rev, address);
            h.retain(|r, _| *r >= rev.saturating_sub(8));
            batch.push(Command::put(k, rev, address));
        }
        index.apply(&batch).unwrap();
        if round % 5 == 0 {
            drop(index);
            index = Index::open(&path).unwrap();
        }
        let snapshot = index.snapshot().unwrap();
        for (k, history) in &expected {
            let mut got = BTreeMap::new();
            snapshot
                .history(k, |r, a| {
                    got.insert(r, a);
                })
                .unwrap();
            assert_eq!(&got, history);
        }
    }
}

#[test]
fn existing_unrotated_revision_slot_changes_only_its_eight_bytes() {
    let mut p = Page::default();
    p.apply(&put(1, 0), None).unwrap();
    p.apply(&put(1, 1), None).unwrap();
    let before = p.0;
    let at = (p.pointer(1) & 127) as usize * 32 + 8;
    p.apply(&put(1, 2), None).unwrap();
    assert_eq!(&p.0[..at], &before[..at]);
    assert_eq!(&p.0[at + 8..], &before[at + 8..]);
    assert_eq!(p.reference(0, 2), Some(put(1, 2).address));
    sealed(p);
}
