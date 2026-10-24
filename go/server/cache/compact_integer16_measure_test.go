package cache

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func integer16Fixture(t *testing.T, n int, profile, shape string) (Definition, func(int) []Value) {
	t.Helper()
	if strings.HasPrefix(profile, "mixed-") {
		seed, _, _, _, _ := measurementFixtureProfile(t, 1, false, "range-sized")
		d := seed.Definition
		d.Resource.Roles = Roles{Event: "event_id"}
		d.Resource.Path = nil
		d.Resource.Aggregations = nil
		signed := profile == "mixed-signed"
		indices := []int{1, 2, 3, 8, 9, 11}
		for i, max := range measurementIntegerBounds(n) {
			kind := smallestUnsigned(max)
			if signed {
				switch {
				case max <= 127:
					kind = "I8"
				case max <= 32767:
					kind = "I16"
				case max <= 2147483647:
					kind = "I32"
				default:
					kind = "I64"
				}
			}
			d.Fields[i].Type = kind
		}
		l, e := Compile(d)
		if e != nil {
			t.Fatal(e)
		}
		return d, func(i int) []Value {
			nums := []uint64{uint64(i % 10000), uint64(i/10000 + 1), uint64(50 + i*7919%100000), uint64(i % 100000), uint64(i % 5000), uint64(i % 4)}
			v := make([]Value, 12)
			for j, field := range indices {
				u := nums[j]
				if signed && (i+j)%2 == 1 {
					u = ^u
				}
				v[field] = Uint(u, l.Widths[field])
			}
			if shape == "sparse" {
				for _, field := range []int{3, 9, 11} {
					v[field] = Value{}
				}
				if i%2 != 0 {
					v[8] = Value{}
				}
				v[6] = Uint(uint64(i%3), 1)
			} else {
				v[7] = Text(fmt.Sprintf("Transaction %d completed", i))
				v[10] = Text(fmt.Sprintf("payment-%02d", i%12))
			}
			return v
		}
	}
	signed := strings.HasPrefix(profile, "i16-")
	if !signed && !strings.HasPrefix(profile, "u16-") {
		t.Fatal("unknown 16-bit profile")
	}
	distribution := profile[4:]
	probability := -1
	if distribution != "uniform" {
		var err error
		probability, err = strconv.Atoi(distribution)
		if err != nil || probability < 0 || probability > 100 {
			t.Fatal("invalid probability")
		}
	}
	fields := 16
	if shape == "dense8" {
		fields = 8
	}
	kinds := make([]string, fields-1)
	for i := range kinds {
		kinds[i] = "U16"
		if signed {
			kinds[i] = "I16"
		}
	}
	d := compactTestDefinition(kinds...)
	rng := rand.New(rand.NewPCG(0x70696c16, 0x63626f7216))
	next := func() uint64 {
		if distribution == "uniform" {
			return uint64(rng.UintN(65536))
		}
		small := rng.IntN(100) < probability
		if small {
			u := rng.UintN(256)
			if signed {
				return uint64(uint16(int16(int8(u))))
			}
			return uint64(u)
		}
		if signed {
			return uint64(128 + rng.UintN(65280))
		}
		return uint64(256 + rng.UintN(65280))
	}
	return d, func(i int) []Value {
		v := make([]Value, fields)
		switch shape {
		case "dense8", "dense16":
			for j := 1; j < fields; j++ {
				v[j] = Uint(next(), 2)
			}
		case "sparse1", "sparse2":
			j := 1 + i%(fields-1)
			v[j] = Uint(next(), 2)
			if shape == "sparse2" {
				j = 1 + j%(fields-1)
				v[j] = Uint(next(), 2)
			}
		default:
			t.Fatal("unknown shape")
		}
		return v
	}
}

func TestMeasureInteger16(t *testing.T) {
	n := measurementSize(t)
	profile := os.Getenv("TRUNK_INT16_PROFILE")
	shape := os.Getenv("TRUNK_MEASURE_SHAPE")
	d, next := integer16Fixture(t, n, profile, shape)
	ref, e := compileCompactIntegerExperiment(d)
	if e != nil {
		t.Fatal(e)
	}
	p32 := compileFastIntegerExperiment(ref)
	p16, e := compileInteger16Experiment(d)
	if e != nil {
		t.Fatal(e)
	}
	type variant struct {
		name        string
		plan        *fastIntegerLayout
		stream      *streamIntegerLayout
		compact     bool
		specialized bool
		raw         [][]byte
		frame       Frame
		size        int64
	}
	variants := []*variant{{name: "fixed", plan: p32}, {name: "compact32", plan: p32, stream: compileStreamIntegerExperiment(p32), compact: true}, {name: "compact16_generic", plan: p16, stream: compileStreamIntegerExperiment(p16), compact: true}, {name: "compact16", plan: p16, stream: compileStreamIntegerExperiment(p16), compact: true, specialized: true}}
	encode := func(v *variant, dst []byte, values []Value, reuse bool) ([]byte, error) {
		if reuse && v.compact {
			if v.specialized {
				return v.stream.encode16(dst, values)
			}
			return v.stream.encode(dst, values)
		}
		if v.specialized {
			return v.plan.encode16(dst, values, v.compact)
		}
		return v.plan.encode(dst, values, v.compact)
	}
	scan := func(v *variant, raw []byte) error {
		if v.specialized {
			return v.plan.scanMode16(raw, &v.frame, v.compact)
		}
		return v.plan.scanMode(raw, &v.frame, v.compact)
	}
	batch := min(n, 1024)
	var valuesBatch [][]Value
	var rows []int
	buffers := make([][]byte, len(variants))
	var shorts, present int64
	for i := 0; i < n; i++ {
		values := next(i)
		shrink := 0
		for field, v := range values {
			if v.State == Direct && (d.Fields[field].Type == "U16" || d.Fields[field].Type == "I16") {
				present++
				if compactFastCode(v.Bytes, p16.fields[field].signed) == 0 {
					shorts++
					shrink++
				}
			}
		}
		sample := len(rows) < batch && (batch == 1 || i == len(rows)*(n-1)/(batch-1))
		for j, v := range variants {
			buffers[j], e = encode(v, buffers[j], values, false)
			if e != nil {
				t.Fatal(e)
			}
			if v.compact {
				streamed, err := encode(v, nil, values, true)
				if err != nil || !bytes.Equal(streamed, buffers[j]) {
					t.Fatal("planned/streamed bytes differ", err)
				}
			}
			v.size += int64(len(buffers[j]))
			if e = scan(v, buffers[j]); e != nil {
				t.Fatal(e)
			}
			compactExperimentEqual(t, ref.base, values, v.frame.Values)
			if sample {
				v.raw = append(v.raw, bytes.Clone(buffers[j]))
			}
		}
		if !bytes.Equal(buffers[2], buffers[3]) {
			t.Fatal("specialized 16-bit encoding changed bytes")
		}
		if len(buffers[1])-len(buffers[2]) != shrink {
			t.Fatal("16-bit savings were not exactly one byte per shortened value")
		}
		if shrink == 0 && !bytes.Equal(buffers[1], buffers[2]) {
			t.Fatal("native-only frame changed")
		}
		if sample {
			rows = append(rows, i)
			valuesBatch = append(valuesBatch, values)
		}
	}
	type job struct {
		name string
		run  func() error
	}
	var jobs []job
	for _, v := range variants {
		jobs = append(jobs, job{v.name + "/encode", func() error {
			for _, values := range valuesBatch {
				raw, err := encode(v, nil, values, false)
				if err != nil {
					return err
				}
				compactExperimentSink += uint64(len(raw))
			}
			return nil
		}})
		var out []byte
		jobs = append(jobs, job{v.name + "/encode_reuse", func() error {
			for _, values := range valuesBatch {
				var err error
				out, err = encode(v, out, values, true)
				if err != nil {
					return err
				}
				compactExperimentSink += uint64(len(out))
			}
			return nil
		}})
		jobs = append(jobs, job{v.name + "/scan", func() error {
			for _, raw := range v.raw {
				if err := scan(v, raw); err != nil {
					return err
				}
			}
			return nil
		}})
		jobs = append(jobs, job{v.name + "/scan_access", func() error {
			for _, raw := range v.raw {
				if err := scan(v, raw); err != nil {
					return err
				}
				compactExperimentSink += compactExperimentAccess(ref, v.frame.Values)
			}
			return nil
		}})
	}
	allocs := map[string]float64{}
	for _, j := range jobs {
		allocs[j.name] = testing.AllocsPerRun(2, func() {
			if err := j.run(); err != nil {
				panic(err)
			}
		}) / float64(batch)
	}
	runtime.GC()
	samples := map[string][]int64{}
	const repeats = 8
	for round := 0; round < 2*len(jobs); round++ {
		for i := range jobs {
			j := jobs[(i+round)%len(jobs)]
			start := time.Now()
			for k := 0; k < repeats; k++ {
				if err := j.run(); err != nil {
					t.Fatal(err)
				}
			}
			samples[j.name] = append(samples[j.name], time.Since(start).Nanoseconds())
		}
	}
	timings := map[string]timing{}
	for name, s := range samples {
		timings[name] = timingSamples(s)
	}
	sizes := map[string]int64{}
	for _, v := range variants {
		sizes[v.name] = v.size
	}
	saveMeasurement(t, map[string]any{"events": n, "profile": profile, "shape": shape, "definition": d, "population_bytes": sizes, "eligible16_values": present, "shortened16_values": shorts, "codec": timings, "allocations_per_record": allocs, "batch_rows": rows, "records_per_timing_sample": batch * repeats, "samples": 2 * len(jobs), "all_records_validated": true, "exact_one_byte_saving_verified": true, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "scope": "test-only mask16 extension; cached definition; in-memory codec; no indices, IPC, storage I/O, or network"})
}
