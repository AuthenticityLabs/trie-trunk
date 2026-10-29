use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use tantivy::collector::{Collector, SegmentCollector};
use tantivy::fastfield::Column;

// Clip matching canonical addresses before retaining candidates. Internal
// Tantivy document IDs are segment-local and carry no time ordering guarantee.
pub(crate) struct CandidateCollector {
    ranges: Arc<[(u64, u64)]>,
    cancel: Option<Arc<AtomicBool>>,
}
impl CandidateCollector {
    pub fn new(ranges: &[(u64, u64)], cancel: Option<Arc<AtomicBool>>) -> Self {
        let mut ranges: Vec<_> = ranges.iter().copied().filter(|(a, b)| a < b).collect();
        ranges.sort_unstable();
        let mut count = 0;
        for i in 0..ranges.len() {
            let (a, b) = ranges[i];
            if count > 0 && a <= ranges[count - 1].1 {
                ranges[count - 1].1 = ranges[count - 1].1.max(b);
            } else {
                ranges[count] = (a, b);
                count += 1;
            }
        }
        ranges.truncate(count);
        Self {
            ranges: ranges.into(),
            cancel,
        }
    }
    pub fn is_empty(&self) -> bool {
        self.ranges.is_empty()
    }
    pub fn check(&self) -> tantivy::Result<()> {
        if self
            .cancel
            .as_ref()
            .is_some_and(|c| c.load(Ordering::Relaxed))
        {
            Err(tantivy::TantivyError::InvalidArgument(
                "search canceled".into(),
            ))
        } else {
            Ok(())
        }
    }
}
impl Collector for CandidateCollector {
    type Fruit = Vec<u64>;
    type Child = CandidateSegment;
    fn requires_scoring(&self) -> bool {
        false
    }
    fn for_segment(&self, _: u32, r: &tantivy::SegmentReader) -> tantivy::Result<Self::Child> {
        Ok(CandidateSegment {
            ranges: self.ranges.clone(),
            high: r.fast_fields().u64("rh")?,
            low: r.fast_fields().u64("rn")?,
            rows: Vec::new(),
        })
    }
    fn merge_fruits(&self, fruits: Vec<Vec<u64>>) -> tantivy::Result<Vec<u64>> {
        self.check()?;
        Ok(fruits.into_iter().flatten().collect())
    }
    fn collect_segment(
        &self,
        w: &dyn tantivy::query::Weight,
        n: u32,
        r: &tantivy::SegmentReader,
    ) -> tantivy::Result<Vec<u64>> {
        self.check()?;
        let mut out = self.for_segment(n, r)?;
        let mut scorer = w.scorer(r, 1.0)?;
        let mut buffer = [0; tantivy::COLLECT_BLOCK_BUFFER_LEN];
        loop {
            self.check()?;
            let count = scorer.fill_buffer(&mut buffer);
            if count == 0 {
                break;
            }
            if let Some(alive) = r.alive_bitset() {
                for &doc in &buffer[..count] {
                    if alive.is_alive(doc) {
                        out.collect(doc, 0.0);
                    }
                }
            } else {
                out.collect_block(&buffer[..count]);
            }
        }
        self.check()?;
        Ok(out.harvest())
    }
}
pub(crate) struct CandidateSegment {
    ranges: Arc<[(u64, u64)]>,
    high: Column<u64>,
    low: Column<u64>,
    rows: Vec<u64>,
}
impl SegmentCollector for CandidateSegment {
    type Fruit = Vec<u64>;
    fn collect(&mut self, doc: tantivy::DocId, _: tantivy::Score) {
        if let (Some(high), Some(low)) = (self.high.first(doc), self.low.first(doc)) {
            let row = high << 32 | low;
            let at = self.ranges.partition_point(|&(_, end)| end <= row);
            if self.ranges.get(at).is_some_and(|&(start, _)| start <= row) {
                self.rows.push(row);
            }
        }
    }
    fn harvest(self) -> Vec<u64> {
        self.rows
    }
}
