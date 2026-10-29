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
	"time"
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

// legacyColumnarDay reproduces the immediately preceding SUMDAY00 encoder for
// a matched-format benchmark. It is test-only and is never accepted by runtime.
func legacyColumnarDay(day, from, to int64, sums []Summary, compressed bool, level int) ([]byte, error) {
	enc, err := chooseSummaryEncoding(sums, compressed)
	if err != nil {
		return nil, err
	}
	totals, errors, err := summaryTotals(sums)
	if err != nil {
		return nil, err
	}
	const directory = 16 + 24*12 + summaryRollupSize + summaryEncodingSize
	var chunks [24][]byte
	size := directory
	for _, sum := range sums {
		raw, e := encodeSummaryColumns(sum, &enc)
		if e != nil {
			return nil, e
		}
		if compressed {
			if e = shuffleSummary(raw, enc, false); e != nil {
				return nil, e
			}
		}
		h := int((utcHour(sum.Columns.Period[0]) - day) / TimeHourMS)
		if h < 0 || h >= len(chunks) || chunks[h] != nil {
			return nil, fmt.Errorf("invalid legacy hour")
		}
		chunks[h] = raw
		size += len(raw)
	}
	body := make([]byte, size)
	order.PutUint64(body, uint64(from))
	order.PutUint64(body[8:], uint64(to))
	putSummaryTotals(body[304:], totals, errors)
	enc.put(body[304+summaryRollupSize:])
	cursor := directory
	for h, raw := range chunks {
		d := 16 + h*12
		at := day + int64(h)*TimeHourMS
		if at >= from && at < to {
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
	stored := body
	if compressed {
		var out bytes.Buffer
		z, e := gzip.NewWriterLevel(&out, level)
		if e != nil {
			return nil, e
		}
		if _, e = z.Write(body); e != nil {
			return nil, e
		}
		if e = z.Close(); e != nil {
			return nil, e
		}
		stored = out.Bytes()
	}
	raw := make([]byte, 32+len(stored))
	copy(raw, "SUMDAY00")
	if compressed {
		raw[8], raw[9], raw[10] = 1, 1, 1
	}
	order.PutUint32(raw[12:], uint32(len(raw)))
	order.PutUint32(raw[16:], uint32(len(body)))
	order.PutUint32(raw[20:], crc32.Checksum(body, checksum))
	order.PutUint64(raw[24:], uint64(day))
	copy(raw[32:], stored)
	return raw, nil
}

func legacyDecodeColumnarDay(raw []byte) (sums []Summary, err error) {
	body := raw[32:]
	if raw[8] == 1 {
		z, e := gzip.NewReader(bytes.NewReader(body))
		if e != nil {
			return nil, e
		}
		body, e = io.ReadAll(io.LimitReader(z, maxSummaryDay+1))
		closeErr := z.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if crc32.Checksum(body, checksum) != order.Uint32(raw[20:]) {
		return nil, fmt.Errorf("legacy checksum")
	}
	enc, err := readSummaryEncoding(body[304+summaryRollupSize:])
	if err != nil {
		return nil, err
	}
	const directory = 16 + 24*12 + summaryRollupSize + summaryEncodingSize
	for h := 0; h < 24; h++ {
		d := body[16+h*12:]
		a, n := int(order.Uint32(d)), int(order.Uint32(d[4:]))
		if n == 0 {
			continue
		}
		if a < directory || n > len(body)-a {
			return nil, fmt.Errorf("legacy member range")
		}
		chunk := bytes.Clone(body[a : a+n])
		if raw[10] == 1 {
			if err = shuffleSummary(chunk, enc, true); err != nil {
				return nil, err
			}
		}
		var sum Summary
		if err = decodeSummaryColumns(chunk, &sum, &enc); err != nil {
			return nil, err
		}
		sums = append(sums, sum)
	}
	return sums, nil
}

func BenchmarkSummaryDayPACTransition(b *testing.B) {
	at, sums := summaryBenchDay(b)
	for _, version := range []string{"sumday", "pil-pac"} {
		for _, level := range []int{0, 1, 6} {
			compressed := level != 0
			codecLevel := max(level, 1)
			encode := func() ([]byte, error) {
				if version == "sumday" {
					return legacyColumnarDay(at.UnixMilli(), at.UnixMilli(), at.Add(24*time.Hour).UnixMilli(), sums, compressed, codecLevel)
				}
				return encodeSummaryDayLevel(at.UnixMilli(), at.UnixMilli(), at.Add(24*time.Hour).UnixMilli(), sums, compressed, codecLevel)
			}
			raw, err := encode()
			if err != nil {
				b.Fatal(err)
			}
			decode := func() error {
				var got []Summary
				var e error
				if version == "sumday" {
					got, e = legacyDecodeColumnarDay(raw)
				} else {
					_, _, _, got, e = decodeSummaryDay(raw)
				}
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(got, sums) {
					return fmt.Errorf("semantic mismatch")
				}
				return nil
			}
			if err = decode(); err != nil {
				b.Fatal(err)
			}
			b.Run(fmt.Sprintf("%s/gzip%d/encode", version, level), func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(len(raw)), "bytes/day")
				for i := 0; i < b.N; i++ {
					if _, err := encode(); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(fmt.Sprintf("%s/gzip%d/decode", version, level), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if err := decode(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
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
