use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use tantivy::collector::DocSetCollector;
// Preserve the existing document collector while making result enumeration
// cooperatively cancellable. Query/scorer construction may finish before the
// next cancellation check; cancellation never kills another query's engine.
use tantivy::collector::{Collector, SegmentCollector};
pub struct CancelCollector(pub Arc<AtomicBool>);
impl Collector for CancelCollector {
    type Fruit = <DocSetCollector as Collector>::Fruit;
    type Child = <DocSetCollector as Collector>::Child;
    fn requires_scoring(&self) -> bool {
        false
    }
    fn for_segment(&self, n: u32, r: &tantivy::SegmentReader) -> tantivy::Result<Self::Child> {
        DocSetCollector.for_segment(n, r)
    }
    fn merge_fruits(
        &self,
        f: Vec<<Self::Child as SegmentCollector>::Fruit>,
    ) -> tantivy::Result<Self::Fruit> {
        self.check()?;
        DocSetCollector.merge_fruits(f)
    }
    fn collect_segment(
        &self,
        w: &dyn tantivy::query::Weight,
        n: u32,
        r: &tantivy::SegmentReader,
    ) -> tantivy::Result<<Self::Child as SegmentCollector>::Fruit> {
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
impl CancelCollector {
    fn check(&self) -> tantivy::Result<()> {
        if self.0.load(Ordering::Relaxed) {
            Err(tantivy::TantivyError::InvalidArgument(
                "search canceled".into(),
            ))
        } else {
            Ok(())
        }
    }
}
