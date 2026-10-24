package cache

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
)

type ViewSummaryDay struct {
	Key        string `json:"key"`
	Source     string `json:"source"`
	Definition string `json:"definition"`
	Generation string `json:"generation"`
	Day        int64  `json:"day"`
	Sealed     bool   `json:"sealed"`
}
type ViewCalendarRollups struct {
	Source     string           `json:"source"`
	Definition string           `json:"definition"`
	Generation string           `json:"generation"`
	Buckets    []CalendarRollup `json:"buckets"`
}
type ViewAnalytics struct {
	Rollups []ViewCalendarRollups `json:"rollups,omitempty"`
	From    int64                 `json:"from"`
	To      int64                 `json:"to"`
	Days    []ViewSummaryDay      `json:"days"`
	Border  []statisticsGroup     `json:"border,omitempty"`
}

func (s *Store) rememberSummaryExport(raw []byte) string {
	key := summaryDigest(raw)
	if s.summaryExports == nil {
		s.summaryExports = map[string][]byte{}
	}
	if _, ok := s.summaryExports[key]; ok {
		return key
	}
	s.summaryExports[key] = raw
	s.summaryExportOrder = append(s.summaryExportOrder, key)
	s.summaryExportBytes += len(raw)
	for s.summaryExportBytes > 64<<20 && len(s.summaryExportOrder) > 1 {
		old := s.summaryExportOrder[0]
		s.summaryExportOrder = s.summaryExportOrder[1:]
		s.summaryExportBytes -= len(s.summaryExports[old])
		delete(s.summaryExports, old)
	}
	return key
}
func (h *HTTP) summaryDayHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	q := r.URL.Query()
	if len(q) != 3 || len(q["key"]) != 1 || len(q["source"]) != 1 || len(q["definition"]) != 1 {
		http.Error(w, "invalid summary reference", 400)
		return
	}
	key := q.Get("key")
	if len(key) != 64 {
		http.Error(w, "invalid summary key", 400)
		return
	}
	if _, e := hex.DecodeString(key); e != nil {
		http.Error(w, "invalid summary key", 400)
		return
	}
	apis := h.sources
	if len(apis) == 0 {
		apis = []*HTTP{h}
	}
	for _, api := range apis {
		if fmtDigestNative(api.Store.layout.Digest) != q.Get("definition") {
			continue
		}
		branches, e := api.branch(q.Get("source"))
		if e != nil || len(branches) != 1 {
			continue
		}
		s := api.Store
		s.mu.Lock()
		b, e := s.openBranch(branches[0])
		if e != nil {
			s.mu.Unlock()
			http.Error(w, e.Error(), 500)
			return
		}
		for _, loc := range b.summaryDays {
			if loc.Key != key {
				continue
			}
			// Pin the immutable inode across retention/repacking. No packet-sized
			// Go buffer and no summary decompression are needed for delivery.
			file, e := os.Open(b.sum.Name())
			s.mu.Unlock()
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			defer file.Close()
			w.Header().Set("Content-Type", "application/vnd.trie-trunk.summary")
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("ETag", `"`+key+`"`)
			w.Header().Set("Content-Length", strconv.FormatUint(loc.Length, 10))
			_, _ = io.Copy(w, io.NewSectionReader(file, int64(loc.Offset), int64(loc.Length)))
			return
		}
		raw := s.summaryExports[key]
		s.mu.Unlock()
		if raw == nil {
			http.Error(w, "summary snapshot expired; retry search", 409)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.trie-trunk.summary")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("ETag", `"`+key+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.Write(raw)
		return
	}
	http.NotFound(w, r)
}

// Produce complete-day replicas for the five-minute-aligned interior. Exact
// partial buckets remain server-side index counts, with explicit statistics
// completeness. No cold event PAC is opened for summary generation.
func buildViewAnalytics(sources []*viewSource, from, to int64, resolution Resolution) (*ViewAnalytics, []*viewSource, error) {
	a := &ViewAnalytics{From: (from + sumBucketMS - 1) / sumBucketMS * sumBucketMS, To: to / sumBucketMS * sumBucketMS}
	if a.From >= a.To {
		return nil, sources, nil
	}
	var remaining []*viewSource
	for _, v := range sources {
		if !v.hasSummaries {
			remaining = append(remaining, v)
			continue
		}
		s := v.api.Store
		grouped := map[int64][]Summary{}
		for _, sum := range append(append([]Summary(nil), v.summaries...), v.liveSummaries...) {
			day := summaryDay(sum.Columns.Period[0])
			if day+summaryDayMS > a.From && day < a.To {
				grouped[day] = append(grouped[day], sum)
			}
		}
		// This is the unfiltered summary path. Never apply resource-wide totals
		// to a text/field-filtered candidate set. Return contained coverage only;
		// the client retains fine-grained packets for timezone/query edges.
		s.mu.Lock()
		branch := s.branches[v.branch]
		if branch == nil || hex.EncodeToString(branch.generation[:]) != v.binding.Generation || branch.baseRows != v.binding.Retired {
			s.mu.Unlock()
			return nil, nil, fmt.Errorf("summary source changed; retry search")
		}
		var buckets []CalendarRollup
		for _, r := range branch.calendarRollups {
			if r.Resolution == resolution && r.CoveredFrom >= a.From && r.CoveredTo <= a.To && r.CoveredTo <= v.binding.Finalized {
				buckets = append(buckets, r)
			}
		}
		s.mu.Unlock()
		if len(buckets) > 0 {
			a.Rollups = append(a.Rollups, ViewCalendarRollups{v.api.tap(v.branch), v.binding.Definition, v.binding.Generation, buckets})
		}
		var days []int64
		for day := range grouped {
			days = append(days, day)
		}
		sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
		for _, day := range days {
			sealed := day+summaryDayMS <= v.binding.Finalized
			s.mu.Lock()
			b := s.branches[v.branch]
			if b == nil || hex.EncodeToString(b.generation[:]) != v.binding.Generation || b.baseRows != v.binding.Retired {
				s.mu.Unlock()
				return nil, nil, fmt.Errorf("summary source changed; retry search")
			}
			var key string
			if loc, ok := b.summaryDays[day]; ok && sealed {
				key = loc.Key
			} else {
				begin := day
				if b.timeAnchor != nil {
					begin = max(begin, b.timeAnchor.First)
				}
				raw, e := encodeSummaryDay(day, begin, day+summaryDayMS, grouped[day], sealed)
				if e != nil {
					s.mu.Unlock()
					return nil, nil, e
				}
				key = s.rememberSummaryExport(raw)
			}
			s.mu.Unlock()
			a.Days = append(a.Days, ViewSummaryDay{key, v.api.tap(v.branch), v.binding.Definition, v.binding.Generation, day, sealed})
		}
		// Shallow copy shares the already pinned snapshot; it must not be closed.
		edge := *v
		edge.ranges = nil
		for _, span := range v.ranges {
			first := span.at
			left := viewLower(v, first, span.end, a.From)
			right := viewLower(v, first, span.end, a.To)
			if first < left {
				edge.ranges = append(edge.ranges, cursor{at: first, end: left})
			}
			if right < span.end {
				edge.ranges = append(edge.ranges, cursor{at: right, end: span.end})
			}
		}
		remaining = append(remaining, &edge)
	}
	if len(a.Days) == 0 {
		return nil, sources, nil
	}
	return a, remaining, nil
}
func viewLower(v *viewSource, a, z uint64, ms int64) uint64 {
	return a + uint64(sort.Search(int(z-a), func(i int) bool { return eventTime(v.eventID(a+uint64(i))) >= ms }))
}
func addViewCounts(summary *ViewSummary, sources []*viewSource) {
	for _, s := range sources {
		for _, span := range s.ranges {
			at := span.at
			for i := range summary.Counts {
				hi := min(summary.To, summary.From+int64(i+1)*summary.Step)
				end := viewLower(s, at, span.end, hi)
				n := end - at
				summary.Counts[i] += n
				summary.Count += n
				if s.branch == Error {
					summary.Errors[i] += n
				}
				at = end
				if at == span.end {
					break
				}
			}
		}
	}
}

// Merge the partial-bucket/non-aggregated border before export. This keeps exact
// percentile distributions without serializing one metadata object per event.
type summaryBorder struct {
	groups map[[4]string]*statisticsGroup
	hist   map[[4]string]map[uint64]uint64
}

func (b *summaryBorder) add(g statisticsGroup) {
	if b.groups == nil {
		b.groups = map[[4]string]*statisticsGroup{}
		b.hist = map[[4]string]map[uint64]uint64{}
	}
	out := b.groups[g.Labels]
	if out == nil {
		out = &statisticsGroup{Labels: g.Labels}
		b.groups[g.Labels] = out
		b.hist[g.Labels] = map[uint64]uint64{}
	}
	out.Count += g.Count
	out.Errors += g.Errors
	for _, pair := range g.Durations {
		b.hist[g.Labels][pair[0]] += pair[1]
	}
}
func (b *summaryBorder) rows() []statisticsGroup {
	rows := make([]statisticsGroup, 0, len(b.groups))
	for key, g := range b.groups {
		for value, count := range b.hist[key] {
			g.Durations = append(g.Durations, [2]uint64{value, count})
		}
		sort.Slice(g.Durations, func(i, j int) bool { return g.Durations[i][0] < g.Durations[j][0] })
		rows = append(rows, *g)
	}
	sort.Slice(rows, func(i, j int) bool {
		for k := range rows[i].Labels {
			if rows[i].Labels[k] != rows[j].Labels[k] {
				return rows[i].Labels[k] < rows[j].Labels[k]
			}
		}
		return false
	})
	return rows
}
