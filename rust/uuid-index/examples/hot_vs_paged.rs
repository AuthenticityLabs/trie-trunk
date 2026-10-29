//! Resident lookup and volatile updates versus the historical paged index.
//! Hash timings exclude payload bytes, durable input, serialization, and fanout.
use std::{
    alloc::{GlobalAlloc, Layout, System},
    collections::HashMap,
    hint::black_box,
    sync::{
        RwLock,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    time::Instant,
};
use trunk_uuid_index::{
    KeyKind,
    slotted::{Command, Index},
};
type Uuid = [u8; 16];
type Histories = HashMap<Uuid, Vec<u64>>;
static TRACK: AtomicBool = AtomicBool::new(false);
static BYTES: AtomicU64 = AtomicU64::new(0);
static ALLOCS: AtomicU64 = AtomicU64::new(0);
struct CountAlloc;
unsafe impl GlobalAlloc for CountAlloc {
    unsafe fn alloc(&self, l: Layout) -> *mut u8 {
        if TRACK.load(Ordering::Relaxed) {
            BYTES.fetch_add(l.size() as u64, Ordering::Relaxed);
            ALLOCS.fetch_add(1, Ordering::Relaxed);
        }
        unsafe { System.alloc(l) }
    }
    unsafe fn dealloc(&self, p: *mut u8, l: Layout) {
        unsafe { System.dealloc(p, l) }
    }
    unsafe fn realloc(&self, p: *mut u8, l: Layout, n: usize) -> *mut u8 {
        if TRACK.load(Ordering::Relaxed) {
            BYTES.fetch_add(n as u64, Ordering::Relaxed);
            ALLOCS.fetch_add(1, Ordering::Relaxed);
        }
        unsafe { System.realloc(p, l, n) }
    }
}
#[global_allocator]
static ALLOCATOR: CountAlloc = CountAlloc;
fn next(s: &mut u64) -> u64 {
    *s ^= *s << 13;
    *s ^= *s >> 7;
    *s ^= *s << 17;
    *s
}
fn keys(n: usize) -> Vec<Uuid> {
    let mut seed = 71;
    (0..n)
        .map(|_| {
            let mut k = [0; 16];
            k[..8].copy_from_slice(&next(&mut seed).to_be_bytes());
            k[8..].copy_from_slice(&next(&mut seed).to_be_bytes());
            k[6] = (k[6] & 15) | 64;
            k[8] = (k[8] & 63) | 128;
            k
        })
        .collect()
}
fn addr(n: usize, r: usize) -> u64 {
    ((n as u64 + 1) << 16) | (r as u64 + 1)
}
fn time(probes: &[usize], get: impl Fn(usize) -> u64) -> f64 {
    let mut samples = vec![];
    for run in 0..6 {
        let t = Instant::now();
        for &n in probes {
            black_box(get(black_box(n)));
        }
        if run > 0 {
            samples.push(t.elapsed().as_nanos() as f64 / probes.len() as f64);
        }
    }
    samples.sort_by(f64::total_cmp);
    samples[2]
}
fn build(keys: &[Uuid], revisions: usize, spare: usize) -> Histories {
    let mut map = HashMap::with_capacity(keys.len());
    for (i, k) in keys.iter().enumerate() {
        let mut refs = Vec::with_capacity(revisions + spare);
        for r in 0..revisions {
            refs.push(addr(i, r));
        }
        map.insert(*k, refs);
    }
    map
}
fn run(n: usize, revisions: usize) {
    let keys = keys(n);
    let dir = tempfile::tempdir().unwrap();
    let index = Index::create(dir.path().join("objects.idx"), KeyKind::Uuid, None).unwrap();
    let t = Instant::now();
    let mut batch = Vec::with_capacity(1024);
    for (i, k) in keys.iter().enumerate() {
        for r in 0..revisions {
            batch.push(Command::put(*k, r as u16, addr(i, r)));
            if batch.len() == 1024 {
                index.apply(&batch).unwrap();
                batch.clear();
            }
        }
    }
    index.apply(&batch).unwrap();
    let durable_ms = t.elapsed().as_secs_f64() * 1000.;
    BYTES.store(0, Ordering::Relaxed);
    ALLOCS.store(0, Ordering::Relaxed);
    TRACK.store(true, Ordering::Relaxed);
    let map = build(&keys, revisions, 1);
    TRACK.store(false, Ordering::Relaxed);
    let allocated = BYTES.load(Ordering::Relaxed);
    let allocs = ALLOCS.load(Ordering::Relaxed);
    let capacity = map.capacity();
    let resident = RwLock::new(map);
    let mut random = 22;
    let probes: Vec<_> = (0..100000)
        .map(|_| next(&mut random) as usize % n)
        .collect();
    let latest = time(&probes, |i| {
        *resident
            .read()
            .unwrap()
            .get(&keys[i])
            .unwrap()
            .last()
            .unwrap()
    });
    let exact = time(&probes, |i| {
        resident.read().unwrap().get(&keys[i]).unwrap()[revisions / 2]
    });
    let hist = time(&probes, |i| {
        let m = resident.read().unwrap();
        let mut v = 0;
        for a in m.get(&keys[i]).unwrap() {
            v ^= black_box(*a);
        }
        v
    });
    let paged_latest = time(&probes, |i| index.latest(&keys[i]).unwrap().unwrap().1);
    let paged_exact = time(&probes, |i| {
        index
            .get(&keys[i], (revisions / 2) as u16)
            .unwrap()
            .unwrap()
    });
    let snapshot = index.snapshot().unwrap();
    let paged_history = time(&probes[..20000], |i| {
        let mut v = 0;
        snapshot
            .history(&keys[i], |_, a| {
                v ^= black_box(a);
            })
            .unwrap();
        v
    });
    println!("reads,{n},{revisions},hash_latest,{latest:.2}");
    println!("reads,{n},{revisions},hash_exact,{exact:.2}");
    println!("reads,{n},{revisions},hash_history,{hist:.2}");
    println!("reads,{n},{revisions},paged_latest,{paged_latest:.2}");
    println!("reads,{n},{revisions},paged_exact,{paged_exact:.2}");
    println!("reads,{n},{revisions},paged_history,{paged_history:.2}");
    eprintln!(
        "storage objects={n} revisions={revisions} hash_allocated_bytes={allocated} hash_allocations={allocs} hash_capacity={capacity} paged_current_directory_bytes={} paged_file_bytes={} durable_build_ms={durable_ms:.3}",
        snapshot.directory_bytes(),
        index.file_bytes().unwrap()
    );
    // Only 5% of objects are resident. Control request hit rate independently.
    let hot_start = n * 95 / 100;
    let mut hot = HashMap::with_capacity(n - hot_start);
    for (i, k) in keys.iter().enumerate().skip(hot_start) {
        hot.insert(*k, (0..revisions).map(|r| addr(i, r)).collect::<Vec<_>>());
    }
    let hot = RwLock::new(hot);
    for percent in [0, 50, 90, 99, 100] {
        let mut seed = 112;
        let mixed: Vec<_> = (0..100000)
            .map(|i| {
                let r = next(&mut seed) as usize;
                if i % 100 < percent {
                    hot_start + r % (n - hot_start)
                } else {
                    r % hot_start
                }
            })
            .collect();
        let ns = time(&mixed, |i| {
            let found = hot
                .read()
                .unwrap()
                .get(&keys[i])
                .and_then(|v| v.last().copied());
            found.unwrap_or_else(|| index.latest(&keys[i]).unwrap().unwrap().1)
        });
        println!("hybrid,{n},{revisions},hit_{percent},{ns:.2}");
    }
    // Volatile update measurements. One exclusive lock per change; no I/O.
    let mut samples = vec![];
    for _ in 0..5 {
        let t = Instant::now();
        for (i, k) in keys.iter().enumerate() {
            resident
                .write()
                .unwrap()
                .get_mut(k)
                .unwrap()
                .push(addr(i, revisions));
        }
        samples.push(t.elapsed().as_nanos() as f64 / n as f64);
        let mut m = resident.write().unwrap();
        for refs in m.values_mut() {
            assert_eq!(refs.len(), revisions + 1);
            refs.pods();
        }
    }
    samples.sort_by(f64::total_cmp);
    println!(
        "writes,{n},{revisions},hash_append_reserved,{:.2}",
        samples[2]
    );
    let t = Instant::now();
    let mut fresh = HashMap::with_capacity(n);
    for (i, k) in keys.iter().enumerate() {
        fresh.insert(*k, vec![addr(i, 0)]);
    }
    black_box(&fresh);
    println!(
        "writes,{n},{revisions},hash_new_object_preallocated_table,{:.2}",
        t.elapsed().as_nanos() as f64 / n as f64
    );
    // Full table creation including bucket allocation and per-object vectors.
    let t = Instant::now();
    let built = build(&keys, revisions, 1);
    black_box(&built);
    println!(
        "writes,{n},{revisions},hash_build_per_reference,{:.2}",
        t.elapsed().as_nanos() as f64 / (n * revisions) as f64
    );
    // Semantic checks: none of the timed hot paths silently substitutes payloads.
    for i in probes.into_iter().take(1000) {
        assert_eq!(
            resident.read().unwrap()[&keys[i]].last(),
            Some(&addr(i, revisions - 1))
        );
        assert_eq!(
            index.latest(&keys[i]).unwrap(),
            Some(((revisions - 1) as u16, addr(i, revisions - 1)))
        );
    }
}
fn main() {
    println!("phase,objects,revisions,operation,ns_per_operation");
    run(100000, 1);
    run(100000, 9);
    run(1000000, 1);
}
