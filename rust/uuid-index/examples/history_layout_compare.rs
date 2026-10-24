//! Equal histories, equal immutable-seal I/O, no object hash table on either path.
//! Isolates representation from the two prototypes' different mutable writers.
use std::{
    cmp::Ordering,
    fs::{File, OpenOptions},
    hint::black_box,
    io::Write,
    os::unix::fs::FileExt,
    path::Path,
    time::Instant,
};
use trunk_uuid_index::slotted::{
    Command, Stats,
    page::{HEADER, PAGE, Page},
};
const WIDTH: usize = 26;
const FLAT_CAP: usize = (PAGE - HEADER) / WIDTH;
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
struct Key([u8; 16], u16);
#[derive(Clone, Copy)]
struct Row {
    key: Key,
    address: u64,
}
#[derive(Clone, Copy, Default)]
struct Cost {
    reads: u64,
    comparisons: u64,
    pointers: u64,
}
fn rng(s: &mut u64) -> u64 {
    *s ^= *s << 13;
    *s ^= *s >> 7;
    *s ^= *s << 17;
    *s
}
fn uuid(n: usize) -> [u8; 16] {
    let mut s = n as u64 + 937;
    let mut k = [0; 16];
    k[..8].copy_from_slice(&rng(&mut s).to_be_bytes());
    k[8..].copy_from_slice(&rng(&mut s).to_be_bytes());
    k[6] = (k[6] & 15) | 64;
    k[8] = (k[8] & 63) | 128;
    k
}
fn count(case: &str, i: usize) -> usize {
    match case {
        "mixed" => {
            if i.is_multiple_of(20) {
                9
            } else if i.is_multiple_of(5) {
                2
            } else {
                1
            }
        }
        "gap" => 2,
        _ => case.parse().unwrap(),
    }
}
fn rev(case: &str, j: usize) -> u16 {
    if case == "gap" && j == 1 {
        488
    } else {
        j as u16
    }
}
fn addr(i: usize, r: u16) -> u64 {
    ((i as u64 + 1) << 32) | (r as u64 + 1)
}
fn flat_key(p: &Page, n: usize) -> Key {
    let at = HEADER + n * WIDTH;
    Key(
        p.0[at..at + 16].try_into().unwrap(),
        u16::from_le_bytes(p.0[at + 16..at + 18].try_into().unwrap()),
    )
}
fn flat_ref(p: &Page, n: usize) -> u64 {
    let at = HEADER + n * WIDTH + 18;
    u64::from_le_bytes(p.0[at..at + 8].try_into().unwrap())
}
fn flat_bound(p: &Page, key: &Key, upper: bool, cost: &mut Cost) -> usize {
    let (mut lo, mut hi) = (0, p.0[28] as usize);
    while lo < hi {
        let mid = (lo + hi) / 2;
        cost.comparisons += 1;
        let c = flat_key(p, mid).cmp(key);
        if c == Ordering::Less || (upper && c == Ordering::Equal) {
            lo = mid + 1
        } else {
            hi = mid
        }
    }
    lo
}
struct Reader<const REPEATED: bool> {
    file: File,
    fences: Vec<Key>,
    data_bytes: u64,
    stored_bytes: u64,
    assembly_ms: f64,
    flush_ms: f64,
}
impl<const R: bool> Reader<R> {
    fn build(path: &Path, rows: &[Row]) -> Self {
        let file = OpenOptions::new()
            .create_new(true)
            .read(true)
            .write(true)
            .open(path)
            .unwrap();
        let mut output = std::io::BufWriter::with_capacity(1024 * 1024, &file);
        let mut fences = vec![];
        let start = Instant::now();
        let mut p = Page::default();
        let flush =
            |p: &mut Page, fences: &mut Vec<Key>, output: &mut std::io::BufWriter<&File>| {
                if R {
                    fences.push(flat_key(p, 0));
                } else {
                    fences.push(Key(p.first(), 0));
                }
                p.seal();
                output.write_all(&p.0).unwrap();
                *p = Page::default();
            };
        if R {
            for row in rows {
                let n = p.0[28] as usize;
                if n == 0 {
                    p.0.fill(0);
                    p.0[..4].copy_from_slice(b"REP0");
                    p.0[29] = 2;
                }
                let at = HEADER + n * WIDTH;
                p.0[at..at + 16].copy_from_slice(&row.key.0);
                p.0[at + 16..at + 18].copy_from_slice(&row.key.1.to_le_bytes());
                p.0[at + 18..at + 26].copy_from_slice(&row.address.to_le_bytes());
                p.0[28] += 1;
                if p.0[28] as usize == FLAT_CAP {
                    flush(&mut p, &mut fences, &mut output);
                }
            }
        } else {
            let mut at = 0;
            while at < rows.len() {
                let mut end = at + 1;
                while end < rows.len() && rows[end].key.0 == rows[at].key.0 {
                    end += 1;
                }
                let first = rows[at];
                let last = rows[end - 1];
                let wanted = 1 + ((last.key.1 - first.key.1) as usize).div_ceil(4);
                if p.used() + wanted > 123 {
                    flush(&mut p, &mut fences, &mut output);
                }
                let i = p.used();
                // Direct bulk serialization of the agreed format; no repeated
                // insertion/retention bookkeeping while assembling a sealed page.
                for unit in 0..wanted {
                    p.0[HEADER + i + unit] =
                        ((5 + i + unit) as u8) | if unit == 0 { 0 } else { 128 };
                }
                p.0[28] += (wanted) as u8;
                let parent = (5 + i) * 32;
                p.0[parent..parent + 16].copy_from_slice(&first.key.0);
                p.0[parent + 16..parent + 18].copy_from_slice(&first.key.1.to_le_bytes());
                for row in &rows[at..end] {
                    let delta = (row.key.1 - first.key.1) as usize;
                    let pos = if delta == 0 {
                        parent + 24
                    } else {
                        (6 + i + (delta - 1) / 4) * 32 + ((delta - 1) % 4) * 8
                    };
                    p.0[pos..pos + 8].copy_from_slice(&row.address.to_le_bytes());
                }
                at = end;
            }
        }
        if p.0[28] > 0 {
            flush(&mut p, &mut fences, &mut output);
        }
        let data_bytes = fences.len() as u64 * PAGE as u64;
        // The common footer stores minimum composite keys; page ordinal is
        // implicit because this benchmark's sealed page images are contiguous.
        for k in &fences {
            output.write_all(&k.0).unwrap();
            output.write_all(&k.1.to_le_bytes()).unwrap();
        }
        output.write_all(&data_bytes.to_le_bytes()).unwrap();
        output
            .write_all(&(fences.len() as u64).to_le_bytes())
            .unwrap();
        output.flush().unwrap();
        drop(output);
        let assembly_ms = start.elapsed().as_secs_f64() * 1000.;
        let t = Instant::now();
        file.sync_all().unwrap();
        let flush_ms = t.elapsed().as_secs_f64() * 1000.;
        let stored_bytes = file.metadata().unwrap().len();
        Self {
            file,
            fences,
            data_bytes,
            stored_bytes,
            assembly_ms,
            flush_ms,
        }
    }
    fn read(&self, id: usize, cost: &mut Cost) -> Page {
        let mut p = Page([0; PAGE]);
        self.file
            .read_exact_at(&mut p.0, id as u64 * PAGE as u64)
            .unwrap();
        let expected = u32::from_le_bytes(p.0[24..28].try_into().unwrap());
        p.0[24..28].fill(0);
        assert_eq!(crc32c::crc32c(&p.0), expected);
        p.0[24..28].copy_from_slice(&expected.to_le_bytes());
        cost.reads += 1;
        p
    }
    fn route(&self, key: &Key, cost: &mut Cost) -> Option<usize> {
        let n = self.fences.partition_point(|k| {
            cost.comparisons += 1;
            *k <= *key
        });
        n.checked_sub(1)
    }
    fn get(&self, key: Key, latest: bool, cost: &mut Cost) -> Option<(u16, u64)> {
        let route = if latest { Key(key.0, u16::MAX) } else { key };
        let id = self.route(&route, cost)?;
        let p = self.read(id, cost);
        if R {
            let at = flat_bound(&p, &route, latest, cost);
            let at = if latest { at.checked_sub(1)? } else { at };
            if at >= p.0[28] as usize {
                return None;
            }
            let got = flat_key(&p, at);
            if got.0 != key.0 || (!latest && got.1 != key.1) {
                return None;
            }
            Some((got.1, flat_ref(&p, at)))
        } else {
            let mut s = Stats::default();
            let found = p.find(&key.0, &mut s);
            cost.comparisons += s.uuid_comparisons;
            cost.pointers += s.pointer_probes;
            let at = found?;
            if latest {
                p.latest(at)
            } else {
                p.reference(at, key.1).map(|v| (key.1, v))
            }
        }
    }
    fn history(&self, k: [u8; 16], cost: &mut Cost, mut visit: impl FnMut(u16, u64)) {
        let Some(mut id) = self.route(&Key(k, 0), cost) else {
            return;
        };
        if !R {
            let p = self.read(id, cost);
            let mut s = Stats::default();
            if let Some(at) = p.find(&k, &mut s) {
                p.history(at, &mut visit);
            }
            cost.comparisons += s.uuid_comparisons;
            cost.pointers += s.pointer_probes;
            return;
        }
        loop {
            let p = self.read(id, cost);
            let begin = flat_bound(&p, &Key(k, 0), false, cost);
            for i in begin..p.0[28] as usize {
                let found = flat_key(&p, i);
                if found.0 != k {
                    return;
                }
                visit(found.1, flat_ref(&p, i));
            }
            id += 1;
            if id == self.fences.len() || self.fences[id].0 != k {
                return;
            }
        }
    }
    fn verify(&self, case: &str, objects: usize) {
        if !R {
            for id in 0..self.fences.len() {
                self.read(id, &mut Cost::default()).validate().unwrap();
            }
        }
        for i in 0..objects {
            let k = uuid(i);
            let n = count(case, i);
            let r = rev(case, n - 1);
            assert_eq!(
                self.get(Key(k, 0), true, &mut Cost::default()),
                Some((r, addr(i, r)))
            );
            let mut j = 0;
            self.history(k, &mut Cost::default(), |revision, address| {
                assert_eq!(revision, rev(case, j));
                assert_eq!(address, addr(i, revision));
                j += 1;
            });
            assert_eq!(j, n);
            assert_eq!(self.get(Key(k, r + 1), false, &mut Cost::default()), None);
        }
        for i in objects..objects + 100 {
            assert_eq!(self.get(Key(uuid(i), 0), true, &mut Cost::default()), None);
        }
    }
    fn bench(&self, case: &str, objects: usize, op: &str) -> (f64, f64, f64, f64) {
        let mut rand = 29;
        let n = if op == "history" { 2000 } else { 10000 };
        let probes: Vec<_> = (0..n).map(|_| rng(&mut rand) as usize % objects).collect();
        let mut samples = vec![];
        let mut stats = Cost::default();
        for run in 0..6 {
            let t = Instant::now();
            let mut cost = Cost::default();
            for &i in &probes {
                let k = black_box(uuid(i));
                if op == "history" {
                    self.history(k, &mut cost, |r, a| {
                        black_box((r, a));
                    });
                } else {
                    let revision = rev(case, count(case, i) / 2);
                    black_box(self.get(Key(k, revision), op == "latest", &mut cost));
                }
            }
            let elapsed = t.elapsed().as_nanos() as f64 / n as f64;
            if run > 0 {
                samples.push(elapsed)
            }
            stats = cost;
        }
        samples.sort_by(f64::total_cmp);
        (
            samples[2],
            stats.reads as f64 / n as f64,
            stats.comparisons as f64 / n as f64,
            stats.pointers as f64 / n as f64,
        )
    }
}
fn run(case: &str, objects: usize) {
    let mut rows = Vec::new();
    for i in 0..objects {
        for j in 0..count(case, i) {
            let r = rev(case, j);
            rows.push(Row {
                key: Key(uuid(i), r),
                address: addr(i, r),
            });
        }
    }
    let mut rand = 17;
    for i in (1..rows.len()).rev() {
        rows.swap(i, rng(&mut rand) as usize % (i + 1));
    }
    let t = Instant::now();
    rows.sort_unstable_by_key(|r| r.key);
    let sort_ms = t.elapsed().as_secs_f64() * 1000.;
    let dir = tempfile::tempdir().unwrap();
    let mut grouped_runs = Vec::new();
    let mut repeated_runs = Vec::new();
    for trial in 0..3 {
        if trial % 2 == 0 {
            grouped_runs.push(Reader::<false>::build(
                &dir.path().join(format!("grouped-{trial}")),
                &rows,
            ));
            repeated_runs.push(Reader::<true>::build(
                &dir.path().join(format!("repeated-{trial}")),
                &rows,
            ));
        } else {
            repeated_runs.push(Reader::<true>::build(
                &dir.path().join(format!("repeated-{trial}")),
                &rows,
            ));
            grouped_runs.push(Reader::<false>::build(
                &dir.path().join(format!("grouped-{trial}")),
                &rows,
            ));
        }
    }
    fn median_reader<const R: bool>(mut runs: Vec<Reader<R>>) -> Reader<R> {
        let mut assembly: Vec<_> = runs.iter().map(|r| r.assembly_ms).collect();
        let mut sync: Vec<_> = runs.iter().map(|r| r.flush_ms).collect();
        assembly.sort_by(f64::total_cmp);
        sync.sort_by(f64::total_cmp);
        let mut result = runs.pop().unwrap();
        result.assembly_ms = assembly[1];
        result.flush_ms = sync[1];
        result
    }
    let grouped = median_reader(grouped_runs);
    let repeated = median_reader(repeated_runs);
    grouped.verify(case, objects);
    repeated.verify(case, objects);
    fn print<const R: bool>(
        reader: &Reader<R>,
        case: &str,
        objects: usize,
        records: usize,
        sort_ms: f64,
    ) {
        for op in ["latest", "exact", "history"] {
            let (ns, reads, compares, pointers) = reader.bench(case, objects, op);
            println!(
                "{case},{objects},{records},{},{op},{sort_ms:.3},{:.3},{:.3},{},{},{},{ns:.2},{reads:.3},{compares:.3},{pointers:.3}",
                if R { "repeated_uuid" } else { "slotted" },
                reader.assembly_ms,
                reader.flush_ms,
                reader.data_bytes,
                reader.stored_bytes,
                reader.fences.capacity() * std::mem::size_of::<Key>()
            );
        }
    }
    print(&grouped, case, objects, rows.len(), sort_ms);
    print(&repeated, case, objects, rows.len(), sort_ms);
}
fn insert_cpu() {
    let objects = 24;
    let mut cmds = vec![];
    for r in 0..5 {
        for i in 0..objects {
            cmds.push(Command::put(uuid(i), r, addr(i, r)));
        }
    }
    for repeated in [false, true] {
        let t = Instant::now();
        let mut moved = 0u64;
        for _ in 0..2000 {
            let mut p = Page::default();
            if repeated {
                p.0.fill(0);
            }
            for c in &cmds {
                if repeated {
                    let n = p.0[28] as usize;
                    let at =
                        flat_bound(&p, &Key(c.object, c.revision), false, &mut Cost::default());
                    let bytes = (n - at) * WIDTH;
                    moved += bytes as u64;
                    p.0.copy_within(
                        HEADER + at * WIDTH..HEADER + n * WIDTH,
                        HEADER + (at + 1) * WIDTH,
                    );
                    let pos = HEADER + at * WIDTH;
                    p.0[pos..pos + 16].copy_from_slice(&c.object);
                    p.0[pos + 16..pos + 18].copy_from_slice(&c.revision.to_le_bytes());
                    p.0[pos + 18..pos + 26].copy_from_slice(&c.address.to_le_bytes());
                    p.0[28] += 1;
                } else {
                    p.apply(c, None).unwrap();
                }
                black_box(&p);
            }
        }
        eprintln!(
            "page_insert layout={} ns_per_revision={:.2} repeated_bytes_moved_per_revision={:.2}",
            if repeated { "repeated_uuid" } else { "slotted" },
            t.elapsed().as_nanos() as f64 / (2000 * cmds.len()) as f64,
            moved as f64 / (2000 * cmds.len()) as f64
        );
    }
}
fn main() {
    println!(
        "case,objects,references,layout,operation,shared_sort_ms,assembly_write_ms,sync_ms,data_bytes,file_bytes,directory_capacity_bytes,lookup_ns,page_reads,comparisons,pointer_probes"
    );
    let only = std::env::args().nth(1);
    for case in [
        "1", "2", "3", "5", "9", "61", "241", "249", "489", "mixed", "gap",
    ] {
        if only.as_ref().is_none_or(|v| v == case) {
            run(case, if case == "mixed" { 100000 } else { 10000 });
        }
    }
    insert_cpu();
}
