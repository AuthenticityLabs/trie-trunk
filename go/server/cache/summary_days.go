package cache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const summaryDayMS int64 = 24 * TimeHourMS
const summaryDayHeader = 32
const summaryDayDirectory = 16 + 24*12 + summaryRollupSize + summaryEncodingSize
const maxSummaryDay = 128 << 20

type summaryDayLocation struct {
	Offset, Length uint64
	From, To       int64
	Key            string
	Rollup         CalendarRollup
}

func summaryDay(ms int64) int64 { return ms / summaryDayMS * summaryDayMS }

// The same logical hour ordinal used by .tri/.idx chooses a day and a slot
// within its fixed 24-entry directory. Physical event row addresses are not
// summary row addresses. Compressed daily bodies are transmitted unchanged.
func encodeSummaryDay(day, from, to int64, sums []Summary, compressed bool) ([]byte, error) {
	return encodeSummaryDayLevel(day, from, to, sums, compressed, (SummaryPolicy{}).level())
}
func encodeSummaryDayLevel(day, from, to int64, sums []Summary, compressed bool, level int) ([]byte, error) {
	return encodeSummaryDayColumns(day, from, to, sums, compressed, level, compressed)
}
func encodeSummaryDayColumns(day, from, to int64, sums []Summary, compressed bool, level int, based bool) ([]byte, error) {
	return encodeSummaryDayFiltered(day, from, to, sums, compressed, level, based, compressed)
}
func encodeSummaryDayFiltered(day, from, to int64, sums []Summary, compressed bool, level int, based, shuffled bool) ([]byte, error) {
	if level < 1 || level > 9 {
		return nil, fmt.Errorf("invalid summary gzip level")
	}

	if day < 0 || day%summaryDayMS != 0 || from < day || to > day+summaryDayMS || from > to || from%TimeHourMS != 0 || to%TimeHourMS != 0 {
		return nil, fmt.Errorf("invalid summary day coverage")
	}
	enc, e := chooseSummaryEncoding(sums, based)
	if e != nil {
		return nil, e
	}
	totals, errors, e := summaryTotals(sums)
	if e != nil {
		return nil, e
	}
	var chunks [24][]byte
	size := summaryDayDirectory
	for _, s := range sums {
		if s.Columns.Len() == 0 {
			return nil, fmt.Errorf("empty summary chunk")
		}
		hour := utcHour(s.Columns.Period[0])
		if hour < from || hour >= to {
			return nil, fmt.Errorf("summary outside day coverage")
		}
		slot := int((hour - day) / TimeHourMS)
		if chunks[slot] != nil {
			return nil, fmt.Errorf("duplicate summary hour")
		}
		raw, e := encodeSummaryColumns(s, &enc)
		if e != nil {
			return nil, e
		}
		if size+len(raw) > maxSummaryDay {
			return nil, fmt.Errorf("summary day exceeds limit")
		}
		if shuffled {
			shuffleSummary(raw, enc, false)
		}
		chunks[slot] = raw
		size += len(raw)
	}
	storage := make([]byte, summaryDayHeader+size)
	body := storage[summaryDayHeader:]
	order.PutUint64(body, uint64(from))
	order.PutUint64(body[8:], uint64(to))
	putSummaryTotals(body[304:], totals, errors)
	enc.put(body[304+summaryRollupSize:])
	cursor := summaryDayDirectory
	for h, raw := range chunks {
		at := day + int64(h)*TimeHourMS
		d := 16 + h*12
		if at >= from && at < to {
			order.PutUint32(body[d+8:], 1)
		}
		if raw == nil {
			continue
		}
		order.PutUint32(body[d:], uint32(cursor))
		order.PutUint32(body[d+4:], uint32(len(raw)))
		copy(body[cursor:], raw)
		cursor += len(raw)
	}
	encoded := body
	codec := byte(0)
	if compressed {
		var b bytes.Buffer
		gz, _ := gzip.NewWriterLevel(&b, level)
		if _, e := gz.Write(body); e != nil {
			return nil, e
		}
		if e := gz.Close(); e != nil {
			return nil, e
		}
		encoded = b.Bytes()
		codec = 1
	}
	raw := storage
	if compressed {
		raw = make([]byte, summaryDayHeader+len(encoded))
		copy(raw[32:], encoded)
	}
	copy(raw, "SUMDAY00")
	raw[8] = codec
	if shuffled {
		raw[10] = 1
	}
	if compressed {
		raw[9] = 1
	}
	order.PutUint32(raw[12:], uint32(len(raw)))
	order.PutUint32(raw[16:], uint32(len(body)))
	order.PutUint32(raw[20:], crc32.Checksum(body, checksum))
	order.PutUint64(raw[24:], uint64(day))
	return raw, nil
}
func decodeSummaryDay(raw []byte) (day, from, to int64, sums []Summary, err error) {
	bad := func() error { return fmt.Errorf("invalid daily summary") }
	if len(raw) < 32 || len(raw) > maxSummaryDay+32 || string(raw[:8]) != "SUMDAY00" || int(order.Uint32(raw[12:])) != len(raw) || raw[8] > 1 || raw[9] > 1 || raw[10] > 1 || raw[11] != 0 {
		return 0, 0, 0, nil, bad()
	}
	n := order.Uint32(raw[16:])
	if n < summaryDayDirectory || n > maxSummaryDay {
		return 0, 0, 0, nil, bad()
	}
	body := raw[32:]
	if raw[8] == 1 {
		gz, e := gzip.NewReader(bytes.NewReader(body))
		if e != nil {
			return 0, 0, 0, nil, e
		}
		body = make([]byte, int(n))
		_, e = io.ReadFull(gz, body)
		if e == nil {
			var extra [1]byte
			var got int
			got, e = gz.Read(extra[:])
			if got != 0 || e != io.EOF {
				e = bad()
			} else {
				e = nil
			}
		}
		closeErr := gz.Close()
		if e != nil {
			return 0, 0, 0, nil, e
		}
		if closeErr != nil {
			return 0, 0, 0, nil, closeErr
		}
	}
	if len(body) != int(n) || crc32.Checksum(body, checksum) != order.Uint32(raw[20:]) {
		return 0, 0, 0, nil, bad()
	}
	if raw[10] == 1 && raw[8] == 0 {
		body = bytes.Clone(body)
	}
	day = int64(order.Uint64(raw[24:]))
	from = int64(order.Uint64(body))
	to = int64(order.Uint64(body[8:]))
	if day < 0 || day%summaryDayMS != 0 || from < day || to > day+summaryDayMS || from > to || from%TimeHourMS != 0 || to%TimeHourMS != 0 {
		return 0, 0, 0, nil, bad()
	}
	enc, e := readSummaryEncoding(body[304+summaryRollupSize:])
	if e != nil {
		return 0, 0, 0, nil, e
	}
	cursor := uint64(summaryDayDirectory)
	for h := 0; h < 24; h++ {
		d := body[16+h*12:]
		a, z, state := uint64(order.Uint32(d)), uint64(order.Uint32(d[4:])), order.Uint32(d[8:])
		hour := day + int64(h)*TimeHourMS
		known := hour >= from && hour < to
		if (known && state != 1) || (!known && state != 0) || z == 0 && a != 0 || !known && z != 0 {
			return 0, 0, 0, nil, bad()
		}
		if z == 0 {
			continue
		}
		if a != cursor || z > uint64(len(body))-cursor {
			return 0, 0, 0, nil, bad()
		}
		var s Summary
		if raw[10] == 1 {
			if e := validateShuffledSummary(body[a:a+z], enc); e != nil {
				return 0, 0, 0, nil, e
			}
			shuffleSummary(body[a:a+z], enc, true)
		}
		if e := decodeSummaryColumns(body[a:a+z], &s, &enc); e != nil {
			return 0, 0, 0, nil, e
		}
		if utcHour(s.Columns.Period[0]) != hour {
			return 0, 0, 0, nil, bad()
		}
		sums = append(sums, s)
		cursor += z
	}
	totals, errors, e := summaryTotals(sums)
	if e != nil {
		return 0, 0, 0, nil, e
	}
	var expected [summaryRollupSize]byte
	putSummaryTotals(expected[:], totals, errors)
	if !bytes.Equal(expected[:], body[304:304+summaryRollupSize]) {
		return 0, 0, 0, nil, bad()
	}
	if cursor != uint64(len(body)) {
		return 0, 0, 0, nil, bad()
	}
	return
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
		daily := len(raw) >= 8 && string(raw[:8]) == "SUMDAY00"
		if daily {
			var e error
			day, from, to, batch, e = decodeSummaryDay(raw)
			if e != nil {
				return e
			}
			if raw[9] != 1 {
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

// Daily compaction copies older compressed packets verbatim. It does not decode
// events, nor recompress older days. The atomic replacement currently copies the
// retained summary file once per daily rollover; measure that write cost.
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
