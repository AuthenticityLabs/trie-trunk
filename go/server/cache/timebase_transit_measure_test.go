package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

func timebaseFixture(t testing.TB, n int, sparse, withTimestamp bool) (*timebasePlan, []timebaseRow, []timebaseBlock) {
	t.Helper()
	l, raws, _, _, _ := measurementFixtureProfile(t, n, sparse, "range-sized")
	d := l.Definition
	if withTimestamp {
		d.Fields[3].Type = "TIMESTAMP_MS"
		d.Fields[3].Name = "observed_at"
	}
	target, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	p := compileTimebaseExperiment(target)
	rows := make([]timebaseRow, n)
	var blocks []timebaseBlock
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 2, 0)
	var f Frame
	blockStart := 0
	for i, raw := range raws {
		if err = l.Scan(raw, &f); err != nil {
			t.Fatal(err)
		}
		ms := from.UnixMilli() + int64(i)*(to.UnixMilli()-from.UnixMilli())/int64(n)
		now := time.UnixMilli(ms).UTC()
		base := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		if i == 0 || i-blockStart == 4096 || base != blocks[len(blocks)-1].base {
			blocks = append(blocks, timebaseBlock{base})
			blockStart = i
		}
		f.Values[l.Event] = uuid(ms, uint32(i))
		if withTimestamp {
			// One timestamp per record, including sparse updates' metadata envelope.
			f.Values[3] = Uint(uint64(ms+17), 8)
		}
		absolute, e := target.Encode(nil, f.Values)
		if e != nil {
			t.Fatal(e)
		}
		relative, e := p.transform(nil, absolute, base, false)
		if e != nil {
			t.Fatal(e)
		}
		restored, e := p.transform(nil, relative, base, true)
		if e != nil || !bytes.Equal(restored, absolute) {
			t.Fatal("roundtrip", e)
		}
		wantSaving := 2
		if withTimestamp {
			wantSaving += 4
		}
		if len(absolute)-len(relative) != wantSaving {
			t.Fatal("incorrect temporal savings")
		}
		rows[i] = timebaseRow{absolute, relative, len(blocks) - 1, ms}
	}
	// Model contiguous committed file bytes, rather than allocator-dependent
	// placement/capacity of one heap object per encoded frame. Outside timing.
	absBytes, relBytes := 0, 0
	for _, row := range rows {
		absBytes += len(row.absolute)
		relBytes += len(row.relative)
	}
	absoluteSlab := make([]byte, 0, absBytes)
	relativeSlab := make([]byte, 0, relBytes)
	for i := range rows {
		a, r := len(absoluteSlab), len(relativeSlab)
		absoluteSlab = append(absoluteSlab, rows[i].absolute...)
		relativeSlab = append(relativeSlab, rows[i].relative...)
		rows[i].absolute = absoluteSlab[a:len(absoluteSlab):len(absoluteSlab)]
		rows[i].relative = relativeSlab[r:len(relativeSlab):len(relativeSlab)]
	}
	return p, rows, blocks
}

func TestMeasureTimebaseTransit(t *testing.T) {
	n := measurementSize(t)
	sparse := os.Getenv("TRUNK_MEASURE_SHAPE") == "sparse"
	withTimestamp := os.Getenv("TRUNK_TIMEBASE_PROFILE") == "uuid_ts"
	p, rows, blocks := timebaseFixture(t, n, sparse, withTimestamp)
	queries := map[string][]int{"all": {}, "scattered_1pct": {}, "boundary_3h": {}, "single": {n / 2}}
	for i := range rows {
		queries["all"] = append(queries["all"], i)
		if i%100 == 0 {
			queries["scattered_1pct"] = append(queries["scattered_1pct"], i)
		}
	}
	from := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC).UnixMilli()
	to := from + 3*3600000
	lo := sort.Search(n, func(i int) bool { return rows[i].ms >= from })
	hi := sort.Search(n, func(i int) bool { return rows[i].ms >= to })
	for i := lo; i < hi; i++ {
		queries["boundary_3h"] = append(queries["boundary_3h"], i)
	}
	modes := []string{"absolute_forward", "relative_forward", "relative_expand", "absolute_compact"}
	orderQueries := []string{"all", "scattered_1pct", "boundary_3h", "single"}
	sizes := map[string]timebaseOutput{}
	timings := map[string]timing{}
	allocs := map[string]float64{}
	repetitions := map[string]int{}
	hashes := map[string]string{}
	buffer := make([]byte, 0, 65536)
	sink := func(b []byte) error { compactExperimentSink += uint64(len(b)) + uint64(b[len(b)-1]); return nil }
	for _, query := range orderQueries {
		selected := queries[query]
		repeats := 1
		if len(selected) < 1000 {
			repeats = 1000 / max(1, len(selected))
		}
		repetitions[query] = repeats
		for _, mode := range modes {
			seen := 0
			digest := sha256.New()
			output, e := p.collect(rows, blocks, selected, mode, buffer, func(packet []byte) error {
				if len(packet) < 8 {
					return fmt.Errorf("short packet")
				}
				profile := order.Uint32(packet)
				count := int(order.Uint32(packet[4:]))
				at := 8
				var base int64
				if profile == 1 {
					if len(packet) < 16 {
						return fmt.Errorf("missing base")
					}
					base = int64(order.Uint64(packet[8:]))
					at = 16
				}
				for i := 0; i < count; i++ {
					length, err := p.frameLength(packet[at:], profile == 1)
					if err != nil || seen >= len(selected) {
						return fmt.Errorf("invalid frame/count: %v", err)
					}
					raw := packet[at : at+length]
					at += length
					if profile == 1 {
						var err error
						raw, err = p.transform(nil, raw, base, true)
						if err != nil {
							return err
						}
					}
					if !bytes.Equal(raw, rows[selected[seen]].absolute) {
						return fmt.Errorf("received record differs")
					}
					digest.Write(raw)
					seen++
				}
				if at != len(packet) {
					return fmt.Errorf("trailing bytes")
				}
				return nil
			})
			if e != nil || seen != len(selected) {
				t.Fatal("package validation", e, seen)
			}
			key := query + "/" + mode
			sizes[key] = output
			hashes[key] = hex.EncodeToString(digest.Sum(nil))
			allocs[key] = testing.AllocsPerRun(2, func() {
				if _, e := p.collect(rows, blocks, selected, mode, buffer, sink); e != nil {
					panic(e)
				}
			})
		}
		runtime.GC()
		samples := map[string][]int64{}
		for round := 0; round < 12; round++ {
			for j := range modes {
				mode := modes[(j+round)%len(modes)]
				start := time.Now()
				for k := 0; k < repeats; k++ {
					if _, e := p.collect(rows, blocks, selected, mode, buffer, sink); e != nil {
						t.Fatal(e)
					}
				}
				samples[mode] = append(samples[mode], time.Since(start).Nanoseconds())
			}
		}
		for _, mode := range modes {
			timings[query+"/"+mode] = timingSamples(samples[mode])
		}
	}
	var abs, rel int64
	for _, r := range rows {
		abs += int64(len(r.absolute))
		rel += int64(len(r.relative))
	}
	counts := map[string]int{}
	for q, rows := range queries {
		counts[q] = len(rows)
	}
	saveMeasurement(t, map[string]any{"events": n, "sparse": sparse, "profile": os.Getenv("TRUNK_TIMEBASE_PROFILE"), "definition": p.layout.Definition, "source_blocks": len(blocks), "source_buffers": "contiguous absolute and relative slabs", "source_block_max_rows": 4096, "absolute_frame_bytes": abs, "relative_frame_bytes": rel, "relative_base_bytes": 8 * len(blocks), "output": sizes, "codec": timings, "repeats_per_sample": repetitions, "selected_counts": counts, "received_sha256": hashes, "allocations_per_query": allocs, "samples": 12, "all_records_validated": true, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "scope": "in-memory output assembly after candidate selection; no disk, index search, HTTP, compression, or actual .idx/.pop layout changes"})
}
