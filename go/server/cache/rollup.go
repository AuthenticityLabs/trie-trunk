package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Resolution string

const (
	Daily   Resolution = "day"
	Weekly  Resolution = "week"
	Monthly Resolution = "month"
	Yearly  Resolution = "year"
)

func (s *Store) rollupPeriod(branch Branch, res Resolution, at time.Time) (path string, from, to time.Time, err error) {
	at = at.In(s.location)
	prefix := branch.Prefix()
	year := at.Year()
	from = time.Date(year, at.Month(), at.Day(), 0, 0, 0, 0, s.location)
	switch res {
	case Daily:
		path = filepath.Join(s.dir, fmt.Sprint(year), prefix+".365")
		to = from.AddDate(0, 0, 1)
	case Weekly:
		weekday := (int(at.Weekday()) + 6) % 7
		from = from.AddDate(0, 0, -weekday)
		to = from.AddDate(0, 0, 7)
		y, w := from.ISOWeek()
		path = filepath.Join(s.dir, fmt.Sprint(y), fmt.Sprintf("%s.w%02d", prefix, w))
	case Monthly:
		from = time.Date(year, at.Month(), 1, 0, 0, 0, 0, s.location)
		to = from.AddDate(0, 1, 0)
		path = filepath.Join(s.dir, fmt.Sprint(year), fmt.Sprintf("%s.m%02d", prefix, at.Month()))
	case Yearly:
		from = time.Date(year, 1, 1, 0, 0, 0, 0, s.location)
		to = from.AddDate(1, 0, 0)
		path = filepath.Join(s.dir, fmt.Sprintf("%s.y%02d", prefix, year%100))
	default:
		err = fmt.Errorf("unsupported rollup resolution")
	}
	return
}

type rollup struct {
	From, To int64
	Groups   []Group
}

// PublishRollup preserves closed-period, idempotent publication. If a late source
// correction changes an already published period it reports a conflict, requiring
// an explicit rebuild rather than silently appending a second contradictory total.
func (s *Store) PublishRollup(branch Branch, res Resolution, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	if !branch.Valid() || !branch.Aggregates() {
		return fmt.Errorf("branch does not aggregate")
	}
	path, from, to, e := s.rollupPeriod(branch, res, at)
	if e != nil {
		return e
	}
	if to.After(s.config.Now().Add(-15 * time.Minute)) {
		return fmt.Errorf("aggregate period not finalized")
	}
	summaries, e := s.summaries(branch, from, to)
	if e != nil {
		return e
	}
	groups := map[uint16]Aggregate{}
	for _, g := range summaries {
		for ref, a := range g {
			current := groups[ref]
			if e = current.merge(a); e != nil {
				return e
			}
			groups[ref] = current
		}
	}
	value := rollup{From: from.UnixMilli(), To: to.UnixMilli()}
	for ref, a := range groups {
		value.Groups = append(value.Groups, Group{value.From, ref, a})
	}
	sort.Slice(value.Groups, func(i, j int) bool { return value.Groups[i].Reference < value.Groups[j].Reference })
	data, e := encodeRollup(value)
	if e != nil {
		return e
	}
	if e = s.check(); e != nil {
		return e
	}
	b := s.branches[branch]
	if b == nil || b.sum == nil {
		return fmt.Errorf("no configured aggregates")
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := openFile(path, "NUTSAGG6", s.layout.Digest, b.generation, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	found := false
	e = records(f, func(raw []byte) error {
		var old rollup
		if e := decodeRollup(raw, &old); e != nil {
			return e
		}
		if old.From == value.From {
			canonical, err := encodeRollup(old)
			if err != nil {
				return err
			}
			if found || !bytes.Equal(canonical, data) {
				return fmt.Errorf("published aggregate conflict")
			}
			found = true
		}
		return nil
	})
	if e != nil {
		return e
	}
	if !found {
		if e = appendRecord(f, data); e != nil {
			return e
		}
	}
	if e = f.Sync(); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}
func (s *Store) ReadRollup(branch Branch, res Resolution, at time.Time) (map[uint16]Aggregate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	if !branch.Valid() || !branch.Aggregates() {
		return nil, fmt.Errorf("branch does not aggregate")
	}
	path, from, _, e := s.rollupPeriod(branch, res, at)
	if e != nil {
		return nil, e
	}
	b := s.branches[branch]
	if b == nil {
		return nil, os.ErrNotExist
	}
	if _, e = os.Stat(path); e != nil {
		return nil, e
	}
	f, e := openFile(path, "NUTSAGG6", s.layout.Digest, b.generation, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	var out map[uint16]Aggregate
	e = records(f, func(raw []byte) error {
		var value rollup
		if e := decodeRollup(raw, &value); e != nil {
			return e
		}
		if value.From == from.UnixMilli() {
			if out != nil {
				return fmt.Errorf("duplicate aggregate period")
			}
			out = map[uint16]Aggregate{}
			for _, g := range value.Groups {
				out[g.Reference] = g.Aggregate
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	if out == nil {
		return nil, os.ErrNotExist
	}
	return out, nil
}
