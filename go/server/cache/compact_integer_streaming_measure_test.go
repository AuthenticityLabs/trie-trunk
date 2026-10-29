package cache

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"trie-trunk/go/codec"
)

func TestMeasureStreamingIntegers(t *testing.T) {
	n := measurementSize(t)
	sparse := os.Getenv("TRUNK_MEASURE_SHAPE") == "sparse"
	profile := os.Getenv("TRUNK_COMPACT_PROFILE")
	signed := strings.HasSuffix(profile, "signed") && !strings.HasSuffix(profile, "unsigned")
	fullRange := strings.HasPrefix(profile, "full-range")
	switch profile {
	case "bounded-unsigned", "bounded-signed", "full-range-unsigned", "full-range-signed":
	default:
		t.Fatal("unknown compact profile")
	}
	seedLayout, _, _, _, _ := measurementFixtureProfile(t, 1, false, "range-sized")
	d := seedLayout.Definition
	d.Resource.Roles = Roles{Event: "event_id"}
	d.Resource.Path = nil
	d.Resource.Aggregations = nil
	indices := []int{1, 2, 3, 8, 9, 11}
	bounds := measurementIntegerBounds(n)
	for _, i := range indices {
		max := bounds[i]
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
		if fullRange {
			kind = "U64"
			if signed {
				kind = "I64"
			}
		}
		d.Fields[i].Type = kind
	}
	narrow, err := compileCompactIntegerExperiment(d)
	if err != nil {
		t.Fatal(err)
	}
	wideDef := d
	wideDef.Fields = append([]Field(nil), d.Fields...)
	for _, i := range indices {
		wideDef.Fields[i].Type = "U64"
		if signed {
			wideDef.Fields[i].Type = "I64"
		}
	}
	wide, err := compileCompactIntegerExperiment(wideDef)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := CompilePositionalCBOR(narrow.base)
	if err != nil {
		t.Fatal(err)
	}
	type variant struct {
		name       string
		layout     *compactIntegerLayout
		compact    bool
		fast       *fastIntegerLayout
		checksOnly bool
		stream     *streamIntegerLayout
		values     [][]Value
		raw        [][]byte
		bytes      int64
		f          Frame
	}
	variants := []*variant{{name: "pil_fixed", layout: narrow}, {name: "pil_compact", layout: narrow, compact: true}, {name: "pil_fixed64", layout: wide}, {name: "pil_compact64", layout: wide, compact: true}}

	for _, v := range append([]*variant(nil), variants...) {
		variants = append(variants, &variant{name: v.name + "_fast", layout: v.layout, compact: v.compact, fast: compileFastIntegerExperiment(v.layout)})
	}
	for _, v := range append([]*variant(nil), variants...) {
		if v.compact && v.fast == nil {
			variants = append(variants, &variant{name: v.name + "_loads", layout: v.layout, compact: true, checksOnly: true})
		}
	}
	for _, v := range append([]*variant(nil), variants...) {
		if v.compact && v.fast == nil && !v.checksOnly {
			p := compileFastIntegerExperiment(v.layout)
			variants = append(variants, &variant{name: v.name + "_stream", layout: v.layout, compact: true, fast: p, stream: compileStreamIntegerExperiment(p)})
		}
	}
	encode := func(v *variant, dst []byte, values []Value) ([]byte, error) {
		if v.stream != nil {
			return v.stream.encode(dst, values)
		}
		if v.checksOnly {
			return v.layout.encodeFastLoads(dst, values)
		}
		if v.fast != nil {
			return v.fast.encode(dst, values, v.compact)
		}
		if v.compact {
			return v.layout.encode(dst, values)
		}
		return v.layout.base.Layout.Encode(dst, values)
	}
	scan := func(v *variant, raw []byte, f *Frame) error {
		if v.fast != nil {
			return v.fast.scanMode(raw, f, v.compact)
		}
		if v.compact {
			return v.layout.scan(raw, f)
		}
		return v.layout.base.Scan(raw, f)
	}
	var records []codec.Record
	var cborRaw [][]byte
	var rows []int
	var cborBytes int64
	var cf CBORFrame
	batch := min(n, 1024)
	rng := rand.New(rand.NewPCG(0x70696c, 0x63626f72))
	bufs := make([][]byte, len(variants))
	for i := 0; i < n; i++ {
		numbers := []uint64{uint64(i % 10000), uint64(i/10000 + 1), uint64(50 + i*7919%100000), uint64(i % 100000), uint64(i % 5000), uint64(i % 4)}
		if fullRange {
			for j := range numbers {
				numbers[j] = rng.Uint64()
			}
		} else if signed {
			for j := range numbers {
				if (i+j)%2 == 1 {
					numbers[j] = ^numbers[j]
				}
			}
		}
		nv := make([]Value, 12)
		wv := make([]Value, 12)
		for j, field := range indices {
			nv[field] = Uint(numbers[j], narrow.fields[field].width)
			wv[field] = Uint(numbers[j], 8)
		}
		if sparse {
			for _, field := range []int{3, 9, 11} {
				nv[field] = Value{}
				wv[field] = Value{}
			}
			if i%2 != 0 {
				nv[8] = Value{}
				wv[8] = Value{}
			}
			nv[6] = Uint(uint64(i%3), 1)
			wv[6] = nv[6]
		} else {
			nv[7] = Text(fmt.Sprintf("Transaction %d completed", i))
			nv[10] = Text(fmt.Sprintf("payment-%02d", i%12))
			wv[7] = nv[7]
			wv[10] = nv[10]
		}
		record := compactExperimentRecord(narrow.base, nv)
		cb, err := marshalBlindCBOR(record)
		if err != nil {
			t.Fatal(err)
		}
		cborBytes += int64(len(cb))
		if err = direct.Scan(cb, &cf); err != nil {
			t.Fatal(err)
		}
		compactExperimentEqual(t, narrow.base, nv, cf.Values)
		sample := len(rows) < batch && (batch == 1 || i == len(rows)*(n-1)/(batch-1))
		for j, v := range variants {
			values := nv
			if v.layout == wide {
				values = wv
			}
			bufs[j], err = encode(v, bufs[j], values)
			if err != nil {
				t.Fatal(err)
			}
			if v.fast != nil || v.checksOnly {
				original := 0
				if v.layout == wide {
					original = 2
				}
				if v.compact {
					original++
				}
				if !bytes.Equal(bufs[j], bufs[original]) {
					t.Fatal("optimization changed wire bytes")
				}
			}
			v.bytes += int64(len(bufs[j]))
			if err = scan(v, bufs[j], &v.f); err != nil {
				t.Fatal(err)
			}
			compactExperimentEqual(t, v.layout.base, values, v.f.Values)
			if sample {
				v.values = append(v.values, values)
				v.raw = append(v.raw, bytes.Clone(bufs[j]))
			}
		}
		if sample {
			rows = append(rows, i)
			records = append(records, record)
			cborRaw = append(cborRaw, cb)
		}
	}
	type job struct {
		name string
		run  func() error
	}
	var jobs []job
	for _, v := range variants {
		jobs = append(jobs, job{v.name + "/encode", func() error {
			for _, values := range v.values {
				raw, e := encode(v, nil, values)
				if e != nil {
					return e
				}
				compactExperimentSink += uint64(len(raw))
			}
			return nil
		}})

		var output []byte
		jobs = append(jobs, job{v.name + "/encode_reuse", func() error {
			for _, values := range v.values {
				var err error
				output, err = encode(v, output, values)
				if err != nil {
					return err
				}
				compactExperimentSink += uint64(len(output))
			}
			return nil
		}})
		jobs = append(jobs, job{v.name + "/scan", func() error {
			for _, raw := range v.raw {
				if e := scan(v, raw, &v.f); e != nil {
					return e
				}
			}
			return nil
		}})
		jobs = append(jobs, job{v.name + "/scan_access", func() error {
			for _, raw := range v.raw {
				if e := scan(v, raw, &v.f); e != nil {
					return e
				}
				compactExperimentSink += compactExperimentAccess(v.layout, v.f.Values)
			}
			return nil
		}})
	}
	jobs = append(jobs, job{"cbor/encode", func() error {
		for _, r := range records {
			raw, e := marshalBlindCBOR(r)
			if e != nil {
				return e
			}
			compactExperimentSink += uint64(len(raw))
		}
		return nil
	}})
	jobs = append(jobs, job{"cbor/scan", func() error {
		for _, raw := range cborRaw {
			if e := direct.Scan(raw, &cf); e != nil {
				return e
			}
		}
		return nil
	}})
	jobs = append(jobs, job{"cbor/scan_access", func() error {
		for _, raw := range cborRaw {
			if e := direct.Scan(raw, &cf); e != nil {
				return e
			}
			compactExperimentSink += compactExperimentAccess(narrow, cf.Values)
		}
		return nil
	}})
	allocs := map[string]float64{}
	for _, j := range jobs {
		allocs[j.name] = testing.AllocsPerRun(2, func() {
			if e := j.run(); e != nil {
				panic(e)
			}
		}) / float64(batch)
	}
	runtime.GC()
	const repeats = 8
	samples := map[string][]int64{}
	for round := 0; round < len(jobs); round++ {
		for i := range jobs {
			j := jobs[(i+round)%len(jobs)]
			start := time.Now()
			for k := 0; k < repeats; k++ {
				if e := j.run(); e != nil {
					t.Fatal(e)
				}
			}
			samples[j.name] = append(samples[j.name], time.Since(start).Nanoseconds())
		}
	}
	timings := map[string]timing{}
	for name, s := range samples {
		timings[name] = timingSamples(s)
	}
	sizes := map[string]int64{"cbor": cborBytes}
	for _, v := range variants {
		sizes[v.name] = v.bytes
	}
	saveMeasurement(t, map[string]any{"events": n, "sparse": sparse, "profile": profile, "definition": d, "wide_definition": wideDef, "payload_bytes": sizes, "codec": timings, "allocations_per_record": allocs, "batch_rows": rows, "records_per_timing_sample": batch * repeats, "samples": len(jobs), "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "all_records_validated": true, "scope": "paired reference/optimized mask override; in-memory codec only; no storage/index/IPC/network", "format": "test-only, NOT existing PIL wire format"})
}
