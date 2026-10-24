use std::{
    sync::{
        Arc, Barrier,
        atomic::{AtomicBool, Ordering},
    },
    time::Instant,
};
use trunk_uuid_index::{
    Entry, KeyKind, UuidIndex,
    slotted::{Command, Index},
};
fn key(n: u64) -> [u8; 16] {
    let mut k = [0; 16];
    k[8..].copy_from_slice(&n.to_be_bytes());
    k
}
fn measure(
    label: &str,
    get: impl Fn(&[u8; 16]) -> u64 + Send + Sync + 'static,
    write: impl FnOnce(),
) {
    let get = Arc::new(get);
    let done = Arc::new(AtomicBool::new(false));
    let start = Arc::new(Barrier::new(3));
    let mut handles = vec![];
    for thread in 0..2 {
        let get = get.clone();
        let done = done.clone();
        let start = start.clone();
        handles.push(std::thread::spawn(move || {
            let mut samples = Vec::with_capacity(1_000_000);
            let mut n = thread;
            start.wait();
            while !done.load(Ordering::Relaxed) {
                n = (n * 1664525 + 1013904223) % 20000;
                let t = Instant::now();
                std::hint::black_box(get(&key(n)));
                let elapsed = t.elapsed().as_nanos() as u64;
                if samples.len() < 1_000_000 {
                    samples.push(elapsed);
                }
            }
            samples
        }));
    }
    start.wait();
    let t = Instant::now();
    write();
    let ms = t.elapsed().as_secs_f64() * 1000.;
    done.store(true, Ordering::Relaxed);
    let mut samples: Vec<_> = handles
        .into_iter()
        .flat_map(|h| h.join().unwrap())
        .collect();
    samples.sort_unstable();
    let n = samples.len();
    println!(
        "{label},{ms:.3},{n},{},{},{},{}",
        samples[n / 2],
        samples[n * 95 / 100],
        samples[n * 99 / 100],
        samples[n - 1]
    );
}
fn main() {
    let dir = tempfile::tempdir().unwrap();
    println!("case,write_ms,sampled_reads,p50_ns,p95_ns,p99_ns,max_ns");
    let index = Arc::new(Index::create(dir.path().join("cow.idx"), KeyKind::Uuid, None).unwrap());
    let base = Arc::new(UuidIndex::create(dir.path().join("old.idx"), KeyKind::Uuid).unwrap());
    for batch in (0..20000)
        .map(|n| Command::put(key(n), 0, n + 1))
        .collect::<Vec<_>>()
        .chunks(1024)
    {
        index.apply(batch).unwrap();
        base.upsert_batch(
            &batch
                .iter()
                .map(|c| Entry {
                    key: c.object,
                    address: c.address,
                })
                .collect::<Vec<_>>(),
        )
        .unwrap();
    }
    let i = index.clone();
    measure(
        "cow_readers_during_write",
        move |k| i.latest(k).unwrap().unwrap().1,
        || {
            for r in 1..=40 {
                index
                    .apply(
                        &(0..1024)
                            .map(|n| {
                                Command::put(key((n * 17 + r as u64 * 31) % 20000), r, n + 50000)
                            })
                            .collect::<Vec<_>>(),
                    )
                    .unwrap();
            }
        },
    );
    let i = base.clone();
    measure(
        "baseline_readers_during_write",
        move |k| i.get(k).unwrap().unwrap(),
        || {
            for r in 1..=40 {
                base.upsert_batch(
                    &(0..1024)
                        .map(|n| Entry {
                            key: key((n * 17 + r * 31) % 20000),
                            address: n + 50000,
                        })
                        .collect::<Vec<_>>(),
                )
                .unwrap();
            }
        },
    );
}
