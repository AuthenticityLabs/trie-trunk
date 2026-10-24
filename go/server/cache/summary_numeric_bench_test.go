package cache

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// Previous all-U64 format exists only in tests, not as a production fallback.
func baselineSummaryDay(day, from, to int64, sums []Summary, compressed bool) ([]byte, error) {
	var chunks [24][]byte
	size := 304
	for _, s := range sums {
		raw, e := baselineEncodeSummary(s)
		if e != nil {
			return nil, e
		}
		chunks[(s.Columns.Period[0]-day)/TimeHourMS] = raw
		size += len(raw)
	}
	body := make([]byte, size)
	order.PutUint64(body, uint64(from))
	order.PutUint64(body[8:], uint64(to))
	cursor := 304
	for h, raw := range chunks {
		d := 16 + h*12
		t := day + int64(h)*TimeHourMS
		if t >= from && t < to {
			order.PutUint32(body[d+8:], 1)
		}
		if raw == nil {
			continue
		}
		order.PutUint32(body[d:], uint32(cursor))
		order.PutUint32(body[d+4:], uint32(len(raw)))
		copy(body[cursor:], raw)
		cursor += len(raw)
	}
	out := body
	if compressed {
		var b bytes.Buffer
		gz, _ := gzip.NewWriterLevel(&b, 6)
		gz.Write(body)
		gz.Close()
		out = b.Bytes()
	}
	raw := make([]byte, 32+len(out))
	copy(raw, "SUMDAY00")
	if compressed {
		raw[8] = 1
		raw[9] = 1
	}
	order.PutUint32(raw[12:], uint32(len(raw)))
	order.PutUint32(raw[16:], uint32(len(body)))
	order.PutUint32(raw[20:], crc32.Checksum(body, checksum))
	order.PutUint64(raw[24:], uint64(day))
	copy(raw[32:], out)
	return raw, nil
}
func baselineDecodeDay(raw []byte) (sums []Summary, err error) {
	body := raw[32:]
	if raw[8] == 1 {
		gz, e := gzip.NewReader(bytes.NewReader(body))
		if e != nil {
			return nil, e
		}
		body = make([]byte, int(order.Uint32(raw[16:])))
		_, e = io.ReadFull(gz, body)
		if e == nil {
			var extra [1]byte
			_, e = gz.Read(extra[:])
			if e == io.EOF {
				e = nil
			}
		}
		gz.Close()
		if e != nil {
			return nil, e
		}
	}
	if crc32.Checksum(body, checksum) != order.Uint32(raw[20:]) {
		return nil, fmt.Errorf("baseline CRC")
	}
	for h := 0; h < 24; h++ {
		at, n := order.Uint32(body[16+h*12:]), order.Uint32(body[20+h*12:])
		if n == 0 {
			continue
		}
		var s Summary
		if e := baselineDecodeSummary(body[at:at+n], &s); e != nil {
			return nil, e
		}
		sums = append(sums, s)
	}
	return
}
func BenchmarkSummaryNumericCodec(b *testing.B) {
	at, dense := summaryBenchDay(b)
	// Already-sealed representative low-volume day from the prior deployment.
	sample, e := os.ReadFile("../../../benchmarks/storage/2026-10-24-daily-summaries/sample-1.sumday")
	if e != nil {
		b.Fatal(e)
	}
	sparse, e := baselineDecodeDay(sample)
	if e != nil {
		b.Fatal(e)
	}
	demo, e := os.ReadFile("../../../benchmarks/storage/2026-10-24-daily-summaries/sample-2.sumday")
	if e != nil {
		b.Fatal(e)
	}
	demoSums, e := baselineDecodeDay(demo)
	if e != nil {
		b.Fatal(e)
	}
	// A tightly distributed numeric field with a large offset, not just small ints.
	narrow := make([]Summary, len(dense))
	for h, s := range dense {
		var out Summary
		raw, _ := baselineEncodeSummary(s)
		if e = baselineDecodeSummary(raw, &out); e != nil {
			b.Fatal(e)
		}
		for i := range out.Columns.Min {
			if out.Columns.DurationCount[i] > 0 {
				out.Columns.Min[i] += 1000000000
				out.Columns.Max[i] += 1000000000
				out.Columns.Sum[i] += 1000000000 * out.Columns.DurationCount[i]
			}
		}
		for i := range out.Statistics {
			for j := range out.Statistics[i].Durations {
				out.Statistics[i].Durations[j][0] += 1000000000
			}
		}
		narrow[h] = out
	}
	type report struct {
		Workload, Mode string
		Decoded, Gzip  int
		Widths         []int
		Bases          []uint64
	}
	var reports []report
	for _, fixture := range []struct {
		name string
		day  int64
		sums []Summary
	}{{"sparse-day", int64(order.Uint64(sample[24:])), sparse}, {"demo-20", int64(order.Uint64(demo[24:])), demoSums}, {"dense-240k", at.UnixMilli(), dense}, {"large-base-240k", at.UnixMilli(), narrow}, {"varied-240k", at.UnixMilli(), summaryRandomDay(at.UnixMilli(), false)}, {"full-u32-240k", at.UnixMilli(), summaryRandomDay(at.UnixMilli(), true)}} {
		for _, mode := range []string{"u64", "narrow", "based", "based-shuffle"} {
			encode := func(compressed bool) ([]byte, error) {
				if mode == "u64" {
					return baselineSummaryDay(fixture.day, fixture.day, fixture.day+summaryDayMS, fixture.sums, compressed)
				}
				return encodeSummaryDayFiltered(fixture.day, fixture.day, fixture.day+summaryDayMS, fixture.sums, compressed, 6, mode == "based" || mode == "based-shuffle", mode == "based-shuffle")
			}
			plain, e := encode(false)
			if e != nil {
				b.Fatal(e)
			}
			packed, e := encode(true)
			if e != nil {
				b.Fatal(e)
			}
			row := report{Workload: fixture.name, Mode: mode, Decoded: len(plain), Gzip: len(packed)}
			if mode != "u64" {
				enc, _ := chooseSummaryEncoding(fixture.sums, mode == "based" || mode == "based-shuffle")
				for _, c := range enc {
					row.Widths = append(row.Widths, c.Width)
					row.Bases = append(row.Bases, c.Base)
				}
			}
			reports = append(reports, row)
			if dir := os.Getenv("TRUNK_SUMMARY_NUMERIC_REPORT"); dir != "" {
				os.MkdirAll(dir, 0755)
				os.WriteFile(filepath.Join(dir, fixture.name+"-"+mode+".sumday"), packed, 0644)
			}
			decode := func() error {
				var got []Summary
				var err error
				if mode == "u64" {
					got, err = baselineDecodeDay(packed)
				} else {
					_, _, _, got, err = decodeSummaryDay(packed)
				}
				if err != nil {
					return err
				}
				if len(got) != len(fixture.sums) {
					return fmt.Errorf("round trip length")
				}
				return nil
			}
			if e = decode(); e != nil {
				b.Fatal(e)
			}
			if mode != "u64" {
				_, _, _, got, _ := decodeSummaryDay(packed)
				if !reflect.DeepEqual(got, fixture.sums) {
					b.Fatal("round trip differs")
				}
			}
			b.Run(fixture.name+"/"+mode+"/encode", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(len(packed)), "bytes/day")
				for i := 0; i < b.N; i++ {
					if _, e := encode(true); e != nil {
						b.Fatal(e)
					}
				}
			})
			b.Run(fixture.name+"/"+mode+"/decode", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if e := decode(); e != nil {
						b.Fatal(e)
					}
				}
			})
		}
	}
	if dir := os.Getenv("TRUNK_SUMMARY_NUMERIC_REPORT"); dir != "" {
		raw, _ := json.MarshalIndent(reports, "", "  ")
		os.WriteFile(filepath.Join(dir, "sizes.json"), raw, 0644)
	}
}

func summaryRandomDay(day int64, full bool) []Summary {
	rng := rand.New(rand.NewSource(42))
	var out []Summary
	for h := 0; h < 24; h++ {
		groups := map[aggregateKey]Aggregate{}
		hist := map[aggregateKey]map[uint64]uint64{}
		for i := 0; i < 10000; i++ {
			key := aggregateKey{day + int64(h)*TimeHourMS + int64(i%12)*sumBucketMS, uint16((i / 12) % 2)}
			value := uint64(rng.Uint32())
			if !full {
				value = 1000 + value%500000
				if i%100 == 0 {
					value += 50000000
				}
			}
			var duration *uint64
			if i%3 != 0 {
				duration = &value
			}
			a := groups[key]
			if e := a.add(duration); e != nil {
				panic(e)
			}
			groups[key] = a
			if duration != nil {
				if hist[key] == nil {
					hist[key] = map[uint64]uint64{}
				}
				hist[key][value]++
			}
		}
		s := makeSummary(Run{Start: 1, End: 10001}, groups)
		s.DurationWidth = 4
		for i := 0; i < s.Columns.Len(); i++ {
			key := aggregateKey{s.Columns.Period[i], s.Columns.Reference[i]}
			g := statisticsGroup{Period: key.Period, Labels: [4]string{"service", "resource", fmt.Sprint(key.Reference), "INFO"}, Count: s.Columns.Count[i]}
			for v, n := range hist[key] {
				g.Durations = append(g.Durations, [2]uint64{v, n})
			}
			sort.Slice(g.Durations, func(i, j int) bool { return g.Durations[i][0] < g.Durations[j][0] })
			s.Statistics = append(s.Statistics, g)
		}
		out = append(out, s)
	}
	return out
}
