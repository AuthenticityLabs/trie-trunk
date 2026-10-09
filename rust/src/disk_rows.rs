use crate::IndexRow;
use crate::store::invalid;
use std::{io, ops::Range};

/// A borrowed view of packed rows. Compact starts are derived only from their
/// physical predecessor in this run, never from selected or merged neighbors.
pub(crate) struct Rows<'a> {
    pub data: &'a [u8],
    pub width: usize,
    pub base: u64,
    pub header: usize,
}
impl<'a> Rows<'a> {
    pub fn parse(payload: &'a [u8], compact: bool, narrow: bool) -> io::Result<Self> {
        let width = (if compact { 28 } else { 36 }) - if narrow { 2 } else { 0 };
        let header = if compact { 12 } else { 4 };
        if payload.len() < header {
            return Err(invalid("short index run"));
        }
        let count = u32::from_be_bytes(payload[..4].try_into().unwrap()) as usize;
        if count == 0
            || (payload.len() - header) / width != count
            || !(payload.len() - header).is_multiple_of(width)
        {
            return Err(invalid("invalid index run length"));
        }
        Ok(Self {
            data: &payload[header..],
            width,
            header,
            base: if compact {
                u64::from_be_bytes(payload[4..12].try_into().unwrap())
            } else {
                0
            },
        })
    }
    pub fn len(&self) -> usize {
        self.data.len() / self.width
    }
    pub fn bytes(&self, i: usize) -> &'a [u8] {
        &self.data[i * self.width..(i + 1) * self.width]
    }
    pub fn uuid(&self, i: usize) -> &'a [u8] {
        &self.bytes(i)[..16]
    }
    pub fn time(&self, i: usize) -> u64 {
        u64::from_be_bytes(self.bytes(i)[..8].try_into().unwrap()) >> 16
    }
    pub fn leaf(&self, i: usize) -> u32 {
        if matches!(self.width, 26 | 34) {
            u16::from_be_bytes(self.bytes(i)[16..18].try_into().unwrap()).into()
        } else {
            u32::from_be_bytes(self.bytes(i)[16..20].try_into().unwrap())
        }
    }
    pub fn end(&self, i: usize) -> u64 {
        let b = self.bytes(i);
        u64::from_be_bytes(b[self.width - 8..].try_into().unwrap())
    }
    pub fn row(&self, i: usize) -> IndexRow {
        let b = self.bytes(i);
        let mut row = [0; 36];
        if self.width == 36 {
            row.copy_from_slice(b)
        } else {
            row[..16].copy_from_slice(&b[..16]);
            row[16..20].copy_from_slice(&self.leaf(i).to_be_bytes());
            let start = if self.width == 34 {
                u64::from_be_bytes(b[18..26].try_into().unwrap())
            } else if i == 0 {
                self.base
            } else {
                self.end(i - 1)
            };
            row[20..28].copy_from_slice(&start.to_be_bytes());
            row[28..].copy_from_slice(&self.end(i).to_be_bytes());
        }
        row
    }
    pub fn lower(&self, key: u64) -> usize {
        let (mut lo, mut hi) = (0, self.len());
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            if self.time(mid) < key {
                lo = mid + 1
            } else {
                hi = mid
            }
        }
        lo
    }
    pub fn window(&self, start: u64, end: u64) -> Range<usize> {
        let lo = self.lower(start);
        lo..if start >= end {
            lo
        } else {
            self.lower(end).max(lo)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn ref16_rows_preserve_boundaries_and_maximum_reference() {
        for compact in [false, true] {
            let mut payload = 3u32.to_be_bytes().to_vec();
            if compact {
                payload.extend_from_slice(&16u64.to_be_bytes());
            }
            let mut start = 16u64;
            for (i, leaf) in [0u16, 1, 65535].into_iter().enumerate() {
                let mut id = [0u8; 16];
                id[..6].copy_from_slice(&(1000 + i as u64).to_be_bytes()[2..]);
                id[6] = 0x70;
                id[8] = 0x80;
                payload.extend_from_slice(&id);
                payload.extend_from_slice(&leaf.to_be_bytes());
                if !compact {
                    payload.extend_from_slice(&start.to_be_bytes());
                }
                start += i as u64 + 1;
                payload.extend_from_slice(&start.to_be_bytes());
            }
            let rows = Rows::parse(&payload, compact, true).unwrap();
            assert_eq!(rows.width, if compact { 26 } else { 34 });
            assert_eq!(rows.leaf(2), 65535);
            assert_eq!(&rows.row(2)[20..28], &19u64.to_be_bytes());
            assert_eq!(rows.window(1001, 1003), 1..3);
            assert!(Rows::parse(&payload, compact, false).is_err());
            for n in 0..payload.len() {
                assert!(Rows::parse(&payload[..n], compact, true).is_err());
            }
        }
    }
    #[test]
    fn compact_starts_follow_physical_rows_not_matches() {
        let mut payload = Vec::new();
        payload.extend_from_slice(&4u32.to_be_bytes());
        payload.extend_from_slice(&16u64.to_be_bytes());
        for (i, end) in [19u64, 80, 81, 150].into_iter().enumerate() {
            let mut id = [0u8; 16];
            id[..6].copy_from_slice(&(100u64 + i as u64).to_be_bytes()[2..]);
            id[6] = 0x70;
            id[8] = 0x80;
            payload.extend_from_slice(&id);
            payload.extend_from_slice(&(i as u32).to_be_bytes());
            payload.extend_from_slice(&end.to_be_bytes());
        }
        let rows = Rows::parse(&payload, true, false).unwrap();
        assert_eq!(rows.window(101, 103), 1..3);
        assert_eq!(&rows.row(0)[20..28], &16u64.to_be_bytes());
        assert_eq!(&rows.row(3)[20..28], &81u64.to_be_bytes());
        for n in 0..payload.len() {
            assert!(Rows::parse(&payload[..n], true, false).is_err());
        }
        assert!(Rows::parse(&payload, false, false).is_err());
    }
}
