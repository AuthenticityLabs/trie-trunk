mod disk_rows;
mod index_cache;
mod routing;
pub mod runtime;
use std::ops::Range;
use std::time::{SystemTime, UNIX_EPOCH};

mod filter;
mod postings;
mod schema;
pub mod search;
mod store;

pub type IndexRow = [u8; 36];

fn time_key(query: SystemTime) -> u64 {
    let Ok(elapsed) = query.duration_since(UNIX_EPOCH) else {
        return 0;
    };
    let millis = elapsed.as_millis() + u128::from(elapsed.subsec_nanos() % 1_000_000 != 0);
    millis.min(1 << 48) as u64
}

fn timestamp(row: &IndexRow) -> u64 {
    u64::from_be_bytes(row[..8].try_into().unwrap()) >> 16
}

fn partition(rows: &[IndexRow], key: u64) -> usize {
    rows.partition_point(|row| timestamp(row) < key)
}

pub fn lower_bound(rows: &[IndexRow], query: SystemTime) -> usize {
    let key = time_key(query);
    if key == 0 {
        return 0;
    }
    if key == 1 << 48 {
        return rows.len();
    }
    partition(rows, key)
}

pub fn window(rows: &[IndexRow], start: SystemTime, end: SystemTime) -> Range<usize> {
    window_keys(rows, time_key(start), time_key(end))
}

fn window_keys(rows: &[IndexRow], start: u64, end: u64) -> Range<usize> {
    if start >= end {
        let lo = partition(rows, start);
        return lo..lo;
    }
    let mut size = rows.len();
    if size == 0 {
        return 0..0;
    }
    let (mut lo, mut hi) = (0, 0);
    while size > 1 {
        let half = size / 2;
        let lo_mid = lo + half;
        let hi_mid = hi + half;
        lo = if timestamp(&rows[lo_mid]) < start {
            lo_mid
        } else {
            lo
        };
        hi = if timestamp(&rows[hi_mid]) < end {
            hi_mid
        } else {
            hi
        };
        size -= half;
    }
    lo += usize::from(timestamp(&rows[lo]) < start);
    hi += usize::from(timestamp(&rows[hi]) < end);
    lo..hi
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    fn row(millis: u64, suffix: u8) -> IndexRow {
        let mut row = [0; 36];
        row[..6].copy_from_slice(&millis.to_be_bytes()[2..]);
        row[6] = 0x70;
        row[8] = 0x80;
        row[15] = suffix;
        row
    }

    fn at(millis: u64) -> SystemTime {
        UNIX_EPOCH + Duration::from_millis(millis)
    }

    #[test]
    fn empty_index() {
        assert_eq!(lower_bound(&[], UNIX_EPOCH), 0);
        assert_eq!(lower_bound(&[], at(1 << 48)), 0);
    }

    #[test]
    fn boundaries_and_repeated_timestamps() {
        let rows = [row(0, 0), row(255, 1), row(255, 2), row(256, 0)];
        for (millis, expected) in [(0, 0), (1, 1), (254, 1), (255, 1), (256, 3), (257, 4)] {
            assert_eq!(lower_bound(&rows, at(millis)), expected);
        }
        assert_eq!(lower_bound(&rows, UNIX_EPOCH - Duration::from_nanos(1)), 0);
        assert_eq!(lower_bound(&rows, at(255) + Duration::from_nanos(1)), 3);
        assert_eq!(lower_bound(&rows, at(256) - Duration::from_nanos(1)), 3);
        assert_eq!(lower_bound(&rows, at(256) + Duration::from_nanos(1)), 4);
        let lo = lower_bound(&rows, at(255));
        let hi = lower_bound(&rows, at(256));
        assert_eq!(&rows[lo..hi], &rows[1..3]);
    }

    #[test]
    fn timestamp_limit() {
        let last = (1 << 48) - 1;
        let rows = [row(last, 0)];
        assert_eq!(lower_bound(&rows, at(last)), 0);
        assert_eq!(lower_bound(&rows, at(last) + Duration::from_nanos(1)), 1);
        assert_eq!(lower_bound(&rows, at(1 << 48)), 1);
    }

    #[test]
    fn ignores_uuid_suffix() {
        let first = row(255, 0);
        let mut second = row(255, 1);
        second[6] = 0x7f;
        second[7] = 0xff;
        let rows = [first, second, row(256, 0)];
        assert_eq!(lower_bound(&rows, at(255)), 0);
        assert_eq!(lower_bound(&rows, at(255) + Duration::from_nanos(1)), 2);
        assert_eq!(lower_bound(&rows, at(256)), 2);
    }

    #[test]
    fn timestamp_byte_rollovers() {
        for shift in (8..=40).step_by(8) {
            let boundary = 1 << shift;
            let rows = [row(boundary - 1, 0), row(boundary, 0), row(boundary + 1, 0)];
            for (i, millis) in [boundary - 1, boundary, boundary + 1]
                .into_iter()
                .enumerate()
            {
                assert_eq!(lower_bound(&rows, at(millis)), i);
            }
        }
    }

    #[test]
    fn matches_linear_reference() {
        let millis: Vec<_> = (0..10_000).map(|i| (i / 8) * 257).collect();
        let rows: Vec<_> = millis
            .iter()
            .enumerate()
            .map(|(i, &value)| row(value, (i % 8) as u8))
            .collect();
        for target in (0..=millis[millis.len() - 1] + 1).step_by(31) {
            let expected = millis
                .iter()
                .position(|&value| value >= target)
                .unwrap_or(rows.len());
            assert_eq!(lower_bound(&rows, at(target)), expected);
        }
    }

    #[test]
    fn window_boundaries() {
        let rows = [row(0, 0), row(255, 1), row(255, 2), row(256, 0)];
        assert_eq!(window(&rows, at(255), at(256)), 1..3);
        assert_eq!(window(&rows, at(255), at(255)), 1..1);
        assert_eq!(window(&rows, at(256), at(255)), 3..3);
        assert_eq!(window(&rows, at(1), at(254)), 1..1);
        assert_eq!(window(&rows, at(257), at(1000)), 4..4);
        assert_eq!(window(&[], at(0), at(1000)), 0..0);
        let before = UNIX_EPOCH - Duration::from_nanos(1);
        assert_eq!(window(&rows, before, at(0)), 0..0);
        assert_eq!(window(&rows, before, at(1)), 0..1);
        assert_eq!(window(&rows, at(0), at(1 << 48)), 0..4);
        assert_eq!(
            window(&rows, at(255) + Duration::from_nanos(1), at(256)),
            3..3
        );
        assert_eq!(
            window(&rows, at(255), at(255) + Duration::from_nanos(1)),
            1..3
        );
        let last = (1 << 48) - 1;
        let rows = [row(last, 0), row(last, 1)];
        assert_eq!(
            window(&rows, at(last), at(last) + Duration::from_nanos(1)),
            0..2
        );
        assert_eq!(window(&rows, at(1 << 48), at((1 << 48) + 1)), 2..2);
    }

    #[test]
    fn window_matches_linear_reference() {
        for size in [0, 1, 2, 3, 4, 7, 8, 9, 16, 17, 31, 32, 33, 127] {
            let values: Vec<_> = (0..size).map(|i| (1 << 24) - 9 + (i / 3) * 5).collect();
            let rows: Vec<_> = values
                .iter()
                .enumerate()
                .map(|(i, &value)| row(value, (i % 3) as u8))
                .collect();
            let times: Vec<_> = values.iter().map(|&value| at(value)).collect();
            let queries: Vec<_> = ((1 << 24) - 10..=(1 << 24) + (size / 3) * 5)
                .flat_map(|value| [at(value), at(value) + Duration::from_nanos(1)])
                .collect();
            for &start in &queries {
                let lo = times
                    .iter()
                    .position(|&value| value >= start)
                    .unwrap_or(size as usize);
                for &end in &queries {
                    let hi = times
                        .iter()
                        .position(|&value| value >= end)
                        .unwrap_or(size as usize);
                    assert_eq!(window(&rows, start, end), lo..hi.max(lo));
                }
            }
        }
    }
}

pub mod pil;

// Native Tantivy entry points used by Go; no subprocess or wire serialization.
#[allow(dead_code)]
#[path = "tantivy/mod.rs"]
mod tantivy_engine;

mod buffers;

mod live;
