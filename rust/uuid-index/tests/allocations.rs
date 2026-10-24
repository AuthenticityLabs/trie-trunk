use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;
use trunk_uuid_index::{KeyKind, UuidIndex};

thread_local! {
    static TRACK: Cell<bool> = const { Cell::new(false) };
    static COUNT: Cell<usize> = const { Cell::new(0) };
}
struct Counting;
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        if TRACK.try_with(Cell::get).unwrap_or(false) {
            let _ = COUNT.try_with(|n| n.set(n.get() + 1));
        }
        unsafe { System.alloc(layout) }
    }
    unsafe fn dealloc(&self, p: *mut u8, layout: Layout) {
        unsafe { System.dealloc(p, layout) }
    }
    unsafe fn realloc(&self, p: *mut u8, layout: Layout, size: usize) -> *mut u8 {
        if TRACK.try_with(Cell::get).unwrap_or(false) {
            let _ = COUNT.try_with(|n| n.set(n.get() + 1));
        }
        unsafe { System.realloc(p, layout, size) }
    }
}
#[global_allocator]
static ALLOCATOR: Counting = Counting;

#[test]
fn point_reads_do_not_allocate() {
    let dir = tempfile::tempdir().unwrap();
    let index = UuidIndex::create(dir.path().join("objects.idx"), KeyKind::Uuid).unwrap();
    index.upsert([4; 16], 77).unwrap();
    COUNT.set(0);
    TRACK.set(true);
    for _ in 0..1000 {
        assert_eq!(index.get(&[4; 16]).unwrap(), Some(77));
        assert_eq!(index.get(&[5; 16]).unwrap(), None);
        assert_eq!(index.get(&[3; 16]).unwrap(), None);
    }
    TRACK.set(false);
    assert_eq!(COUNT.get(), 0);
}

#[test]
fn slotted_reads_do_not_allocate() {
    use trunk_uuid_index::slotted::{Command, Index};
    let dir = tempfile::tempdir().unwrap();
    let index = Index::create(dir.path().join("collection.idx"), KeyKind::Uuid, None).unwrap();
    index
        .apply(&[Command::put([4; 16], 0, 77), Command::put([4; 16], 8, 99)])
        .unwrap();
    let snapshot = index.snapshot().unwrap();
    COUNT.set(0);
    TRACK.set(true);
    for _ in 0..1000 {
        assert_eq!(index.latest(&[4; 16]).unwrap(), Some((8, 99)));
        assert_eq!(snapshot.get(&[4; 16], 0).unwrap(), Some(77));
        assert_eq!(snapshot.get(&[4; 16], 4).unwrap(), None);
        assert_eq!(snapshot.latest(&[5; 16]).unwrap(), None);
        snapshot
            .history(&[4; 16], |_, v| {
                std::hint::black_box(v);
            })
            .unwrap();
    }
    TRACK.set(false);
    assert_eq!(COUNT.get(), 0);
}
