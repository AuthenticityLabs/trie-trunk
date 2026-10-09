use std::fs;
use std::hint::black_box;
use std::ops::Range;
use std::path::Path;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use trie_trunk_search::{IndexRow, lower_bound, window};

type Search = fn(&[IndexRow], SystemTime, SystemTime) -> Range<usize>;

fn millis(row: &IndexRow) -> u64 {
    let mut bytes = [0; 8];
    bytes[2..].copy_from_slice(&row[..6]);
    u64::from_be_bytes(bytes)
}

fn independent(rows: &[IndexRow], start: SystemTime, end: SystemTime) -> Range<usize> {
    lower_bound(rows, start)..lower_bound(rows, end)
}

fn measure(name: &str, rows: &[IndexRow], width: u64) {
    let count = rows.len();
    let first = millis(&rows[0]);
    let last = millis(&rows[count - 1]);
    let mut state = 56_u64;
    let queries: Vec<_> = (0..4096)
        .map(|i| {
            state = state
                .wrapping_mul(6_364_136_223_846_793_005)
                .wrapping_add(1_442_695_040_888_963_407);
            let target = if i % 2 == 0 {
                millis(&rows[(state % count as u64) as usize])
            } else {
                first + state % (last - first + 1)
            };
            let start = UNIX_EPOCH + Duration::from_millis(target);
            let end = start + Duration::from_millis(width);
            assert_eq!(window(rows, start, end), independent(rows, start, end));
            (start, end)
        })
        .collect();

    let methods: [(&str, Search); 2] = [("independent", independent), ("paired", window)];
    let iterations = 1_000_000;
    let mut samples = [[0.0; 2]; 5];
    for (pass, sample) in samples.iter_mut().enumerate() {
        for position in 0..methods.len() {
            let method = (pass + position) % methods.len();
            let search = black_box(methods[method].1);
            for &(start, end) in &queries {
                search(rows, start, end);
            }
            let clock = Instant::now();
            let mut result = 0..0;
            for i in 0..iterations {
                let (start, end) = queries[i & (queries.len() - 1)];
                result = search(rows, start, end);
            }
            sample[method] = clock.elapsed().as_nanos() as f64 / iterations as f64;
            black_box(result);
        }
    }
    for (method, &(label, _)) in methods.iter().enumerate() {
        let mut values = samples.map(|sample| sample[method]);
        values.sort_by(f64::total_cmp);
        println!("{name} width={width}ms {label} {:.2} ns/window", values[2]);
    }
}

fn main() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("../logs/index-test/1/2026/267/log.idx");
    let data = fs::read(&path).expect("local 10,000-row index fixture is required");
    assert!(data.len() >= 20);
    assert_eq!(&data[..4], b"TIDX");
    assert_eq!(u32::from_be_bytes(data[4..8].try_into().unwrap()), 2);
    let length = u32::from_be_bytes(data[8..12].try_into().unwrap()) as usize;
    assert_eq!(data.len(), 16 + length);
    let count = u32::from_be_bytes(data[16..20].try_into().unwrap()) as usize;
    let (rows, remainder) = data[20..].as_chunks::<36>();
    assert!(remainder.is_empty());
    assert_eq!(rows.len(), count);
    assert_eq!(count, 10_000);
    assert!(rows.windows(2).all(|pair| pair[0][..16] < pair[1][..16]));
    for width in [1, 60_000, 3_600_000, 86_400_000] {
        measure("fixture_10000", rows, width);
    }
    let base = millis(&rows[0]);
    let million: Vec<_> = (0..1_000_000)
        .map(|i| {
            let mut row = [0; 36];
            let value = base + i * 86_400_000 / 1_000_000;
            row[..6].copy_from_slice(&value.to_be_bytes()[2..]);
            row[6] = 0x70;
            row[8] = 0x80;
            row
        })
        .collect();
    for width in [1, 60_000, 3_600_000, 86_400_000] {
        measure("uniform_1000000", &million, width);
    }
}
