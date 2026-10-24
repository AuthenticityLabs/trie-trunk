package cache

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// CalendarRollup describes the retained, sealed portion of a UTC calendar period.
// Totals are never clipped to a query: callers must use five-minute columns at
// mismatched boundaries and for filters, labels, or percentile distributions.
type CalendarRollup struct {
	Resolution         Resolution `json:"resolution"`
	From               int64      `json:"from"`
	To                 int64      `json:"to"`
	CoveredFrom        int64      `json:"coveredFrom"`
	CoveredTo          int64      `json:"coveredTo"`
	StatisticsComplete bool       `json:"statisticsComplete"`
	Count              uint64     `json:"count,string"`
	DurationCount      uint64     `json:"durationCount,string"`
	Sum                uint64     `json:"sum,string"`
	Min                uint64     `json:"min,string"`
	Max                uint64     `json:"max,string"`
	Errors             uint64     `json:"errors,string"`
}

func calendarPeriod(res Resolution, ms int64) (int64, int64) {
	t := time.UnixMilli(ms).UTC()
	var start, end time.Time
	switch res {
	case Weekly:
		start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		start = start.AddDate(0, 0, -(int(t.Weekday())+6)%7)
		end = start.AddDate(0, 0, 7)
	case Monthly:
		start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)
	case Yearly:
		start = time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(1, 0, 0)
	}
	return start.UnixMilli(), end.UnixMilli()
}

func dailyRollup(sums []Summary) (CalendarRollup, error) {
	a, errors, e := summaryTotals(sums)
	r := CalendarRollup{Count: a.Count, DurationCount: a.DurationCount, Sum: a.Sum, Min: a.Min, Max: a.Max, Errors: errors, StatisticsComplete: true}
	for _, s := range sums {
		if len(s.Statistics) == 0 {
			r.StatisticsComplete = false
		}
	}
	return r, e
}

// Only one cached daily total per populated day is visited for each resolution.
// Empty days inside certified coverage contribute zero; outside it is unknown.
func calendarRollups(days map[int64]summaryDayLocation, from, to int64) ([]CalendarRollup, error) {
	if from >= to {
		return nil, nil
	}
	var dates []int64
	for day := range days {
		dates = append(dates, day)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i] < dates[j] })
	var out []CalendarRollup
	for _, res := range []Resolution{Weekly, Monthly, Yearly} {
		n := 0
		for at := from; at < to; {
			start, end := calendarPeriod(res, at)
			r := CalendarRollup{Resolution: res, From: start, To: end, CoveredFrom: max(start, from), CoveredTo: min(end, to), StatisticsComplete: true}
			var a Aggregate
			for n < len(dates) && dates[n] < r.CoveredTo {
				d := days[dates[n]]
				n++
				if d.To <= r.CoveredFrom {
					continue
				}
				if d.From < r.CoveredFrom || d.To > r.CoveredTo {
					return nil, fmt.Errorf("daily rollup crosses calendar boundary")
				}
				v := d.Rollup
				if e := a.merge(Aggregate{v.Count, v.DurationCount, v.Sum, v.Min, v.Max}); e != nil {
					return nil, e
				}
				if v.Errors > math.MaxUint64-r.Errors {
					return nil, fmt.Errorf("rollup errors overflow")
				}
				r.Errors += v.Errors
				r.StatisticsComplete = r.StatisticsComplete && v.StatisticsComplete
			}
			r.Count, r.DurationCount, r.Sum, r.Min, r.Max = a.Count, a.DurationCount, a.Sum, a.Min, a.Max
			out = append(out, r)
			at = end
		}
	}
	return out, nil
}

// The daily replacement transaction also publishes this small fixed-row table.
// The existing .sum record framing provides length and CRC; no separate journal.
const calendarRollupRowSize = 88

func encodeCalendarRollups(from, to int64, rows []CalendarRollup) []byte {
	raw := make([]byte, 24+len(rows)*calendarRollupRowSize)
	copy(raw, "SUMROLL0")
	order.PutUint64(raw[8:], uint64(from))
	order.PutUint64(raw[16:], uint64(to))
	for i, r := range rows {
		p := raw[24+i*calendarRollupRowSize:]
		switch r.Resolution {
		case Weekly:
			p[0] = 1
		case Monthly:
			p[0] = 2
		case Yearly:
			p[0] = 3
		}
		if r.StatisticsComplete {
			p[1] = 1
		}
		for j, v := range []int64{r.From, r.To, r.CoveredFrom, r.CoveredTo} {
			order.PutUint64(p[8+j*8:], uint64(v))
		}
		putSummaryTotals(p[40:], Aggregate{r.Count, r.DurationCount, r.Sum, r.Min, r.Max}, r.Errors)
	}
	return raw
}

// CalendarRollups returns persisted sealed daily-derived totals. Partial calendar
// periods remain explicit. Live hours are intentionally outside this snapshot.
func (s *Store) CalendarRollups(branch Branch, resolution Resolution, from, to time.Time) ([]CalendarRollup, error) {
	if resolution != Weekly && resolution != Monthly && resolution != Yearly {
		return nil, fmt.Errorf("invalid calendar resolution")
	}
	if !from.Before(to) {
		return nil, fmt.Errorf("invalid calendar interval")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitSealing()
	if e := s.check(); e != nil {
		return nil, e
	}
	if !branch.Valid() || !branch.Aggregates() {
		return nil, fmt.Errorf("branch does not aggregate")
	}
	b, e := s.openBranch(branch)
	if e != nil {
		return nil, e
	}
	if e = s.flush(b); e != nil {
		return nil, s.fail(e)
	}
	var out []CalendarRollup
	for _, r := range b.calendarRollups {
		if r.Resolution == resolution && r.CoveredFrom < to.UnixMilli() && r.CoveredTo > from.UnixMilli() {
			out = append(out, r)
		}
	}
	return out, nil
}
