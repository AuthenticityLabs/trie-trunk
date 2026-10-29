package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const FinalizationCycle = 5 * time.Minute
const FinalizationCycles = 3

// Run promotes the mature tail even when there are no new requests. Flush and
// Close use the same gate; neither can force young events into permanent rows.
func (s *Store) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("positive maintenance interval required")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if e := s.Flush(); e != nil {
				return e
			}
		}
	}
}

func (s *Store) swapPath(b *branchStore) string {
	// Independent of retention generations: the unfinalized tail must survive
	// replacement of the finalized prefix.
	return filepath.Join(s.dir, b.branch.Prefix()+".seed")
}

func (s *Store) openSwap(b *branchStore) error {
	var e error
	b.swap, e = openFile(s.swapPath(b), "PILSEED0", s.layout.Digest, [16]byte{}, 0)
	if e != nil {
		return e
	}
	first := true
	// Old files remain readable. New appends may never precede their greatest
	// committed timestamp, even after a crash between commit and swap cleanup.
	for _, run := range b.runs {
		if run.MaxMS > b.finalizedBefore {
			b.finalizedBefore = run.MaxMS
		}
	}
	e = visitRecords(b.swap, MaxFrame, s.memory, func(raw []byte, input *nativeBuffer) error {
		if first {
			first = false
			if len(raw) != 8 {
				return fmt.Errorf("invalid swap watermark")
			}
			cutoff := int64(order.Uint64(raw))
			if cutoff < 0 {
				return fmt.Errorf("invalid swap cutoff")
			}
			if cutoff > b.finalizedBefore {
				b.finalizedBefore = cutoff
			}
			return nil
		}
		var f Frame
		if e := s.layout.Scan(raw, &f); e != nil {
			return e
		}
		id, e := eventID(f.Values[s.layout.Event])
		if e != nil {
			return e
		}
		if eventTime(id[:]) < b.ingressBoundary() {
			return nil
		}
		if prior := b.live.get(id[:], true, s.config.Now()); prior != nil {
			if !s.layout.sameEvent(prior, raw) {
				return fmt.Errorf("conflicting staged event_id")
			}
			return nil
		}
		// A partially promoted batch can share a timestamp with the committed tail.
		if eventTime(id[:]) < b.finalizedBefore {
			return fmt.Errorf("staged event precedes finalized boundary")
		}
		if b.branch == Write && s.config.ObjectIndex {
			if _, e := s.checkCollectionWrite(b, f.Values); e != nil {
				return e
			}
		}
		if e := s.invalidateReceived(b.branch, raw); e != nil {
			return e
		}
		raw, e = b.own(s, id, raw, input)
		if e != nil {
			return e
		}
		return s.rememberPendingValues(b, pending{id, raw}, f.Values)
	})
	if e != nil {
		return e
	}
	if first {
		var cutoff [8]byte
		order.PutUint64(cutoff[:], uint64(b.finalizedBefore))
		if e = appendRecord(b.swap, cutoff[:]); e != nil {
			return e
		}
	}
	return b.swap.Sync()
}

func (s *Store) flush(b *branchStore) error {
	if s.sealing != nil {
		return nil
	}
	b.live.prune(s.config.Now())
	defer b.pruneMemory(s.config.Now())
	mature := s.config.Now().Truncate(FinalizationCycle).Add(-FinalizationCycles * FinalizationCycle)
	boundary, _ := semanticBounds(mature, 1, s.location)
	cutoff := boundary.UnixMilli()
	if cutoff < b.finalizedBefore {
		cutoff = b.finalizedBefore
	}
	count := 0
	// The native time ordering selects one hour at a time. Backfill no longer
	// copies/sorts the entire pending set before processing its first hour.
	for {
		first := b.live.firstPending()
		if first >= cutoff {
			break
		}
		start, end := semanticBounds(time.UnixMilli(first), 1, s.location)
		batch := b.pendingWindow(nil, start, end)
		if len(batch) == 0 {
			return fmt.Errorf("native pending hour is empty")
		}
		if e := s.commitBatch(b, batch); e != nil {
			return e
		}
		count += len(batch)
	}
	if count == 0 && cutoff == b.finalizedBefore {
		if !b.timeDirty {
			return s.packSummaryDays(b)
		}
		if e := s.publishTime(b); e != nil {
			return e
		}
		return s.packSummaryDays(b)
	}
	// Publish the cutoff and surviving tail atomically only after every mature
	// commit is durable. Recovery deduplicates commits left in the previous swap.
	if e := s.rewriteSwap(b, cutoff, b.pendingWindow(nil, time.UnixMilli(cutoff), time.UnixMilli(1<<48))); e != nil {
		return e
	}
	b.finalizedBefore = cutoff
	if e := s.publishTime(b); e != nil {
		return e
	}
	return s.packSummaryDays(b)
}

func (s *Store) rewriteSwap(b *branchStore, cutoff int64, tail []pending) error {
	tmp, e := os.CreateTemp(s.dir, ".seed-")
	if e != nil {
		return e
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	next, e := openFile(path, "PILSEED0", s.layout.Digest, [16]byte{}, 0)
	if e != nil {
		return e
	}
	published := false
	defer func() {
		if !published {
			next.Close()
		}
	}()
	var raw [8]byte
	order.PutUint64(raw[:], uint64(cutoff))
	if e = appendRecord(next, raw[:]); e != nil {
		return e
	}
	for _, p := range tail {
		if e = appendRecord(next, p.frame); e != nil {
			return e
		}
	}
	if e = next.Sync(); e != nil {
		return e
	}
	if e = os.Rename(path, s.swapPath(b)); e != nil {
		return e
	}
	old := b.swap
	b.swap = next
	published = true
	if e = old.Close(); e != nil {
		return e
	}
	return syncDir(s.dir)
}

// An error or revision-less collection write invalidates replay caches at receipt.
// Replaying this after a kill may conservatively invalidate twice, never clear
// uncertainty or leave a cache valid after an acknowledged error.
func (s *Store) invalidateReceived(branch Branch, raw []byte) error {
	policy := s.policyFor(branch)
	if (branch != Error && !(branch == Write && s.config.ObjectIndex)) || policy.CacheInvalidation != nil && !*policy.CacheInvalidation {
		return nil
	}
	var f Frame
	if e := s.layout.Scan(raw, &f); e != nil {
		return e
	}
	if branch == Write && f.Values[s.layout.Revision].State != Absent {
		return nil
	}
	if s.layout.Object >= 0 && f.Values[s.layout.Object].State >= Direct {
		return s.invalidate([][]byte{s.layout.NormalizeIntegerWidth(s.layout.Object, f.Values[s.layout.Object]).Bytes}, false)
	}
	return s.invalidate(nil, true)
}

// A kill before rename may leave an unpublished copy. The original swap is
// still authoritative. This runs only while holding the cache writer lease.
func (s *Store) removeSwapTemps() error {
	entries, e := os.ReadDir(s.dir)
	if e != nil {
		return e
	}
	changed := false
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".seed-") {
			if e = os.Remove(filepath.Join(s.dir, entry.Name())); e != nil {
				return e
			}
			changed = true
		}
	}
	if changed {
		return syncDir(s.dir)
	}
	return nil
}
