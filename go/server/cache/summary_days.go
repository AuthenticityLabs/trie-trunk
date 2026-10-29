package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	pilcodec "github.com/AuthenticityLabs/pill-packer/go/codec"
	"os"
	"path/filepath"
	"sort"
)

const summaryDayMS int64 = 24 * TimeHourMS
const maxSummaryDay = 128 << 20

func isSummaryPAC(raw []byte) bool {
	if len(raw) < pilcodec.PACHeaderSize {
		return false
	}
	return int(order.Uint32(raw[:4])) == len(raw) && order.Uint32(raw[4:8]) <= pilcodec.PACGzip
}

type summaryDayLocation struct {
	Offset, Length uint64
	From, To       int64
	Key            string
	Rollup         CalendarRollup
}

func summaryDay(ms int64) int64 { return ms / summaryDayMS * summaryDayMS }

// Daily summary packets are standard PIL PACs. Their members are ordered hourly
// summaries, independent of physical event-row addresses.
func encodeSummaryDay(day, from, to int64, sums []Summary, compressed bool) ([]byte, error) {
	return encodeSummaryDayLevel(day, from, to, sums, compressed, (SummaryPolicy{}).level())
}
func encodeSummaryDayLevel(day, from, to int64, sums []Summary, compressed bool, level int) ([]byte, error) {
	return encodeSummaryPAC(day, from, to, sums, compressed, level)
}
func encodeSummaryDayColumns(day, from, to int64, sums []Summary, compressed bool, level int, _ bool) ([]byte, error) {
	return encodeSummaryDayLevel(day, from, to, sums, compressed, level)
}
func encodeSummaryDayFiltered(day, from, to int64, sums []Summary, compressed bool, level int, _, _ bool) ([]byte, error) {
	return encodeSummaryDayLevel(day, from, to, sums, compressed, level)
}
func decodeSummaryDay(raw []byte) (day, from, to int64, sums []Summary, err error) {
	return decodeSummaryPAC(raw)
}

// Summary boundaries are certified against canonical committed event ranges,
// rather than physical .sum byte positions that change with daily packing.
func (s *Store) loadSummaries(b *branchStore) error {
	if b.sum == nil {
		return nil
	}
	b.summaryDays = map[int64]summaryDayLocation{}
	b.summaries = nil
	b.summaryPackedThrough = 0
	b.calendarRollups = nil
	var rollupRaw []byte
	next := 0
	trim := uint64(headerSize)
	at := uint64(headerSize)
	uncommitted := false
	err := visitRecords(b.sum, maxSummaryDay+32, nil, func(raw []byte, _ *nativeBuffer) error {
		if len(raw) >= 8 && string(raw[:8]) == "SUMROLL0" {
			if rollupRaw != nil || len(raw) < 24 || uncommitted {
				return fmt.Errorf("invalid calendar rollup table")
			}
			rollupRaw = bytes.Clone(raw)
			at += 8 + uint64(len(raw))
			trim = at
			return nil
		}
		var batch []Summary
		var day, from, to int64
		daily := isSummaryPAC(raw)
		if daily {
			var e error
			var sealed bool
			day, from, to, batch, sealed, e = decodeSummaryPACInfo(raw)
			if e != nil {
				return e
			}
			if !sealed {
				return fmt.Errorf("provisional day persisted")
			}
		} else {
			var sum Summary
			if e := decodeSummary(raw, &sum); e != nil {
				return e
			}
			batch = []Summary{sum}
		}
		if uncommitted {
			return fmt.Errorf("summary after uncommitted tail")
		}
		for _, sum := range batch {
			if next >= len(b.runs) {
				if daily {
					return fmt.Errorf("uncommitted daily summary")
				}
				uncommitted = true
				return nil
			}
			run := b.runs[next]
			if sum.From != run.Start || sum.Through != run.End {
				return fmt.Errorf("summary/source mismatch")
			}
			b.summaries = append(b.summaries, sum)
			next++
		}
		if daily {
			if _, exists := b.summaryDays[day]; exists {
				return fmt.Errorf("duplicate daily summary")
			}
			totals, e := dailyRollup(batch)
			if e != nil {
				return e
			}
			b.summaryDays[day] = summaryDayLocation{at + 8, uint64(len(raw)), from, to, summaryDigest(raw), totals}
			b.summaryPackedThrough = max(b.summaryPackedThrough, day+summaryDayMS)
		}
		at += 8 + uint64(len(raw))
		trim = at
		return nil
	})
	if err != nil {
		return err
	}
	if rollupRaw != nil {
		from, to := int64(order.Uint64(rollupRaw[8:])), int64(order.Uint64(rollupRaw[16:]))
		rowsInFile := (len(rollupRaw) - 24) / calendarRollupRowSize
		if (len(rollupRaw)-24)%calendarRollupRowSize != 0 || rowsInFile == 0 || from < 0 || from%TimeHourMS != 0 || to%summaryDayMS != 0 || from >= to || to-from > int64(rowsInFile)*7*summaryDayMS {
			return fmt.Errorf("invalid calendar coverage")
		}
		for _, d := range b.summaryDays {
			if d.From < from || d.To > to {
				return fmt.Errorf("calendar coverage excludes daily summary")
			}
		}
		rows, e := calendarRollups(b.summaryDays, from, to)
		if e != nil {
			return e
		}
		if !bytes.Equal(rollupRaw, encodeCalendarRollups(from, to, rows)) {
			return fmt.Errorf("calendar rollup mismatch")
		}
		b.calendarRollups = rows
		b.summaryPackedThrough = to
	} else if len(b.summaryDays) > 0 {
		return fmt.Errorf("missing calendar rollups")
	}
	if next != len(b.runs) {
		return fmt.Errorf("missing committed summaries")
	}
	if err = b.sum.Truncate(int64(trim)); err != nil {
		return err
	}
	b.sumEnd = trim
	return nil
}

// Daily compaction copies older sealed PACs verbatim. It does not decode events
// or recompress older days. The atomic replacement copies the retained summary
// file once per daily rollover; measure that write cost.
func (s *Store) packSummaryDays(b *branchStore) error {
	if b.sum == nil || len(b.summaries) == 0 {
		return nil
	}
	cutoff := summaryDay(utcHour(b.finalizedBefore))
	if cutoff <= b.summaryPackedThrough {
		return nil
	}
	first := utcHour(b.summaries[0].Columns.Period[0])
	if b.timeAnchor != nil {
		first = max(first, b.timeAnchor.First)
	}
	eligible := len(b.summaryDays) > 0
	for _, sum := range b.summaries {
		day := summaryDay(sum.Columns.Period[0])
		if day < cutoff {
			if _, ok := b.summaryDays[day]; !ok {
				eligible = true
				break
			}
		}
	}
	if !eligible {
		b.summaryPackedThrough = cutoff
		return nil
	}
	// Match hourly sealing: one scheduled worker, ingestion continues into the
	// seed while immutable summary chunks are packed. Retention's unpublished
	// replacement branch is already exclusively owned and needs no second gate.
	level := s.policyFor(b.branch).Summary.level()
	background := s.branches[b.branch] == b
	locked := true
	var release func()
	if background {
		if s.sealing != nil {
			return nil
		}
		done := make(chan struct{})
		s.sealing = done
		s.mu.Unlock()
		locked = false
		release = s.config.Sealer.acquire()
		defer func() {
			if !locked {
				release()
				s.mu.Lock()
			}
			s.sealing = nil
			close(done)
		}()
	}
	var header [headerSize]byte
	if _, e := b.sum.ReadAt(header[:], 0); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(s.dir, ".summary-day-")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	if _, e = tmp.Write(header[:]); e != nil {
		return e
	}
	grouped := map[int64][]Summary{}
	var days []int64
	for _, sum := range b.summaries {
		day := summaryDay(sum.Columns.Period[0])
		if _, ok := grouped[day]; !ok {
			days = append(days, day)
		}
		grouped[day] = append(grouped[day], sum)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	locations := map[int64]summaryDayLocation{}
	position := uint64(headerSize)
	for _, day := range days {
		if day < cutoff {
			var raw []byte
			if old, ok := b.summaryDays[day]; ok {
				raw, e = readBytes(b.sum, old.Offset, old.Offset+old.Length)
			} else {
				raw, e = encodeSummaryDayLevel(day, max(day, first), day+summaryDayMS, grouped[day], true, level)
			}
			if e != nil {
				return e
			}
			if e = appendBoundedRecord(tmp, raw, maxSummaryDay+32); e != nil {
				return e
			}
			totals := b.summaryDays[day].Rollup
			if _, exists := b.summaryDays[day]; !exists {
				totals, e = dailyRollup(grouped[day])
				if e != nil {
					return e
				}
			}
			locations[day] = summaryDayLocation{position + 8, uint64(len(raw)), max(day, first), day + summaryDayMS, summaryDigest(raw), totals}
			position += 8 + uint64(len(raw))
		} else {
			for _, sum := range grouped[day] {
				raw, e := encodeSummary(sum)
				if e != nil {
					return e
				}
				if e = appendRecord(tmp, raw); e != nil {
					return e
				}
				position += 8 + uint64(len(raw))
			}
		}
	}
	rollups, e := calendarRollups(locations, first, cutoff)
	if e != nil {
		return e
	}
	rollupRaw := encodeCalendarRollups(first, cutoff, rollups)
	if e = appendBoundedRecord(tmp, rollupRaw, maxSummaryDay+32); e != nil {
		return e
	}
	position += 8 + uint64(len(rollupRaw))
	if e = tmp.Sync(); e != nil {
		return e
	}
	if e = s.sealProbe("summary-day-ready"); e != nil {
		return e
	}
	if background {
		release()
		s.mu.Lock()
		locked = true
		if e = s.check(); e != nil {
			return e
		}
	}
	path := b.sum.Name()
	if e = os.Rename(name, path); e != nil {
		return e
	}
	old := b.sum
	b.sum, e = os.OpenFile(path, os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	old.Close()
	if e = syncDir(filepath.Dir(path)); e != nil {
		return e
	}
	b.summaryDays = locations
	b.calendarRollups = rollups
	b.sumEnd = position
	b.summaryPackedThrough = cutoff
	return s.sealProbe("summary-day-published")
}

func summaryDigest(raw []byte) string { hash := sha256.Sum256(raw); return hex.EncodeToString(hash[:]) }
