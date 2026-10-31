package cache

import (
	"bytes"
	"math"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestCalendarRollupBoundariesAndTotals(t *testing.T) {
	ms := func(s string) int64 {
		v, e := time.Parse("2006-01-02", s)
		if e != nil {
			t.Fatal(e)
		}
		return v.UnixMilli()
	}
	days := map[int64]summaryDayLocation{}
	// Leap day, absent durations, and unequal counts prove weighted averages.
	for i, date := range []string{"2024-02-28", "2024-02-29", "2024-03-01"} {
		a := ms(date)
		r := CalendarRollup{StatisticsComplete: true, Count: uint64(i + 1), DurationCount: uint64(i + 1), Sum: uint64((i + 1) * 10), Min: 10, Max: 10, Errors: uint64(i)}
		if i == 1 {
			r.DurationCount = 0
			r.Sum = 0
			r.Min = 0
			r.Max = 0
		}
		days[a] = summaryDayLocation{From: a, To: a + summaryDayMS, Rollup: r}
	}
	rows, e := calendarRollups(days, ms("2024-02-28"), ms("2024-03-03"))
	if e != nil {
		t.Fatal(e)
	}
	var months []CalendarRollup
	for _, r := range rows {
		switch r.Resolution {
		case Monthly:
			months = append(months, r)
		case Yearly, Weekly:
			if r.Count != 6 || r.DurationCount != 4 || r.Sum != 40 || r.Min != 10 || r.Max != 10 || r.Errors != 3 {
				t.Fatalf("bad totals %+v", r)
			}
			if r.CoveredTo != ms("2024-03-03") {
				t.Fatal("empty day lost")
			}
		}
	}
	if len(months) != 2 || months[0].Count != 3 || months[1].Count != 3 || months[0].To != ms("2024-03-01") {
		t.Fatal(months)
	}
	a, z := calendarPeriod(Weekly, ms("2021-01-01"))
	if a != ms("2020-12-28") || z != ms("2021-01-04") {
		t.Fatal("ISO week year", a, z)
	}
	// The same instant in a different timezone selects the same UTC rollup.
	instant := time.UnixMilli(ms("2024-03-01")).In(time.FixedZone("UTC-5", -5*3600))
	a, _ = calendarPeriod(Monthly, instant.UnixMilli())
	if a != ms("2024-03-01") {
		t.Fatal("timezone affected storage")
	}
	empty, e := calendarRollups(nil, ms("2024-01-01"), ms("2024-02-01"))
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range empty {
		if r.Count != 0 || !r.StatisticsComplete {
			t.Fatal(r)
		}
	}
	// Totals must fail rather than wrap when crossing days.
	d := days[ms("2024-02-28")]
	d.Rollup.Count = math.MaxUint64
	days[ms("2024-02-28")] = d
	if _, e = calendarRollups(days, ms("2024-02-28"), ms("2024-03-03")); e == nil {
		t.Fatal("overflow accepted")
	}
}

func TestCalendarRollupPublicationRecoveryRetention(t *testing.T) {
	l := testLayout(t, false)
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	now := start.Add(3*24*time.Hour + 15*time.Minute)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return now }}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 72; i++ {
		if _, e = s.Append(Read, testFrame(t, l, start.Add(time.Duration(i)*time.Hour), uint32(i+1), false)); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	rows, e := s.CalendarRollups(Read, Monthly, start, start.AddDate(0, 1, 0))
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 || rows[0].Count != 24 || rows[1].Count != 48 {
		t.Fatalf("monthly %+v", rows)
	}
	before := append([]CalendarRollup(nil), s.branches[Read].calendarRollups...)
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b, e := s.openBranch(Read)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, b.calendarRollups) {
		t.Fatal("restart changed totals")
	}
	// A partial retained day must not retain its expired counts in parent totals.
	s.mu.Lock()
	e = s.prune(Read, start.Add(12*time.Hour))
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	rows, e = s.CalendarRollups(Read, Monthly, start, start.AddDate(0, 1, 0))
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 || rows[0].Count != 12 || rows[0].CoveredFrom != start.Add(12*time.Hour).UnixMilli() || rows[1].Count != 48 {
		t.Fatalf("retained %+v", rows)
	}
	// Advancing across known empty days still advances coverage, without new events.
	now = now.AddDate(0, 0, 2)
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	rows, e = s.CalendarRollups(Read, Monthly, start, start.AddDate(0, 1, 0))
	if e != nil {
		t.Fatal(e)
	}
	if rows[1].CoveredTo != start.AddDate(0, 0, 5).UnixMilli() || rows[1].Count != 48 {
		t.Fatal("empty-day rollover", rows)
	}
	if _, e = s.CalendarRollups(Debug, Monthly, start, now); e == nil {
		t.Fatal("debug aggregates")
	}
	// Checksummed but inconsistent derived totals must still fail validation.
	b = s.branches[Read]
	raw, e := readBytes(b.sum, 72, b.sumEnd)
	if e != nil {
		t.Fatal(e)
	}
	at := bytes.Index(raw, []byte("SUMROLL0"))
	if at < 0 {
		t.Fatal("no persisted rollups")
	}
	n := int(order.Uint32(raw[at-8:]))
	packet := bytes.Clone(raw[at : at+n])
	packet[24+40] ^= 1
	if e = b.sum.Truncate(int64(72 + at - 8)); e != nil {
		t.Fatal(e)
	}
	if _, e = b.sum.Seek(0, 2); e != nil {
		t.Fatal(e)
	}
	if e = appendBoundedRecord(b.sum, packet, maxSummaryDay+32); e != nil {
		t.Fatal(e)
	}
	if e = s.loadSummaries(b); e == nil {
		t.Fatal("inconsistent rollup accepted")
	}
}

func BenchmarkCalendarRollupsYear(b *testing.B) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	days := map[int64]summaryDayLocation{}
	for i := int64(0); i < 366; i++ {
		at := start + i*summaryDayMS
		days[at] = summaryDayLocation{From: at, To: at + summaryDayMS, Rollup: CalendarRollup{Count: 10000, DurationCount: 10000, Sum: 500000, Min: 1, Max: 100, StatisticsComplete: true}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := calendarRollups(days, start, start+366*summaryDayMS); e != nil {
			b.Fatal(e)
		}
	}
}

func TestCalendarRollupHTTPSelection(t *testing.T) {
	l := testLayout(t, false)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s, e := Open(Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return start.Add(25 * time.Hour) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Append(Read, testFrame(t, l, start, 1, false)); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	get := func(res, known, query string) (int, ViewResult) {
		q := url.Values{"tap": {"demo/payment/transactions.v1/read"}, "from": {start.Format(time.RFC3339)}, "to": {start.Add(24 * time.Hour).Format(time.RFC3339)}, "include": {"summary"}}
		if query != "" {
			q.Set("query", query)
		}
		r := httptest.NewRequest("GET", "/events?"+q.Encode(), nil)
		r.Header.Set("Accept", "application/pil")
		r.Header.Set("PIL-Version", "2")
		r.Header.Set("PIL-Features", "pac-arrays,daily-summaries")
		r.Header.Set("Summary-Resolution", res)
		r.Header.Set("Summary-Known", known)
		w := httptest.NewRecorder()
		(&HTTP{Store: s}).serveHTTP(w, r)
		if w.Code != 200 {
			return w.Code, ViewResult{}
		}
		return w.Code, decodeView(t, l, w.Body.Bytes()).result
	}
	code, base := get("", "", "")
	if code != 200 || base.Analytics == nil || len(base.Analytics.Rollups) != 0 {
		t.Fatal("default view changed", code)
	}
	code, monthly := get("month", base.SummaryVersion, "")
	if code != 200 || monthly.Analytics == nil || len(monthly.Analytics.Rollups) != 1 || monthly.SummaryVersion == base.SummaryVersion {
		t.Fatal("resolution cache identity", code)
	}
	if monthly.Analytics.Rollups[0].Buckets[0].Count != 1 {
		t.Fatal("incorrect count")
	}
	code, cached := get("month", monthly.SummaryVersion, "")
	if code != 200 || cached.Summary != nil {
		t.Fatal("known summary not reused", code)
	}
	if code, _ = get("decade", "", ""); code != 400 {
		t.Fatal("invalid resolution accepted")
	}
	if code, _ = get("month", "", `{"search":{"text":"timeout","mode":"phrase"}}`); code != 400 {
		t.Fatal("unfiltered totals allowed for text query", code)
	}
}
