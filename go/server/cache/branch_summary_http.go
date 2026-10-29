package cache

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

// Copy closed-day PACs unchanged. Unfinished days use an in-memory snapshot of
// existing aggregate columns, without searching event indexes or decoding events.
func (h *HTTP) branchSummaries(w http.ResponseWriter, r *http.Request, file branchFile, from, to time.Time) {
	if to.Sub(from) > 1024*24*time.Hour {
		http.Error(w, "summary range exceeds 1024 days", 400)
		return
	}
	s := h.Store
	s.mu.Lock()
	b, err := s.openBranch(file.branch)
	if err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), 500)
		return
	}
	if err = s.flush(b); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), 500)
		return
	}
	transient := map[int64][]byte{}
	grouped := map[int64][]Summary{}
	for _, sum := range b.summaries {
		day := summaryDay(sum.Columns.Period[0])
		if _, sealed := b.summaryDays[day]; !sealed && day >= from.UnixMilli() && day < to.UnixMilli() {
			grouped[day] = append(grouped[day], sum)
		}
	}
	for day, sums := range grouped {
		raw, e := encodeSummaryDay(day, day, day+summaryDayMS, sums, false)
		if e != nil {
			s.mu.Unlock()
			http.Error(w, e.Error(), 500)
			return
		}
		transient[day] = raw
	}
	var days []int64
	for day := range b.summaryDays {
		if day >= from.UnixMilli() && day < to.UnixMilli() {
			days = append(days, day)
		}
	}
	for day := range transient {
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	var locs []summaryDayLocation
	for _, day := range days {
		if raw, ok := transient[day]; ok {
			locs = append(locs, summaryDayLocation{Length: uint64(len(raw))})
		} else {
			locs = append(locs, b.summaryDays[day])
		}
	}
	var f *os.File
	if len(locs) > 0 {
		f, err = os.Open(b.sum.Name())
	}
	generation := fmt.Sprintf("%x", b.generation)
	sequence := s.generationSequence(file.branch)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if f != nil {
		defer f.Close()
	}
	w.Header().Set("Trunk-Format", "summary-packs/0")
	w.Header().Set("Trunk-Path", file.path)
	w.Header().Set("Trunk-Source", h.tap(file.branch))
	w.Header().Set("Definition-Version", fmtDigestNative(s.layout.Digest))
	w.Header().Set("Trunk-Generation", generation)
	w.Header().Set("Trunk-Generation-Sequence", strconv.FormatUint(sequence, 10))
	w.Header().Set("Trunk-Pack-Count", strconv.Itoa(len(locs)))
	w.Header().Set("Content-Type", "application/pil")
	w.Header().Set("Cache-Control", "private, no-store")
	array := extended(nil, uint64(len(locs)))
	var size uint64
	for _, loc := range locs {
		size += loc.Length
		array = extended(array, size)
	}
	prefix := extended([]byte{0xc0}, uint64(len(array))+size)
	prefix = append(prefix, array...)
	w.Header().Set("Content-Length", strconv.FormatUint(uint64(len(prefix))+size, 10))
	out := summaryContextWriter{r.Context(), w}
	if _, err = out.Write(prefix); err != nil {
		return
	}
	buf := make([]byte, 32<<10)
	for i, loc := range locs {
		if raw, ok := transient[days[i]]; ok {
			if _, err = out.Write(raw); err != nil {
				panic(http.ErrAbortHandler)
			}
			continue
		}
		if _, err = io.CopyBuffer(out, io.NewSectionReader(f, int64(loc.Offset), int64(loc.Length)), buf); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}
