use crate::store::{DAY_MILLIS, Snapshot, invalid, u32be, u64be, versions};
use std::{
    collections::BTreeMap,
    fs, io,
    path::{Path, PathBuf},
};
#[derive(Clone)]
pub(crate) struct Partition {
    pub version: PathBuf,
    pub path: PathBuf,
    pub start: u64,
    pub end: u64,
    pub owner: Option<u32>,
}
pub(crate) fn discover(
    root: &Path,
    tap: &str,
    start: u64,
    end: u64,
) -> io::Result<BTreeMap<u64, Vec<Partition>>> {
    let mut all = Vec::new();
    for version in versions(root, tap)? {
        walk(&version, &version, 0, start, end, &mut all)?;
    }
    all.sort_by(|a, b| (a.start, &a.path).cmp(&(b.start, &b.path)));
    // Connected time intervals must merge together, including timezone-offset
    // buckets and legacy day files which overlap new hourly files.
    let mut out: BTreeMap<u64, Vec<Partition>> = BTreeMap::new();
    let (mut group, mut through) = (0, 0);
    for p in all {
        if out.is_empty() || p.start >= through {
            group = p.start;
            through = p.end
        } else {
            through = through.max(p.end)
        }
        out.entry(group).or_default().push(p);
    }
    Ok(out)
}
fn walk(
    version: &Path,
    dir: &Path,
    depth: usize,
    start: u64,
    end: u64,
    out: &mut Vec<Partition>,
) -> io::Result<()> {
    if depth > 4 {
        return Ok(());
    }
    crate::runtime::check()?;
    for entry in fs::read_dir(dir)? {
        let entry = entry?;
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if entry.file_type()?.is_dir() {
            if depth == 1
                && let (Some(year), Ok(ordinal)) = (
                    dir.file_name()
                        .and_then(|y| y.to_str())
                        .and_then(|y| y.parse::<i32>().ok()),
                    name.parse::<u32>(),
                )
                && let Some(date) = chrono::NaiveDate::from_yo_opt(year, ordinal)
            {
                let midnight = date
                    .and_hms_opt(0, 0, 0)
                    .unwrap()
                    .and_utc()
                    .timestamp_millis();
                let from = (midnight - 2 * DAY_MILLIS as i64).max(0) as u64;
                let to = (midnight + 3 * DAY_MILLIS as i64).max(0) as u64;
                if to <= start || from >= end {
                    continue;
                }
            }
            if !name.starts_with('.') {
                walk(version, &entry.path(), depth + 1, start, end, out)?
            }
        } else if entry.file_type()?.is_file() && (name == "events.idx" || name == "log.idx") {
            let path = entry.path();
            let (from, to, owner) = if name == "events.idx" {
                let snap = Snapshot::open(&path, b"TIDX")?;
                let mut b = [0; 28];
                snap.file.read_exact_at(&mut b, 8)?;
                if !matches!(snap.format, 3 | 4)
                    || u32be(&b) != 20
                    || crc32c::crc32c(&b[8..]) != u32be(&b[4..])
                {
                    return Err(invalid("invalid index scope"));
                }
                let (from, to, owner) = (u64be(&b[8..]), u64be(&b[16..]), u32be(&b[24..]));
                if from >= to
                    || to > 1 << 48
                    || dir.file_name().and_then(|x| x.to_str()) != Some(owner.to_string().as_str())
                {
                    return Err(invalid("invalid index bucket/owner"));
                }
                (from, to, Some(owner))
            } else {
                let rel = dir.strip_prefix(version).map_err(invalid)?;
                let text = rel
                    .to_str()
                    .ok_or_else(|| invalid("invalid partition path"))?;
                let date = chrono::NaiveDate::parse_from_str(text, "%Y/%j").map_err(invalid)?;
                let ms = date
                    .and_hms_opt(0, 0, 0)
                    .unwrap()
                    .and_utc()
                    .timestamp_millis();
                if ms < 0 {
                    return Err(invalid("negative partition time"));
                }
                (ms as u64, ms as u64 + DAY_MILLIS, None)
            };
            if from < end && to > start {
                out.push(Partition {
                    version: version.into(),
                    path,
                    start: from,
                    end: to,
                    owner,
                })
            }
        }
    }
    Ok(())
}
use std::os::unix::fs::FileExt;
