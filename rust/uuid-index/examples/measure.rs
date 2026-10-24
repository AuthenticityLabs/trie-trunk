//! Warm-OS-cache directory benchmark. Not an end-to-end replay benchmark.
use std::fs::File;
use std::hint::black_box;
use std::io::Write;
use std::time::Instant;
use trunk_uuid_index::{Entry, KeyKind, MAX_BATCH, UuidIndex};

fn next(s: &mut u64) -> u64 {
    *s ^= *s << 13;
    *s ^= *s >> 7;
    *s ^= *s << 17;
    *s
}
fn data(n: usize, random: bool, late: bool) -> Vec<Entry> {
    let mut rng = 1234567;
    let mut rows: Vec<_> = (0..n)
        .map(|i| {
            let mut key = [0; 16];
            if random {
                key[..8].copy_from_slice(&next(&mut rng).to_be_bytes());
                key[8..].copy_from_slice(&next(&mut rng).to_be_bytes());
                key[6] = (key[6] & 15) | 0x40;
                key[8] = (key[8] & 63) | 0x80;
            } else {
                key[..6]
                    .copy_from_slice(&(1_700_000_000_000u64 + i as u64 / 10).to_be_bytes()[2..]);
                key[6] = 0x70;
                key[8] = 0x80;
                key[9..].copy_from_slice(&(i as u64).to_be_bytes()[1..]);
            }
            Entry {
                key,
                address: i as u64 * 8,
            }
        })
        .collect();
    if late {
        // Most arrivals are near their creation time; 5% of swaps cross the full
        // collection. No assumed guarantee that a v7 key arrives in order.
        for chunk in rows.chunks_mut(1000) {
            for i in (1..chunk.len()).rev() {
                chunk.swap(i, next(&mut rng) as usize % (i + 1));
            }
        }
        for _ in 0..n / 20 {
            let a = next(&mut rng) as usize % n;
            let b = next(&mut rng) as usize % n;
            rows.swap(a, b);
        }
    }
    rows
}
fn mmap_get(map: &[u8], key: &[u8; 16]) -> Option<u64> {
    let (mut lo, mut hi) = (0, map.len() / 24);
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        let row = &map[mid * 24..mid * 24 + 24];
        match row[..16].cmp(key) {
            std::cmp::Ordering::Less => lo = mid + 1,
            std::cmp::Ordering::Greater => hi = mid,
            std::cmp::Ordering::Equal => {
                return Some(u64::from_le_bytes(row[16..24].try_into().unwrap()));
            }
        }
    }
    None
}
fn lookups(rows: &[Entry], get: impl Fn(&[u8; 16]) -> Option<u64>) -> (f64, u128, u128) {
    let mut rng = 89101;
    let probes: Vec<_> = (0..20_000)
        .map(|_| rows[next(&mut rng) as usize % rows.len()])
        .collect();
    for e in &probes {
        assert_eq!(get(&e.key), Some(e.address));
    }
    let mut samples = Vec::new();
    for _ in 0..5 {
        let now = Instant::now();
        for e in &probes {
            black_box(get(black_box(&e.key)));
        }
        samples.push(now.elapsed().as_nanos() as f64 / probes.len() as f64);
    }
    samples.sort_by(f64::total_cmp);
    let mut timings = Vec::new();
    for e in &probes[..4096] {
        let now = Instant::now();
        black_box(get(black_box(&e.key)));
        timings.push(now.elapsed().as_nanos());
    }
    timings.sort_unstable();
    (samples[2], timings[2048], timings[3891])
}
fn run(n: usize, label: &str, kind: KeyKind, random: bool, late: bool) {
    let rows = data(n, random, late);
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("directory.idx");
    let index = UuidIndex::create(&path, kind).unwrap();
    let mut batches = Vec::new();
    let now = Instant::now();
    for batch in rows.chunks(MAX_BATCH) {
        let begin = Instant::now();
        index.upsert_batch(batch).unwrap();
        batches.push(begin.elapsed().as_micros());
    }
    let insert_ms = now.elapsed().as_secs_f64() * 1000.0;
    batches.sort_unstable();
    let s = index.stats().unwrap();
    let (lookup_ns, p50, p95) = lookups(&rows, |k| index.get(k).unwrap());
    let updates: Vec<_> = rows
        .iter()
        .step_by((n / MAX_BATCH).max(1))
        .take(MAX_BATCH)
        .map(|e| Entry {
            key: e.key,
            address: e.address + 1,
        })
        .collect();
    let now = Instant::now();
    index.upsert_batch(&updates).unwrap();
    let update_us = now.elapsed().as_micros();
    drop(index);
    let now = Instant::now();
    let index = UuidIndex::open(&path).unwrap();
    let open_ms = now.elapsed().as_secs_f64() * 1000.0;
    for e in &updates {
        assert_eq!(index.get(&e.key).unwrap(), Some(e.address));
    }
    drop(index);

    let mut sorted = rows.clone();
    sorted.sort_unstable_by_key(|e| e.key);
    assert!(sorted.windows(2).all(|p| p[0].key < p[1].key));
    let baseline = dir.path().join("immutable.idx");
    let mut f = std::io::BufWriter::new(File::create(&baseline).unwrap());
    for e in &sorted {
        f.write_all(&e.key).unwrap();
        f.write_all(&e.address.to_le_bytes()).unwrap();
    }
    f.flush().unwrap();
    drop(f);
    let f = File::open(baseline).unwrap();
    // This separate benchmark fixture is immutable for the map's entire life.
    let map = unsafe { memmap2::Mmap::map(&f).unwrap() };
    let (flat_ns, _, _) = lookups(&rows, |k| mmap_get(&map, k));
    println!(
        "{label},{n},{insert_ms:.3},{},{},{lookup_ns:.2},{p50},{p95},{open_ms:.3},{update_us},{},{},{},{},{},{},{flat_ns:.2}",
        batches[batches.len() / 2],
        batches[batches.len() * 95 / 100],
        s.pages,
        s.file_bytes,
        s.directory_bytes,
        s.directory_capacity_bytes,
        s.splits,
        s.write_bytes
    );
}
fn main() {
    let n = std::env::args()
        .nth(1)
        .map(|s| s.parse().unwrap())
        .unwrap_or(100_000);
    assert!(n > 0);
    println!(
        "workload,objects,insert_ms,batch_p50_us,batch_p95_us,get_ns,get_p50_ns,get_p95_ns,reopen_ms,update_1024_us,pages,file_bytes,directory_bytes,directory_capacity_bytes,splits,application_write_bytes,immutable_mmap_get_ns"
    );
    run(n, "v7_ordered_generic", KeyKind::Uuid, false, false);
    run(n, "v7_ordered_append", KeyKind::V7, false, false);
    run(n, "v7_late", KeyKind::V7, false, true);
    run(n, "uuid_random", KeyKind::Uuid, true, false);
}
