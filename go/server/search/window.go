package search

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func ParseTime(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	now = now.UTC()
	switch strings.ToLower(value) {
	case "now":
		return now, nil
	case "today":
		return now.Truncate(24 * time.Hour), nil
	case "yesterday":
		return now.Truncate(24*time.Hour).AddDate(0, 0, -1), nil
	}
	offset := strings.TrimPrefix(strings.ToLower(value), "now")
	if strings.HasPrefix(offset, "-") || strings.HasPrefix(offset, "+") {
		duration, err := parseOffset(offset)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid relative time %q: %w", value, err)
		}
		return now.Add(duration), nil
	}
	if !strings.Contains(value, ",") {
		for _, layout := range [...]string{
			time.RFC3339Nano,
			"2006-01-02T15:04Z07:00",
			"20060102T15:04:05Z07:00",
			"20060102T15:04Z07:00",
			"2006-01-02T15:04:05", "2006-01-02T15:04",
			"2006-01-02 15:04:05", "2006-01-02 15:04",
			"20060102T15:04:05", "20060102T15:04",
			"2006-01-02", "20060102",
		} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.UTC(), nil
			}
		}
		for _, layout := range [...]string{"15:04:05", "15:04"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(), time.UTC), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q; use a year-first date, HH:MM[:SS], now, today, yesterday or now-15m; timezone defaults to UTC", value)
}

func ParseWindow(value string, now time.Time) (time.Time, time.Time, error) {
	a, b, hasEnd := strings.Cut(value, ",")
	if strings.TrimSpace(a) == "" || hasEnd && (strings.TrimSpace(b) == "" || strings.Contains(b, ",")) {
		return time.Time{}, time.Time{}, fmt.Errorf("window requires a start timestamp and an optional comma-separated end")
	}
	if !hasEnd {
		start, err := ParseTime(a, now)
		if err == nil {
			return start, time.Time{}, nil
		}
		parts := strings.Fields(a)
		if len(parts) != 2 {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start time: %w", err)
		}
		a, b = parts[0], parts[1]
	}
	start, err := ParseTime(a, now)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start time: %w", err)
	}
	end, err := ParseTime(b, now)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end time: %w", err)
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("search end time must be after start time")
	}
	return start, end, nil
}

func parseOffset(value string) (time.Duration, error) {
	unit := time.Duration(0)
	switch value[len(value)-1] {
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	default:
		return time.ParseDuration(value)
	}
	count, err := strconv.ParseInt(value[:len(value)-1], 10, 64)
	if err != nil || count > (1<<63-1)/int64(unit) || count < (-1<<63)/int64(unit) {
		return 0, fmt.Errorf("days and weeks require a whole number within duration range")
	}
	return time.Duration(count) * unit, nil
}
