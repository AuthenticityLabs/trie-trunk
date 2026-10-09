package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func versionDir(root, tap string, version uint32) string {
	return filepath.Join(root, tap, strconv.FormatUint(uint64(version), 10))
}

func ValidateTap(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return fmt.Errorf("tap must be 1..64 characters")
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '.' || c == '_' || c == '-') {
			continue
		}
		return fmt.Errorf("tap must start with a lowercase letter or digit and contain only a-z, 0-9, '.', '_' or '-'")
	}
	return nil
}

func checkTapDirectory(root, tap string) error {
	info, err := os.Lstat(filepath.Join(root, tap))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("tap must be a directory, not a file or symbolic link")
	}
	return nil
}

func dayPath(day time.Time) string {
	day = day.UTC()
	return fmt.Sprintf("%04d/%03d", day.Year(), day.YearDay())
}

func parseDay(path string) (time.Time, error) {
	day, err := time.Parse("2006/002", filepath.ToSlash(path))
	if err != nil || dayPath(day) != filepath.ToSlash(path) {
		return time.Time{}, fmt.Errorf("invalid UTC partition path %q", path)
	}
	return day, nil
}

type Resolution uint8

const (
	Daily Resolution = iota
	Weekly
	Monthly
	Yearly
)

func rollupPath(c Config, version uint32, resolution Resolution, at time.Time) (string, uint16, time.Time, error) {
	return schemaRollupPath(versionDir(c.Root, c.Tap, version), resolution, at, time.UTC, "log")
}

func schemaRollupPath(base string, resolution Resolution, at time.Time, zone *time.Location, prefix string) (string, uint16, time.Time, error) {
	at = at.In(zone)
	start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, zone)
	year := fmt.Sprintf("%04d", at.Year())
	switch resolution {
	case Daily:
		return filepath.Join(base, year, prefix+".365"), uint16(at.YearDay()), start.AddDate(0, 0, 1), nil
	case Weekly:
		y, w := at.ISOWeek()
		start = start.AddDate(0, 0, -(int(start.Weekday())+6)%7)
		return filepath.Join(base, fmt.Sprintf("%04d", y), fmt.Sprintf("%s.w%02d", prefix, w)), 0, start.AddDate(0, 0, 7), nil
	case Monthly:
		start = time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, zone)
		return filepath.Join(base, year, fmt.Sprintf("%s.m%02d", prefix, at.Month())), 0, start.AddDate(0, 1, 0), nil
	case Yearly:
		start = time.Date(at.Year(), 1, 1, 0, 0, 0, 0, zone)
		return filepath.Join(base, fmt.Sprintf("%s.y%02d", prefix, at.Year()%100)), 0, start.AddDate(1, 0, 0), nil
	default:
		return "", 0, time.Time{}, fmt.Errorf("invalid aggregate resolution")
	}
}
