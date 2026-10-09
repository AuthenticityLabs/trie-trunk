package search

import (
	"testing"
	"time"
)

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 9, 25, 16, 30, 15, 123456789, time.UTC)
	start, end, err := ParseWindow("20260923T23:59,20260924T00:01", now)
	if err != nil || start.Location() != time.UTC || end.Sub(start) != 2*time.Minute {
		t.Fatal(start, end, err)
	}
	start, end, err = ParseWindow(" now-15m , now ", now)
	if err != nil || !end.Equal(now) || end.Sub(start) != 15*time.Minute {
		t.Fatal(start, end, err)
	}
	start, end, err = ParseWindow("now", now)
	if err != nil || !start.Equal(now) || !end.IsZero() {
		t.Fatal(start, end, err)
	}
	start, end, err = ParseWindow("2026-09-25T14:00-04:00,2026-09-25T19:00Z", now)
	if err != nil || end.Sub(start) != time.Hour {
		t.Fatal(start, end, err)
	}
	for _, value := range []string{"14:00 15:00", "14:00,15:00", "14:00 , 15:00"} {
		start, end, err = ParseWindow(value, now)
		if err != nil || start.Hour() != 14 || end.Sub(start) != time.Hour {
			t.Fatal(value, start, end, err)
		}
	}
	start, end, err = ParseWindow("2026-09-24 14:00", now)
	if err != nil || start.Day() != 24 || start.Hour() != 14 || !end.IsZero() {
		t.Fatal(start, end, err)
	}
	for _, value := range []string{
		"", ",20260924T00:00", "20260923T00:00,", "today,yesterday,now",
		"20260230T00:00,20260301T00:00", "20260923T24:00,20260924T00:00",
		"20260924T00:00,20260923T00:00", "20260923T00:00,20260923T00:00",
		"today,nope", "now,now", "14:00:15:00", "23:00,01:00",
		"14:00 15:00 16:00", "14:00,15:00 16:00", "pretty", "100",
		"2026-09-25T14:00-04:00,2026-09-25T17:00Z",
	} {
		if _, _, err := ParseWindow(value, now); err == nil {
			t.Fatal("accepted", value)
		}
	}
}

func TestParseTime(t *testing.T) {
	now := time.Date(2026, 9, 25, 16, 30, 15, 123456789, time.UTC)
	for _, test := range []struct{ input, want string }{
		{"14:30", "2026-09-25T14:30:00Z"},
		{"14:30:15.123456789", "2026-09-25T14:30:15.123456789Z"},
		{"20260925T14:30", "2026-09-25T14:30:00Z"},
		{"20260925T14:30:15.123", "2026-09-25T14:30:15.123Z"},
		{"20260925T14:30-04:00", "2026-09-25T18:30:00Z"},
		{"20260925T14:30:15Z", "2026-09-25T14:30:15Z"},
		{"2026-09-25T14:30", "2026-09-25T14:30:00Z"},
		{"2026-09-25T14:30:15", "2026-09-25T14:30:15Z"},
		{"2026-09-25T14:30:15.123456789-04:00", "2026-09-25T18:30:15.123456789Z"},
		{"2026-09-25T00:30+02:00", "2026-09-24T22:30:00Z"},
		{"2026-09-25 14:30", "2026-09-25T14:30:00Z"},
		{"2026-09-25 14:30:15.123", "2026-09-25T14:30:15.123Z"},
		{"20260925", "2026-09-25T00:00:00Z"},
		{" 2026-09-25 ", "2026-09-25T00:00:00Z"},
		{"2024-02-29", "2024-02-29T00:00:00Z"},
		{"now", "2026-09-25T16:30:15.123456789Z"},
		{"NOW", "2026-09-25T16:30:15.123456789Z"},
		{"now-15m", "2026-09-25T16:15:15.123456789Z"},
		{"-15m", "2026-09-25T16:15:15.123456789Z"},
		{"now-1h30m", "2026-09-25T15:00:15.123456789Z"},
		{"now+1s", "2026-09-25T16:30:16.123456789Z"},
		{"now-7d", "2026-09-18T16:30:15.123456789Z"},
		{"-1w", "2026-09-18T16:30:15.123456789Z"},
		{"today", "2026-09-25T00:00:00Z"},
		{"yesterday", "2026-09-24T00:00:00Z"},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := ParseTime(test.input, now)
			if err != nil || got.Location() != time.UTC || got.Format(time.RFC3339Nano) != test.want {
				t.Fatal(got, err)
			}
		})
	}
	for _, value := range []string{
		"", "03/04/2026", "20260230T00:00", "20260925T24:00", "14:60", "14:30:60",
		"2026-02-29", "2026-13-01", "2026-09-25junk", "last Friday evening",
		"now-", "now+", "-1mon", "-d", "-1.5d", "now-1d2h", "-15m@m", "@d",
		"-9999999999999999999999d", "+106752d", "-106752d", "+15251w",
		"14:30:15,123", "2026-09-25T14:30:15,123Z",
	} {
		if got, err := ParseTime(value, now); err == nil {
			t.Fatal("accepted", value, got)
		}
	}
}

func TestParseTimeUsesUTCDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 30, 0, 0, time.FixedZone("offset", 2*60*60))
	for input, want := range map[string]string{
		"today":     "2025-12-31T00:00:00Z",
		"yesterday": "2025-12-30T00:00:00Z",
		"23:00":     "2025-12-31T23:00:00Z",
	} {
		got, err := ParseTime(input, now)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Fatal(input, got, err)
		}
	}
	now = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	got, err := ParseTime("yesterday", now)
	if err != nil || got.Format(time.DateOnly) != "2024-02-29" {
		t.Fatal(got, err)
	}
}
