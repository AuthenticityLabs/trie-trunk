package cache

import (
	"fmt"
	"time"
)

// Package sizing is a replication policy, not a storage-format or definition
// version. Changing it rebuilds navigation; source rows and payloads stay put.
func validIndexPeriod(p string) bool {
	switch p {
	case "", "1h", "4h", "6h", "12h", "24h", "day":
		return true
	}
	return false
}
func (r ReplicationPolicy) hours() int {
	switch r.IndexPeriod {
	case "4h":
		return 4
	case "6h":
		return 6
	case "24h", "day":
		return 24
	case "12h":
		return 12
	default:
		return 1
	}
}
func semanticBounds(at time.Time, hours int, loc *time.Location) (time.Time, time.Time) {
	at = at.In(loc)
	if hours == 1 {
		// Use this occurrence's offset, so two fall-back 01:00 hours have
		// distinct absolute bounds. Clip at transitions, including half-hour DST.
		_, offset := at.Zone()
		from := time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), 0, 0, 0, time.FixedZone("", offset))
		to := from.Add(time.Hour)
		start, end := at.ZoneBounds()
		if !start.IsZero() && start.After(from) {
			from = start
		}
		if !end.IsZero() && end.Before(to) {
			to = end
		}
		return from.In(loc), to.In(loc)
	}
	h := at.Hour() / hours * hours
	from := calendarBoundary(at.Year(), at.Month(), at.Day(), h, loc)
	// Calendar construction, not Add(hours): retain noon/midnight across DST.
	to := calendarBoundary(at.Year(), at.Month(), at.Day(), h+hours, loc)
	return from, to
}

// Some zones skip midnight (or a whole civil date). A missing wall-clock
// boundary resolves to the first valid instant after the gap. time.Date alone
// may normalize backward, which would overlap packages or stall a planner.
func calendarBoundary(year int, month time.Month, day, hour int, loc *time.Location) time.Time {
	wanted := time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	at := time.Date(year, month, day, hour, 0, 0, 0, loc)
	civil := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	}
	if actual := civil(at); actual.Before(wanted) {
		at = at.Add(wanted.Sub(actual))
	}
	if civil(at).After(wanted) {
		start, _ := at.ZoneBounds()
		if !start.IsZero() && !civil(start).Before(wanted) {
			at = start
		}
	}
	// Repeated midnight belongs to one package; choose its first occurrence.
	if start, _ := at.ZoneBounds(); !start.IsZero() {
		_, offset := at.Zone()
		_, previous := start.Add(-time.Nanosecond).Zone()
		earlier := at.Add(time.Duration(offset-previous) * time.Second)
		if earlier.Before(at) && civil(earlier).Equal(wanted) {
			at = earlier
		}
	}
	return at
}

type PackageWindow struct {
	From, To    time.Time
	IndexPeriod string
	Timezone    string
}

// PlanPackages rounds a half-open query outward to complete semantic packages.
// Empty intervals are included: a successful collection can certify zero rows.
// It does not imply that a client has coverage or that an open package is final.
func (s *Store) PlanPackages(from, to time.Time) ([]PackageWindow, error) {
	return s.planPackages(from, to, Read, false)
}
func (s *Store) PlanBranchPackages(branch Branch, from, to time.Time) ([]PackageWindow, error) {
	if !branch.Valid() {
		return nil, fmt.Errorf("invalid branch")
	}
	return s.planPackages(from, to, branch, true)
}
func (s *Store) planPackages(from, to time.Time, branch Branch, branchPolicy bool) ([]PackageWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	if from.IsZero() || !from.Before(to) || from.Year() < 1 || to.Year() > 9999 {
		return nil, fmt.Errorf("invalid package window")
	}
	h := s.policy.Replication.hours()
	if branchPolicy {
		h = s.policyFor(branch).Replication.hours()
	}
	var out []PackageWindow
	a, b := semanticBounds(from, h, s.location)
	for a.Before(to) {
		if !b.After(a) {
			return nil, fmt.Errorf("non-increasing calendar boundary")
		}
		out = append(out, PackageWindow{a, b, fmt.Sprintf("%dh", h), s.location.String()})
		a, b = semanticBounds(b, h, s.location)
	}
	return out, nil
}

func (s *Store) provisionalTail(branch ...Branch) time.Duration {
	p := s.policy
	if len(branch) > 0 {
		p = s.policyFor(branch[0])
	}
	if raw := p.Replication.ProvisionalTail; raw != "" {
		d, _ := time.ParseDuration(raw) // validated when loading policy
		return d
	}
	return 15 * time.Minute
}
