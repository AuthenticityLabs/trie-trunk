package cache

import "time"

// Historical map baseline for comparison, never used by the store.
type timeTrie struct {
	years       map[int]map[int]*Period
	last        *Period
	lastPackage *Period
	hours       int
	location    *time.Location
	lastRow     uint64
	hasRow      bool
	min, max    int64
}

func newTimeTrie(hours ...int) timeTrie {
	h := 1
	if len(hours) != 0 {
		h = hours[0]
	}
	return timeTrie{years: map[int]map[int]*Period{}, hours: h}
}
func appendPeriod(last **Period, next *Period, row uint64) {
	if *last == next {
		return
	}
	if *last != nil {
		end := row
		(*last).Ranges[len((*last).Ranges)-1].End = &end
	}
	next.Ranges = append(next.Ranges, RowRange{Start: row})
	*last = next
}
func (t *timeTrie) append(ms int64, row uint64, location *time.Location) {
	firstRecord := !t.hasRow
	// Header slots interrupt otherwise contiguous event ranges.
	if t.hasRow && row != t.lastRow+1 {
		end := t.lastRow + 1
		if t.last != nil {
			t.last.Ranges[len(t.last.Ranges)-1].End = &end
		}
		if t.lastPackage != nil {
			t.lastPackage.Ranges[len(t.lastPackage.Ranges)-1].End = &end
		}
		t.last, t.lastPackage = nil, nil
	}
	t.hasRow, t.lastRow = true, row
	// Usually the next row belongs to the same package. Avoid calendar conversion.
	if p := t.lastPackage; p != nil && ms >= p.From && ms < p.To {
		return
	}
	t.location = location
	now := time.UnixMilli(ms).In(location)
	year, day := now.Year(), now.YearDay()
	days := t.years[year]
	if days == nil {
		days = map[int]*Period{}
		t.years[year] = days
	}
	p := days[day]
	if p == nil {
		from := calendarBoundary(year, now.Month(), now.Day(), 0, location)
		p = &Period{Year: year, Day: day, From: from.UnixMilli(), To: calendarBoundary(year, now.Month(), now.Day()+1, 0, location).UnixMilli(), Packages: map[int][]*Period{}}
		days[day] = p
	}
	appendPeriod(&t.last, p, row)
	from, to := semanticBounds(now, t.hours, location)
	hour := now.Hour() / t.hours * t.hours
	var part *Period
	for _, candidate := range p.Packages[hour] {
		if candidate.From == from.UnixMilli() {
			part = candidate
			break
		}
	}
	if part == nil {
		part = &Period{Year: year, Day: day, From: from.UnixMilli(), To: to.UnixMilli()}
		p.Packages[hour] = append(p.Packages[hour], part)
	}
	first := firstRecord
	appendPeriod(&t.lastPackage, part, row)
	if first || part.From < t.min {
		t.min = part.From
	}
	if part.To > t.max {
		t.max = part.To
	}
}

// visit seeks directly by year/day/package. It never scans unrelated row data.
func (t *timeTrie) visit(from, to time.Time, fn func(*Period)) {
	lo, hi := timeBound(from), timeBound(to)
	if lo < t.min {
		lo = t.min
	}
	if hi > t.max {
		hi = t.max
	}
	if lo >= hi {
		return
	}
	loc := t.location
	if loc == nil {
		return
	}
	at := time.UnixMilli(lo).In(loc)
	// Iterate civil labels in UTC: local midnight may be skipped by a timezone
	// transition. Only the persisted package boundaries determine inclusion.
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	last := time.UnixMilli(hi - 1).In(loc)
	lastDay := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
	for !day.After(lastDay) {
		if p := t.years[day.Year()][day.YearDay()]; p != nil {
			for h := 0; h < 24; h += t.hours {
				for _, part := range p.Packages[h] {
					if part.From < hi && part.To > lo {
						fn(part)
					}
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
}
