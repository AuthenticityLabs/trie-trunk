use std::collections::HashMap;
use std::io;
use std::ops::Range;

#[cfg(test)]
use crate::IndexRow;
use crate::filter::{BoundFilter, Decision};

pub(crate) struct Postings(HashMap<u32, Box<[u32]>>);

pub(crate) enum Positions {
    Range(Range<usize>),
    Selected(Box<[u32]>),
}

impl Postings {
    #[cfg(test)]
    pub fn new(rows: &[IndexRow]) -> Self {
        Self::from_leaves(
            rows.iter()
                .map(|row| u32::from_be_bytes(row[16..20].try_into().unwrap())),
        )
    }
    pub fn from_leaves(values: impl Iterator<Item = u32>) -> Self {
        let mut leaves = HashMap::<u32, Vec<u32>>::new();
        for (position, leaf) in values.enumerate() {
            leaves.entry(leaf).or_default().push(position as u32);
        }
        Self(
            leaves
                .into_iter()
                .map(|(leaf, rows)| (leaf, rows.into_boxed_slice()))
                .collect(),
        )
    }

    pub fn select(&self, range: Range<usize>, filter: &BoundFilter) -> io::Result<Positions> {
        self.select_leaves(
            range,
            |leaf| Ok(filter.decision(leaf)? != Decision::Exclude),
        )
    }

    fn select_leaves(
        &self,
        range: Range<usize>,
        mut accepts: impl FnMut(u32) -> io::Result<bool>,
    ) -> io::Result<Positions> {
        let mut lists = Vec::new();
        let mut count = 0;
        for (&leaf, positions) in &self.0 {
            if !accepts(leaf)? {
                continue;
            }
            let start = positions.partition_point(|&row| (row as usize) < range.start);
            let end = positions.partition_point(|&row| (row as usize) < range.end);
            count += end - start;
            lists.push(&positions[start..end]);
        }
        if count == range.len() {
            return Ok(Positions::Range(range));
        }
        let mut positions = Vec::with_capacity(count);
        for list in lists {
            positions.extend_from_slice(list);
        }
        positions.sort_unstable();
        Ok(Positions::Selected(positions.into_boxed_slice()))
    }
}

impl Positions {
    pub fn len(&self) -> usize {
        match self {
            Self::Range(range) => range.len(),
            Self::Selected(rows) => rows.len(),
        }
    }

    pub fn at(&self, position: usize) -> usize {
        match self {
            Self::Range(range) => range.start + position,
            Self::Selected(rows) => rows[position] as usize,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn multiple_leaves_and_windows_match_row_scan() {
        let rows: Vec<_> = (0..10_000_u32)
            .map(|i| {
                let mut row = [0; 36];
                row[16..20].copy_from_slice(&(i % 17).to_be_bytes());
                row
            })
            .collect();
        let postings = Postings::new(&rows);
        for range in [0..0, 0..rows.len(), 31..8111, 9999..10_000] {
            for accepted in [
                &[][..],
                &[0][..],
                &[0, 3, 9, 16][..],
                &(0..17).collect::<Vec<_>>()[..],
            ] {
                let positions = postings
                    .select_leaves(range.clone(), |leaf| Ok(accepted.contains(&leaf)))
                    .unwrap();
                let expected: Vec<_> = range
                    .clone()
                    .filter(|i| accepted.contains(&(*i as u32 % 17)))
                    .collect();
                let actual: Vec<_> = (0..positions.len()).map(|i| positions.at(i)).collect();
                assert_eq!(actual, expected);
            }
        }
    }
}
