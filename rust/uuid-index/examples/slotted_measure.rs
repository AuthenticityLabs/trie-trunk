//! Warm filesystem-cache measurements, including durable batched writes.
use std::{hint::black_box, time::Instant};
use trunk_uuid_index::{
    Entry, KeyKind, UuidIndex,
    slotted::{Command, Index, Stats, page::Page},
};
fn next(s: &mut u64) -> u64 {
    *s ^= *s << 13;
    *s ^= *s >> 7;
    *s ^= *s << 17;
    *s
}
fn keys(n: usize, random: bool) -> Vec<[u8; 16]> {
    let mut s = 999;
    (0..n)
        .map(|i| {
            let mut k = [0; 16];
            if random {
                k[..8].copy_from_slice(&next(&mut s).to_be_bytes());
                k[8..].copy_from_slice(&next(&mut s).to_be_bytes());
                k[6] = (k[6] & 15) | 64;
            } else {
                k[..6].copy_from_slice(&(1_700_000_000_000u64 + i as u64 / 10).to_be_bytes()[2..]);
                k[6] = 112;
                k[9..].copy_from_slice(&(i as u64).to_be_bytes()[1..]);
            }
            k[8] = (k[8] & 63) | 128;
            k
        })
        .collect()
}
fn time_get(keys: &[[u8; 16]], get: impl Fn(&[u8; 16]) -> u64) -> (f64, u128, u128) {
    let mut r = 35;
    let probes: Vec<_> = (0..20_000)
        .map(|_| &keys[next(&mut r) as usize % keys.len()])
        .collect();
    for k in &probes {
        black_box(get(k));
    }
    let mut samples = vec![];
    for _ in 0..5 {
        let t = Instant::now();
        for k in &probes {
            black_box(get(black_box(k)));
        }
        samples.push(t.elapsed().as_nanos() as f64 / probes.len() as f64);
    }
    samples.sort_by(f64::total_cmp);
    let mut latency = vec![];
    for k in &probes[..4096] {
        let t = Instant::now();
        black_box(get(k));
        latency.push(t.elapsed().as_nanos());
    }
    latency.sort_unstable();
    (samples[2], latency[2048], latency[3891])
}
fn report(index: &Index, keys: &[[u8; 16]], label: &str, n: usize, revisions: usize, ms: f64) {
    let snap = index.snapshot().unwrap();
    let (ns, p50, p95) = time_get(keys, |k| snap.latest(k).unwrap().unwrap().1);
    let mut s = Stats::default();
    for k in keys.iter().step_by((n / 10000).max(1)) {
        snap.latest_measured(k, &mut s).unwrap();
    }
    let count = keys.iter().step_by((n / 10000).max(1)).count() as f64;
    let st = index.stats();
    println!(
        "{label},{n},{revisions},{ms:.3},{ns:.2},{p50},{p95},{},{},{},{},{},{:.2},{:.2},{}",
        snap.pages(),
        index.file_bytes().unwrap(),
        snap.directory_bytes(),
        st.written_bytes,
        st.splits,
        s.pointer_probes as f64 / count,
        s.uuid_comparisons as f64 / count,
        st.peak_dirty_pages
    );
}
fn run(n: usize, random: bool) {
    let keys = keys(n, random);
    let kind = if random { KeyKind::Uuid } else { KeyKind::V7 };
    let label = if random { "random" } else { "v7" };
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("slotted.idx");
    let index = Index::create(&path, kind, None).unwrap();
    let commands: Vec<_> = keys
        .iter()
        .enumerate()
        .map(|(i, k)| Command::put(*k, 0, i as u64 + 1))
        .collect();
    let t = Instant::now();
    for batch in commands.chunks(1024) {
        index.apply(batch).unwrap();
    }
    report(
        &index,
        &keys,
        &format!("slotted_{label}_create"),
        n,
        n,
        t.elapsed().as_secs_f64() * 1000.,
    );
    let mut updates = vec![];
    for (i, k) in keys.iter().enumerate() {
        let max = if i % 20 == 0 {
            8
        } else if i % 5 == 0 {
            1
        } else {
            0
        };
        for r in 1..=max {
            updates.push(Command::put(*k, r, (i as u64 + 1) * 100 + r as u64));
        }
    }
    let t = Instant::now();
    for b in updates.chunks(1024) {
        index.apply(b).unwrap();
    }
    report(
        &index,
        &keys,
        &format!("slotted_{label}_mixed"),
        n,
        n + updates.len(),
        t.elapsed().as_secs_f64() * 1000.,
    );
    // Verify every object, not just timed probes, after opening the durable root.
    drop(index);
    let t = Instant::now();
    let index = Index::open(&path).unwrap();
    let open = t.elapsed().as_secs_f64() * 1000.;
    let snap = index.snapshot().unwrap();
    for (i, k) in keys.iter().enumerate() {
        let r = if i % 20 == 0 {
            8
        } else if i % 5 == 0 {
            1
        } else {
            0
        };
        let a = if r == 0 {
            i as u64 + 1
        } else {
            (i as u64 + 1) * 100 + r
        };
        assert_eq!(snap.latest(k).unwrap(), Some((r as u16, a)));
    }
    eprintln!(
        "{label} objects={n} reopen_ms={open:.3} seed_bytes={}",
        std::fs::metadata(path.with_extension("idx.seed"))
            .unwrap()
            .len()
    );
    let baseline = UuidIndex::create(dir.path().join("baseline.idx"), kind).unwrap();
    let rows: Vec<_> = commands
        .iter()
        .map(|c| Entry {
            key: c.object,
            address: c.address,
        })
        .collect();
    let t = Instant::now();
    for b in rows.chunks(1024) {
        baseline.upsert_batch(b).unwrap();
    }
    let ms = t.elapsed().as_secs_f64() * 1000.;
    let (ns, p50, p95) = time_get(&keys, |k| baseline.get(k).unwrap().unwrap());
    let st = baseline.stats().unwrap();
    println!(
        "baseline_{label}_create,{n},{n},{ms:.3},{ns:.2},{p50},{p95},{},{},{},{},{},0,0,0",
        st.pages, st.file_bytes, st.directory_capacity_bytes, st.write_bytes, st.splits
    );
}
fn pointer_cost() {
    for revisions in [1, 5, 61, 241, 481] {
        let mut p = Page::default();
        let mut key = [0; 16];
        key[15] = 2;
        p.apply(&Command::put(key, 0, 1), None).unwrap();
        for r in 1..revisions {
            p.apply(&Command::put(key, r, r as u64 + 1), None).unwrap();
        }
        for n in [1, 3] {
            let mut k = [0; 16];
            k[15] = n;
            p.apply(&Command::put(k, 0, 999), None).unwrap();
        }
        let starts: Vec<_> = (0..p.used()).filter(|&i| p.start(i)).collect();
        for method in [0, 1] {
            let t = Instant::now();
            let mut probes = 0;
            for i in 0..200_000 {
                let mut k = [0; 16];
                k[15] = (i % 5) as u8;
                let found = if method == 0 {
                    let mut s = Stats::default();
                    let v = p.find(black_box(&k), &mut s);
                    probes += s.pointer_probes;
                    v
                } else {
                    starts
                        .binary_search_by(|&at| p.key(at).cmp(black_box(&k)))
                        .ok()
                        .map(|i| starts[i])
                };
                black_box(found);
            }
            eprintln!(
                "pointer_scan revisions={revisions} method={} ns={:.2} probes={:.2}",
                if method == 0 {
                    "mixed"
                } else {
                    "derived_starts"
                },
                t.elapsed().as_nanos() as f64 / 200000.,
                probes as f64 / 200000.
            );
        }
    }
}
fn main() {
    let n = std::env::args()
        .nth(1)
        .map(|s| s.parse().unwrap())
        .unwrap_or(100_000);
    println!(
        "case,objects,revisions,write_ms,lookup_ns,p50_ns,p95_ns,live_pages,index_file_bytes,current_directory_capacity_bytes,cumulative_write_bytes,splits,pointer_probes,uuid_compares,peak_dirty_pages"
    );
    run(n, false);
    run(n, true);
    pointer_cost();
}
