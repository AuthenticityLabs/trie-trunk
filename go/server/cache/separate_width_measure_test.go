package cache

import (
	"bytes"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestMeasureSeparateWidth(t *testing.T) {
	n := measurementSize(t)
	profile := os.Getenv("TRUNK_INT16_PROFILE")
	shape := os.Getenv("TRUNK_MEASURE_SHAPE")
	d, next := integer16Fixture(t, n, profile, shape)
	ref, e := compileCompactIntegerExperiment(d)
	if e != nil {
		t.Fatal(e)
	}
	p32, e := compileSeparateWidthExperiment(d, false)
	if e != nil {
		t.Fatal(e)
	}
	p16, e := compileSeparateWidthExperiment(d, true)
	if e != nil {
		t.Fatal(e)
	}
	type variant struct {
		name     string
		plan     *separateWidthLayout
		compact  bool
		raw      [][]byte
		frame    Frame
		size     int64
		controls int64
	}
	variants := []*variant{{name: "fixed", plan: p32}, {name: "width32", plan: p32, compact: true}, {name: "width16", plan: p16, compact: true}}
	encode := func(v *variant, dst []byte, values []Value, reuse bool) ([]byte, error) {
		if reuse && v.compact {
			return v.plan.encodeReuse(dst, values)
		}
		return v.plan.encode(dst, values, v.compact)
	}
	scan := func(v *variant, raw []byte) error { return v.plan.scanMode(raw, &v.frame, v.compact) }
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
			// Independent byte-accounting oracle using the unchanged logical states.
			bits, saved := 0, 0
			for field, x := range values {
				f := v.plan.fields[field]
				if v.compact && x.State == Direct && f.eligible {
					bits += f.selectorBits
					// Independent signed-width boundary oracle; no encoder helper.
					w := len(x.Bytes)
					u := unsigned(x.Bytes)
					if f.signed {
						z := int64(u<<uint(64-8*w)) >> uint(64-8*w)
						switch {
						case z >= -128 && z <= 127:
							w = 1
						case z >= -32768 && z <= 32767:
							w = 2
						case z >= -2147483648 && z <= 2147483647:
							w = 4
						default:
							w = 8
						}
					} else {
						switch {
						case u <= 255:
							w = 1
						case u <= 65535:
							w = 2
						case u <= 4294967295:
							w = 4
						default:
							w = 8
						}
					}
					saved += f.width - w
				}
			}
			controlBytes := (bits + 7) / 8
			v.controls += int64(controlBytes)
			if len(buffers[j]) != len(buffers[0])+controlBytes-saved {
				t.Fatal("incorrect selector overhead accounting")
			}
			if !bytes.Equal(buffers[j][:p32.mask], buffers[0][:p32.mask]) {
				t.Fatal("field states changed")
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
		// The extension's incremental size includes newly crossed selector-byte boundaries.
		bits32, bits16 := 0, 0
		for field, x := range values {
			if x.State == Direct {
				if p32.fields[field].eligible {
					bits32 += p32.fields[field].selectorBits
				}
				if p16.fields[field].eligible {
					bits16 += p16.fields[field].selectorBits
				}
			}
		}
		if len(buffers[1])-len(buffers[2]) != shrink-((bits16+7)/8-(bits32+7)/8) {
			t.Fatal("incremental overhead mismatch")
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
	controls := map[string]int64{}
	for _, v := range variants {
		sizes[v.name] = v.size
		controls[v.name] = v.controls
	}
	saveMeasurement(t, map[string]any{"events": n, "profile": profile, "shape": shape, "definition": d, "population_bytes": sizes, "eligible16_values": present, "shortened16_values": shorts, "codec": timings, "allocations_per_record": allocs, "batch_rows": rows, "records_per_timing_sample": batch * repeats, "samples": 2 * len(jobs), "all_records_validated": true, "selector_overhead_verified": true, "field_states_unchanged": true, "width_mask_bytes": controls, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "scope": "test-only separate width mask (native widths included); cached definition; in-memory codec; no indices, IPC, storage I/O, or network"})
}
