use super::{Command, Error, Result, Stats, Uuid};

pub const PAGE: usize = 4096;
pub const HEADER: usize = 37;
pub const UNITS: usize = 123;
pub const UNIT: usize = 32;
pub const MAX_REVISIONS: usize = 1 + (UNITS - 1) * 4;
pub const EMPTY: u8 = 128;

#[derive(Clone)]
pub struct Page(pub [u8; PAGE]);

impl Default for Page {
    fn default() -> Self {
        let mut p = Self([0; PAGE]);
        p.0[..4].copy_from_slice(b"OBJ0");
        p.0[29] = 1;
        p.0[30] = 4;
        p.0[31] = 4;
        p.0[HEADER..HEADER + UNITS].fill(EMPTY);
        p
    }
}

impl Page {
    pub fn used(&self) -> usize {
        self.0[28] as usize
    }
    fn set_used(&mut self, n: usize) {
        self.0[28] = n as u8;
    }
    pub fn pointer(&self, i: usize) -> u8 {
        self.0[HEADER + i]
    }
    pub fn start(&self, i: usize) -> bool {
        self.pointer(i) & 128 == 0
    }
    fn at(&self, i: usize) -> usize {
        (self.pointer(i) as usize & 127) * UNIT
    }
    pub fn key(&self, i: usize) -> Uuid {
        self.0[self.at(i)..self.at(i) + 16].try_into().unwrap()
    }
    pub fn first(&self) -> Uuid {
        self.key(0)
    }
    pub fn end(&self, i: usize) -> usize {
        let mut n = i + 1;
        while n < self.used() && !self.start(n) {
            n += 1;
        }
        n
    }
    pub fn capacity(&self, i: usize) -> usize {
        1 + (self.end(i) - i - 1) * 4
    }
    pub fn objects(&self) -> usize {
        (0..self.used()).filter(|&i| self.start(i)).count()
    }
    pub fn base(&self, i: usize) -> u16 {
        self.u16(self.at(i) + 16)
    }
    fn head(&self, i: usize) -> usize {
        self.u16(self.at(i) + 18) as usize
    }
    fn u16(&self, at: usize) -> u16 {
        u16::from_le_bytes(self.0[at..at + 2].try_into().unwrap())
    }
    fn put16(&mut self, at: usize, n: u16) {
        self.0[at..at + 2].copy_from_slice(&n.to_le_bytes());
    }
    fn ref_at(&self, i: usize, n: usize) -> usize {
        if n == 0 {
            self.at(i) + 24
        } else {
            self.at(i + 1 + (n - 1) / 4) + ((n - 1) % 4) * 8
        }
    }
    fn physical_ref(&self, i: usize, n: usize) -> u64 {
        let at = self.ref_at(i, n);
        ((u32::from_le_bytes(self.0[at..at+4].try_into().unwrap()) as u64) << 32) | u32::from_le_bytes(self.0[at+4..at+8].try_into().unwrap()) as u64
    }
    fn put_ref(&mut self, i: usize, n: usize, v: u64) {
        let at = self.ref_at(i, n);
        self.0[at..at+4].copy_from_slice(&((v >> 32) as u32).to_le_bytes());
        self.0[at+4..at+8].copy_from_slice(&(v as u32).to_le_bytes());
    }
    pub fn reference(&self, i: usize, revision: u16) -> Option<u64> {
        let delta = revision.checked_sub(self.base(i))? as usize;
        let capacity = self.capacity(i);
        if delta >= capacity {
            return None;
        }
        let v = self.physical_ref(i, (self.head(i) + delta) % capacity);
        (v != 0).then_some(v)
    }
    pub fn latest(&self, i: usize) -> Option<(u16, u64)> {
        let capacity = self.capacity(i);
        let base = self.base(i);
        let head = self.head(i);
        for n in (0..capacity).rev() {
            let Some(revision) = base.checked_add(n as u16) else {
                continue;
            };
            let v = self.physical_ref(i, (head + n) % capacity);
            if v != 0 {
                return Some((revision, v));
            }
        }
        None
    }
    pub fn history(&self, i: usize, mut visit: impl FnMut(u16, u64)) {
        let capacity = self.capacity(i);
        let base = self.base(i);
        let head = self.head(i);
        for n in 0..capacity {
            let Some(revision) = base.checked_add(n as u16) else {
                break;
            };
            let v = self.physical_ref(i, (head + n) % capacity);
            if v != 0 {
                visit(revision, v);
            }
        }
    }

    /// Probe either direction, then the remaining half of the bounded interval.
    /// Bounds are updated using the actual start found, never the midpoint.
    pub fn find(&self, key: &Uuid, stats: &mut Stats) -> Option<usize> {
        let (mut lo, mut hi, mut right) = (0usize, self.used(), true);
        while lo < hi {
            let mid = lo + (hi - lo) / 2;
            let mut found = None;
            if right {
                for i in mid..hi {
                    stats.pointer_probes += 1;
                    if self.start(i) {
                        found = Some(i);
                        break;
                    }
                }
                if found.is_none() {
                    for i in (lo..mid).rev() {
                        stats.pointer_probes += 1;
                        if self.start(i) {
                            found = Some(i);
                            break;
                        }
                    }
                }
            } else {
                for i in (lo..=mid).rev() {
                    stats.pointer_probes += 1;
                    if self.start(i) {
                        found = Some(i);
                        break;
                    }
                }
                if found.is_none() {
                    for i in mid + 1..hi {
                        stats.pointer_probes += 1;
                        if self.start(i) {
                            found = Some(i);
                            break;
                        }
                    }
                }
            }
            let i = found?;
            stats.uuid_comparisons += 1;
            match self.key(i).cmp(key) {
                std::cmp::Ordering::Equal => return Some(i),
                std::cmp::Ordering::Less => lo = i + 1,
                std::cmp::Ordering::Greater => hi = i,
            }
            right = !right;
        }
        None
    }

    fn free_unit(&self) -> Option<u8> {
        let mut used = 0u128;
        for i in 0..self.used() {
            used |= 1u128 << (self.pointer(i) & 127);
        }
        (5..128)
            .find(|&n| used & (1u128 << n) == 0)
            .map(|n| n as u8)
    }
    fn add_unit(&mut self, pos: usize, extension: bool) {
        let physical = self.free_unit().unwrap();
        let n = self.used();
        self.0
            .copy_within(HEADER + pos..HEADER + n, HEADER + pos + 1);
        self.0[HEADER + pos] = physical | if extension { 128 } else { 0 };
        self.0[physical as usize * UNIT..(physical as usize + 1) * UNIT].fill(0);
        self.set_used(n + 1);
    }
    pub fn remove(&mut self, i: usize) {
        let end = self.end(i);
        let n = self.used();
        for p in i..end {
            let at = self.at(p);
            self.0[at..at + UNIT].fill(0);
        }
        self.0.copy_within(HEADER + end..HEADER + n, HEADER + i);
        self.0[HEADER + n - (end - i)..HEADER + n].fill(EMPTY);
        self.set_used(n - (end - i));
    }
    pub fn copy_group(&self, i: usize, target: &mut Self) -> Result<()> {
        let end = self.end(i);
        let n = target.used();
        if n + end - i > UNITS {
            return Err(Error::PageFull);
        }
        for p in i..end {
            target.add_unit(target.used(), p != i);
            let at = target.at(target.used() - 1);
            let from = self.at(p);
            target.0[at..at + UNIT].copy_from_slice(&self.0[from..from + UNIT]);
        }
        Ok(())
    }

    pub fn apply(&mut self, cmd: &Command, keep: Option<u16>) -> Result<()> {
        if cmd.address == 0 && !cmd.retire {
            return Err(Error::ZeroAddress);
        }
        let existing = self.find(&cmd.object, &mut Stats::default());
        if cmd.retire {
            if let Some(i) = existing {
                self.retire(i, cmd.revision);
            }
            return Ok(());
        }
        if let Some(i) = existing {
            if let Some(v) = self.reference(i, cmd.revision) {
                return if v == cmd.address {
                    Ok(())
                } else {
                    Err(Error::DuplicateRevision)
                };
            }
            if cmd.revision < self.base(i) {
                return Err(Error::ExpiredRevision);
            }
        }
        let old_base = existing.map(|i| self.base(i)).unwrap_or(cmd.revision);
        let newest = existing
            .and_then(|i| self.latest(i))
            .map(|r| r.0)
            .unwrap_or(cmd.revision)
            .max(cmd.revision);
        let base = keep
            .map(|k| old_base.max(newest.saturating_sub(k - 1)))
            .unwrap_or(old_base);
        if cmd.revision < base {
            return Err(Error::ExpiredRevision);
        }
        let needed = (newest as usize - base as usize) + 1;
        if needed > MAX_REVISIONS {
            return Err(Error::ObjectFull);
        }
        let wanted = 1 + needed.saturating_sub(1).div_ceil(4);
        let old_units = existing.map(|i| self.end(i) - i).unwrap_or(0);
        if self.used() + wanted.saturating_sub(old_units) > UNITS {
            return Err(Error::PageFull);
        }
        if let Some(i) = existing {
            // Stable base/capacity needs one reference write, not a history
            // rebuild. An unrotated history can also grow without relocation.
            if base == old_base && (wanted <= old_units || self.head(i) == 0) {
                while self.end(i) - i < wanted {
                    self.add_unit(self.end(i), true);
                }
                let position = (self.head(i) + (cmd.revision - base) as usize) % self.capacity(i);
                self.put_ref(i, position, cmd.address);
                return Ok(());
            }
        } else {
            let pos = (0..self.used())
                .find(|&p| self.start(p) && self.key(p) > cmd.object)
                .unwrap_or(self.used());
            self.add_unit(pos, false);
            let at = self.at(pos);
            self.0[at..at + 16].copy_from_slice(&cmd.object);
            self.put16(at + 16, base);
            self.put_ref(pos, 0, cmd.address);
            return Ok(());
        }
        let mut old = [0u64; MAX_REVISIONS];
        if let Some(i) = existing {
            self.history(i, |r, v| {
                if r >= base && (r as usize - base as usize) < needed {
                    old[r as usize - base as usize] = v;
                }
            });
        }
        old[cmd.revision as usize - base as usize] = cmd.address;
        let i = existing.expect("new parents return above");
        while self.end(i) - i < wanted {
            self.add_unit(self.end(i), true);
        }
        let capacity = self.capacity(i);
        let old_head = self.head(i);
        let head = if old_units == self.end(i) - i {
            (old_head + (base - old_base) as usize) % capacity
        } else {
            0
        };
        let at = self.at(i);
        self.put16(at + 16, base);
        self.put16(at + 18, head as u16);
        for (j, value) in old.iter().enumerate().take(capacity) {
            self.put_ref(i, (head + j) % capacity, *value);
        }
        Ok(())
    }
    fn retire(&mut self, i: usize, cutoff: u16) {
        let old_base = self.base(i);
        if cutoff <= old_base {
            return;
        }
        let Some((newest, _)) = self.latest(i) else {
            self.remove(i);
            return;
        };
        if cutoff > newest {
            self.remove(i);
            return;
        }
        let mut retained = [0u64; MAX_REVISIONS];
        self.history(i, |r, v| {
            if r >= cutoff {
                retained[(r - cutoff) as usize] = v;
            }
        });
        let wanted = 1 + ((newest - cutoff) as usize).div_ceil(4);
        let end = self.end(i);
        let removed = end - i - wanted;
        let n = self.used();
        if removed > 0 {
            for p in i + wanted..end {
                let at = self.at(p);
                self.0[at..at + UNIT].fill(0);
            }
            self.0
                .copy_within(HEADER + end..HEADER + n, HEADER + i + wanted);
            self.0[HEADER + n - removed..HEADER + n].fill(EMPTY);
            self.set_used(n - removed);
        }
        let at = self.at(i);
        self.put16(at + 16, cutoff);
        self.put16(at + 18, 0);
        for (j, v) in retained.iter().enumerate().take(self.capacity(i)) {
            self.put_ref(i, j, *v);
        }
    }
    pub fn seal(&mut self) {
        self.0[24..28].fill(0);
        let crc = crc32c::crc32c(&self.0);
        self.0[24..28].copy_from_slice(&crc.to_le_bytes());
    }
    pub fn verify_checksum(&self) -> Result<()> {
        let mut copy = self.clone();
        copy.0[24..28].fill(0);
        if self.0[..4] != *b"OBJ0"
            || self.0[29] != 1
            || self.used() > UNITS
            || crc32c::crc32c(&copy.0) != u32::from_le_bytes(self.0[24..28].try_into().unwrap())
        {
            return Err(Error::Corrupt("page header/checksum"));
        }
        Ok(())
    }
    pub fn validate(&self) -> Result<()> {
        self.verify_checksum()?;
        if self.0[32..37].iter().any(|&v| v != 0)
            || self.0[HEADER + self.used()..HEADER + UNITS]
                .iter()
                .any(|&v| v != EMPTY)
        {
            return Err(Error::Corrupt("reserved/header pointer bytes"));
        }
        let mut occupied = 0u128;
        let mut last = None;
        for i in 0..self.used() {
            let p = self.pointer(i) & 127;
            if p < 5 || occupied & (1u128 << p) != 0 || (i == 0 && !self.start(i)) {
                return Err(Error::Corrupt("physical pointer"));
            }
            occupied |= 1u128 << p;
            if self.start(i) {
                let key = self.key(i);
                if last.is_some_and(|k| k >= key) {
                    return Err(Error::Corrupt("object ordering"));
                }
                last = Some(key);
            }
        }
        for i in 0..self.used() {
            if self.start(i) {
                let at = self.at(i);
                if self.head(i) >= self.capacity(i)
                    || self.0[at + 20..at + 24].iter().any(|&v| v != 0)
                {
                    return Err(Error::Corrupt("parent metadata"));
                }
                for j in 0..self.capacity(i) {
                    if self.base(i).checked_add(j as u16).is_none()
                        && self.physical_ref(i, (self.head(i) + j) % self.capacity(i)) != 0
                    {
                        return Err(Error::Corrupt("revision overflow"));
                    }
                }
            }
        }
        Ok(())
    }
}
