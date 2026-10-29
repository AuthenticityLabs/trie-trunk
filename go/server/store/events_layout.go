package store

import (
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"trie-trunk/go/codec"
)

const matureDelay = 15 * time.Minute
const compactRowSize = 28
const indexMetadataSize = 28

type scope struct {
	Start, End time.Time
	Owner      uint32
	Dir        string
}

func (s *schema) modern() bool { return s.definition.Storage != nil }
func (s *schema) filename(ext string) string {
	if s.modern() {
		return "events." + ext
	}
	return "log." + ext
}
func (s *schema) openLog(path, magic string) (*os.File, error) {
	version := uint32(2)
	if s.modern() {
		version = s.definition.Storage.Format
	}
	return openLogVersion(path, magic, version)
}
func loadLocation(name string) (*time.Location, error) {
	if len(name) == 6 && (name[0] == '+' || name[0] == '-') && name[3] == ':' {
		h, e := strconv.Atoi(name[1:3])
		m, e2 := strconv.Atoi(name[4:])
		if e != nil || e2 != nil || h < 0 || m < 0 || h > 23 || m > 59 {
			return nil, fmt.Errorf("invalid UTC offset")
		}
		offset := h*3600 + m*60
		if name[0] == '-' {
			offset = -offset
		}
		return time.FixedZone(name, offset), nil
	}
	return time.LoadLocation(name)
}

// Intersect this civil date with each constant-offset timezone interval. Unlike
// time.Date(midnight), this also handles a DST jump which removes local midnight.
func (s *schema) dayBounds(at time.Time) (time.Time, time.Time) {
	local := at.In(s.location)
	civil := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	next := civil.AddDate(0, 0, 1)
	scan := civil.Add(-48 * time.Hour)
	stop := next.Add(48 * time.Hour)
	var first, last time.Time
	for scan.Before(stop) {
		zoned := scan.In(s.location)
		_, offset := zoned.Zone()
		_, end := zoned.ZoneBounds()
		if end.IsZero() || end.After(stop) {
			end = stop
		}
		lo, hi := civil.Add(-time.Duration(offset)*time.Second), next.Add(-time.Duration(offset)*time.Second)
		if lo.Before(scan) {
			lo = scan
		}
		if hi.After(end) {
			hi = end
		}
		if lo.Before(hi) {
			if first.IsZero() || lo.Before(first) {
				first = lo
			}
			if last.IsZero() || hi.After(last) {
				last = hi
			}
		}
		scan = end
	}
	return first.In(s.location), last.In(s.location)
}
func (s *schema) day(at time.Time) time.Time { start, _ := s.dayBounds(at); return start }
func (s *schema) bucket(id codec.UUIDv7) uint16 {
	at := time.UnixMilli(millis(id))
	return uint16(at.Sub(s.day(at)) / (5 * time.Minute))
}
func (s *schema) scope(id codec.UUIDv7, owner uint32) scope {
	at := time.UnixMilli(millis(id))
	start, end := s.dayBounds(at)
	local := at.In(s.location)
	dir := fmt.Sprintf("%04d/%03d", local.Year(), local.YearDay())
	if s.definition.Storage.Bucket == "hour" {
		start = start.Add(at.Sub(start).Truncate(time.Hour))
		candidate := start.Add(time.Hour)
		if candidate.Before(end) {
			end = candidate
		}
		dir = filepath.Join(dir, start.Format("150405-0700"))
	}
	if start.Before(time.UnixMilli(0)) {
		start = time.UnixMilli(0)
	}
	if end.After(time.UnixMilli(1 << 48)) {
		end = time.UnixMilli(1 << 48)
	}
	dir = filepath.Join(dir, strconv.FormatUint(uint64(owner), 10))
	return scope{start, end, owner, dir}
}
func (s *schema) owner(tr *trie, leaf uint32) uint32 {
	if s.hasBoundary {
		for leaf != 0 {
			n := tr.nodes[leaf]
			if n.Key == s.boundary {
				return leaf
			}
			leaf = n.Parent
		}
	}
	return 0
}
func readSchemaMap(dir string) ([]byte, error) {
	modern, e := readMap(filepath.Join(dir, "events.rx"))
	if e == nil {
		if _, err := os.Stat(filepath.Join(dir, "log.rx")); err == nil {
			return nil, fmt.Errorf("ambiguous schema maps in %s", dir)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		return modern, nil
	}
	if !os.IsNotExist(e) {
		return nil, e
	}
	return readMap(filepath.Join(dir, "log.rx"))
}
func readScope(dir string) (scope, bool, error) {
	f, err := os.Open(filepath.Join(dir, "events.idx"))
	if err != nil {
		return scope{}, false, err
	}
	defer f.Close()
	var b [36]byte
	if _, err = f.ReadAt(b[:], 0); err != nil {
		return scope{}, false, err
	}
	magic := string(b[:4])
	if (magic != "TIDA" && magic != "TIDC") || (order.Uint32(b[4:8]) != 3 && order.Uint32(b[4:8]) != 4) || order.Uint32(b[8:12]) != 20 {
		return scope{}, false, fmt.Errorf("invalid event index header")
	}
	if checksum(b[16:]) != order.Uint32(b[12:16]) {
		return scope{}, false, fmt.Errorf("invalid event index scope checksum")
	}
	start, end := order.Uint64(b[16:24]), order.Uint64(b[24:32])
	if start >= end || end > 1<<48 {
		return scope{}, false, fmt.Errorf("invalid bucket bounds")
	}
	owner := order.Uint32(b[32:])
	if filepath.Base(dir) != strconv.FormatUint(uint64(owner), 10) {
		return scope{}, false, fmt.Errorf("index owner/path mismatch")
	}
	return scope{time.UnixMilli(int64(start)), time.UnixMilli(int64(end)), owner, dir}, magic == "TIDC", nil
}
func (s *schema) scopes(root, tap string) ([]scope, error) {
	base := s.directory(root, tap)
	var scopes []scope
	err := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && strings.HasPrefix(e.Name(), ".") {
			return filepath.SkipDir
		}
		if !e.IsDir() && e.Name() == "events.idx" {
			v, _, err := readScope(filepath.Dir(path))
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, v.Dir)
			if err != nil {
				return err
			}
			v.Dir = rel
			scopes = append(scopes, v)
		}
		return nil
	})
	return scopes, err
}
func checksum(b []byte) uint32 { return crc32.Checksum(b, crcTable) }

// Existing scope headers are authoritative across timezone database updates.
func (m *Manager) routeScope(s *schema, id codec.UUIDv7, owner uint32) (scope, error) {
	candidate := s.scope(id, owner)
	known := m.known[s.revision.Digest]
	at := time.UnixMilli(millis(id))
	if old, ok := known[candidate.Dir]; ok && !at.Before(old.Start) && at.Before(old.End) {
		return old, nil
	}
	for _, old := range known {
		if old.Owner == owner && !at.Before(old.Start) && at.Before(old.End) {
			return old, nil
		}
	}
	if _, exists := known[candidate.Dir]; exists {
		return scope{}, fmt.Errorf("timezone rules changed an existing bucket; register a new schema")
	}
	return candidate, nil
}
