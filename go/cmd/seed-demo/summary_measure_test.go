package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Matches the expanded-JSONL footprint generator; no old-format reader is used.
func TestDemoDailySummaryMeasurement(t *testing.T) {
	out := os.Getenv("TRUNK_DAILY_SUMMARY_OUT")
	if out == "" {
		t.Skip("opt-in daily summary footprint")
	}
	if e := os.MkdirAll(out, 0755); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "caches")
	end := time.Date(2026, 10, 20, 18, 41, 34, 0, time.UTC)
	started := time.Now()
	manifest, e := seedWithClock(root, 50000, 30, end, func() time.Time { return end.Add(2 * time.Hour) })
	if e != nil {
		t.Fatal(e)
	}
	type daily struct {
		Source  string `json:"source"`
		Day     int64  `json:"day_ms"`
		Encoded int    `json:"encoded_bytes"`
		Decoded int    `json:"decoded_bytes"`
	}
	var packets []daily
	totals := map[string]int64{}
	byDay := map[int64]int64{}
	var rawHours, rawHourBytes, rollupBytes, rollupRows int
	e = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		totals[filepath.Ext(path)] += info.Size()
		if filepath.Ext(path) != ".sum" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source, _ := filepath.Rel(root, path)
		for pos := 72; pos < len(raw); {
			if pos+8 > len(raw) {
				return fmt.Errorf("short summary frame")
			}
			n := int(binary.LittleEndian.Uint32(raw[pos:]))
			pos += 8
			if n < 8 || pos+n > len(raw) {
				return fmt.Errorf("bad summary frame")
			}
			chunk := raw[pos : pos+n]
			pos += n
			if string(chunk[:8]) == "SUMROLL0" {
				rollupBytes += n + 8
				rollupRows += (n - 24) / 88
				continue
			}
			if string(chunk[:8]) == "SUMCOL00" {
				rawHours++
				rawHourBytes += 8 + n
				continue
			}
			if string(chunk[:8]) != "SUMDAY00" {
				return fmt.Errorf("unknown summary format")
			}
			day := int64(binary.LittleEndian.Uint64(chunk[24:]))
			decoded := int(binary.LittleEndian.Uint32(chunk[16:]))
			packets = append(packets, daily{source, day, n, decoded})
			byDay[day] += int64(n)
			// Save a bounded representative selection for client decoding/scan timing.
			if len(packets) <= 3 {
				if err = os.WriteFile(filepath.Join(out, fmt.Sprintf("sample-%d.sumday", len(packets))), chunk, 0644); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var lengths, allDays []int64
	var compressed, decoded int64
	for _, p := range packets {
		lengths = append(lengths, int64(p.Encoded))
		compressed += int64(p.Encoded)
		decoded += int64(p.Decoded)
	}
	for _, n := range byDay {
		allDays = append(allDays, n)
	}
	sort.Slice(lengths, func(i, j int) bool { return lengths[i] < lengths[j] })
	sort.Slice(allDays, func(i, j int) bool { return allDays[i] < allDays[j] })
	result := map[string]any{"calendar_rollup_bytes": rollupBytes, "calendar_rollup_rows": rollupRows, "manifest": manifest, "seed_and_verify_ms": time.Since(started).Milliseconds(), "logical_bytes_by_extension": totals, "sealed_packets": len(packets), "sealed_encoded_bytes": compressed, "sealed_decoded_bytes": decoded, "packet_min_median_max_bytes": []int64{lengths[0], lengths[len(lengths)/2], lengths[len(lengths)-1]}, "all_resources_day_min_median_max_bytes": []int64{allDays[0], allDays[len(allDays)/2], allDays[len(allDays)-1]}, "open_hour_chunks": rawHours, "open_hour_framed_bytes": rawHourBytes, "packets": packets}
	raw, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(out, "footprint.json"), raw, 0644); e != nil {
		t.Fatal(e)
	}
	t.Logf("sum=%d bytes; %d sealed packets %d compressed / %d decoded bytes; whole-day median %d bytes", totals[".sum"], len(packets), compressed, decoded, allDays[len(allDays)/2])
}
