package cache

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
	"trie-trunk/go/codec"
)

// These adapters intentionally use the existing compact CBOR implementation.
// Field IDs and dictionary ordinals remain numeric. No expanded JSON is built.
// This is a benchmark payload adapter, not a second production file format.
func fixtureCBOR(l *Layout, values []Value) (codec.Record, error) {
	r := codec.Record{}
	for i, v := range values {
		if v.State == Absent {
			continue
		}
		var value any
		switch v.State {
		case Null:
			value = nil
		case Direct:
			if l.Definition.Fields[i].Type == "UUID" {
				value = v.Bytes
			} else {
				value = unsigned(v.Bytes)
			}
		case Offset:
			if l.Definition.Fields[i].Type == "STRING" {
				value = string(v.Bytes)
			} else {
				return nil, fmt.Errorf("unsupported benchmark variable type")
			}
		}
		r[codec.Key(i)] = value
	}
	return r, nil
}
func scanFixtureCBOR(l *Layout, raw []byte, f *Frame) error {
	r, e := codec.Decode(raw, codec.CBOR)
	if e != nil {
		return e
	}
	f.Values = make([]Value, len(l.Widths))
	for key, v := range r {
		i := int(key)
		if i >= len(f.Values) {
			return fmt.Errorf("unknown benchmark field")
		}
		switch v := v.(type) {
		case nil:
			f.Values[i] = Value{State: Null}
		case uint64:
			w := l.Widths[i]
			if w <= 0 || w > 8 {
				return fmt.Errorf("invalid integer width")
			}
			if w < 8 && v >= uint64(1)<<uint(w*8) {
				return fmt.Errorf("CBOR integer overflow")
			}
			if len(l.Definition.Fields[i].Values) > 0 && v >= uint64(len(l.Definition.Fields[i].Values)) {
				return fmt.Errorf("invalid CBOR dictionary reference")
			}
			f.Values[i] = Uint(v, w)
		case string:
			f.Values[i] = Text(v)
		case []byte:
			f.Values[i] = Value{Direct, v}
		default:
			return fmt.Errorf("unsupported benchmark scalar %T", v)
		}
	}
	return nil
}
func (s *Store) fixtureCBORReader() func(*branchStore, uint64, []byte) ([]byte, error) {
	var reader podsReader
	return func(b *branchStore, number uint64, dst []byte) ([]byte, error) {
		raw, e := reader.read(b, number, s.row)
		if e != nil {
			return nil, e
		}
		var f Frame
		if e = scanFixtureCBOR(s.layout, raw, &f); e != nil {
			return nil, e
		}
		return s.expandFrame(b.assemblyRow(number), &f, dst, b.rowLeaf(number))
	}
}

func TestMeasurePayloadCodecs(t *testing.T) {
	n := measurementSize(t)
	sparse := os.Getenv("TRUNK_MEASURE_SHAPE") == "sparse"
	profile := measurementIntegerProfile(t)
	l, frames, from, to, hash := measurementFixtureProfile(t, n, sparse, profile)
	d := l.Definition
	d.Resource.Aggregations = nil
	l, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	direct, e := CompilePositionalCBOR(l)
	if e != nil {
		t.Fatal(e)
	}
	c := Config{Root: t.TempDir(), Cache: "bench", Tenant: "measure", Layout: l, ObjectIndex: false, Now: func() time.Time { return to.Add(time.Hour) }}
	s, e := Open(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, f := range frames {
		if _, e = s.Append(Write, f); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Flush(); e != nil {
		t.Fatal(e)
	}
	original := s.branches[Write]
	alt := *original
	alt.rows = append([]byte(nil), original.rows...)
	alt.pods, e = os.CreateTemp(t.TempDir(), "cbor-pods-")
	if e != nil {
		t.Fatal(e)
	}
	defer alt.pods.Close()
	writer := bufio.NewWriterSize(alt.pods, 1<<20)
	if _, e = writer.Write(make([]byte, headerSize)); e != nil {
		t.Fatal(e)
	}
	at := uint64(headerSize)
	// Prepare one representative committed batch for encode/decode measurements;
	// the full payload comparison below uses every record in both files.
	var pilBatch, cborBatch [][]byte
	var valueBatch [][]Value
	var recordBatch []codec.Record
	var batchRows []int
	batchSize := min(n, 1024)
	for offset := 0; offset < len(alt.rows); offset += s.row.Width {
		row := alt.rows[offset : offset+s.row.Width]
		start, end, e := original.podsRange(uint64(offset/s.row.Width), s.row)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := readBytes(original.pods, start, end)
		if e != nil {
			t.Fatal(e)
		}
		var f Frame
		if e = l.Scan(raw, &f); e != nil {
			t.Fatal(e)
		}
		record, e := fixtureCBOR(l, f.Values)
		if e != nil {
			t.Fatal(e)
		}
		encoded, e := marshalBlindCBOR(record)
		if e != nil {
			t.Fatal(e)
		}
		// Sample the entire collection, not only its earliest/smallest revisions.
		if len(pilBatch) < batchSize && (batchSize == 1 || offset/s.row.Width == len(pilBatch)*(n-1)/(batchSize-1)) {
			pilBatch = append(pilBatch, raw)
			cborBatch = append(cborBatch, encoded)
			valueBatch = append(valueBatch, f.Values)
			recordBatch = append(recordBatch, record)
			batchRows = append(batchRows, offset/s.row.Width)
		}
		if _, e = writer.Write(encoded); e != nil {
			t.Fatal(e)
		}
		at += uint64(len(encoded))
		order.PutUint64(row[s.row.End:], at)
	}
	if e = writer.Flush(); e != nil {
		t.Fatal(e)
	}
	if e = alt.pods.Sync(); e != nil {
		t.Fatal(e)
	}
	alt.podsEnd = at
	podsStat, e := original.pods.Stat()
	if e != nil {
		t.Fatal(e)
	}
	results := map[string]map[string]timing{}
	hashes := map[string]string{}
	matched := map[string]int{}
	queries := []struct {
		name  string
		query Query
	}{
		{"all", Query{Branch: Write, From: from, To: to}},
		{"day", Query{Branch: Write, From: from.AddDate(0, 0, 15), To: from.AddDate(0, 0, 16)}},
		{"three_hours", Query{Branch: Write, From: from.AddDate(0, 0, 15).Add(9 * time.Hour), To: from.AddDate(0, 0, 15).Add(12 * time.Hour)}},
		{"state", Query{Branch: Write, From: from, To: to, Equals: map[string]Value{"state": Text("canceled")}}},
	}
	defer func() { s.branches[Write] = original }()
	for _, q := range queries {
		var expected string
		modes := []string{"cbor", "cbor_direct", "pil", "cbor_direct_reuse", "pil_reuse"}
		runs := map[string]func() error{}
		for _, mode := range modes {
			mode := mode
			run := func(visit func(uint64, []byte) error) error {
				b, reader := original, s.assembledReader()
				if mode == "cbor" {
					b, reader = &alt, s.fixtureCBORReader()
				} else if mode == "cbor_direct" || mode == "cbor_direct_reuse" {
					b, reader = &alt, s.directCBORReader(direct, mode == "cbor_direct_reuse")
				} else if mode == "pil_reuse" {
					reader = s.reusingPILReader()
				}
				s.branches[Write] = b
				return s.search(context.Background(), q.query, visit, reader)
			}
			digest := sha256.New()
			count := 0
			if e = run(func(_ uint64, b []byte) error { count++; digest.Write(b); return nil }); e != nil {
				t.Fatal(e)
			}
			got := hex.EncodeToString(digest.Sum(nil))
			if q.name == "all" && (count != n || got != hash) {
				t.Fatal("codec output differs from original input")
			}
			if expected != "" && expected != got {
				t.Fatal("codec output differs")
			}
			expected = got
			hashes[q.name] = got
			matched[q.name] = count
			runs[mode] = func() error {
				seen := 0
				err := run(func(uint64, []byte) error { seen++; return nil })
				if err != nil {
					return err
				}
				if seen != count {
					return fmt.Errorf("result count changed")
				}
				return nil
			}
		}
		// Interleave samples to avoid attributing sustained warmup/thermal drift to
		// whichever codec happened to run first. Warmup and validation are untimed.
		samples := map[string]timing{}
		for i := 0; i < 9; i++ {
			for _, mode := range modes {
				start := time.Now()
				if e = runs[mode](); e != nil {
					t.Fatal(e)
				}
				v := samples[mode]
				v.Samples = append(v.Samples, time.Since(start).Nanoseconds())
				samples[mode] = v
			}
			modes = append(modes[1:], modes[0])
		}
		for mode, v := range samples {
			samples[mode] = timingSamples(v.Samples)
		}
		results[q.name] = samples
	}
	s.branches[Write] = original
	codecTimings := map[string]timing{}
	codecTimings["cbor_encode_1024"] = timed(t, 21, func() error {
		for _, v := range recordBatch {
			if _, e := marshalBlindCBOR(v); e != nil {
				return e
			}
		}
		return nil
	})
	codecTimings["pil_encode_1024"] = timed(t, 21, func() error {
		for _, v := range valueBatch {
			if _, e := l.Encode(nil, v); e != nil {
				return e
			}
		}
		return nil
	})
	codecTimings["cbor_decode_1024"] = timed(t, 21, func() error {
		for _, v := range cborBatch {
			var f Frame
			if e := scanFixtureCBOR(l, v, &f); e != nil {
				return e
			}
		}
		return nil
	})
	codecTimings["pil_decode_1024"] = timed(t, 21, func() error {
		for _, v := range pilBatch {
			var f Frame
			if e := l.Scan(v, &f); e != nil {
				return e
			}
		}
		return nil
	})
	codecTimings["cbor_direct_decode_1024"] = timed(t, 21, func() error {
		for _, raw := range cborBatch {
			var f CBORFrame
			if e := direct.Scan(raw, &f); e != nil {
				return e
			}
		}
		return nil
	})
	var cborReuse CBORFrame
	var pilReuse Frame
	codecTimings["cbor_direct_reuse_decode_1024"] = timed(t, 21, func() error {
		for _, raw := range cborBatch {
			if e := direct.Scan(raw, &cborReuse); e != nil {
				return e
			}
		}
		return nil
	})
	codecTimings["pil_reuse_decode_1024"] = timed(t, 21, func() error {
		for _, raw := range pilBatch {
			if e := l.Scan(raw, &pilReuse); e != nil {
				return e
			}
		}
		return nil
	})
	allocs := map[string]float64{}
	allocs["cbor"] = testing.AllocsPerRun(100, func() {
		var f Frame
		if e := scanFixtureCBOR(l, cborBatch[0], &f); e != nil {
			panic(e)
		}
	})
	allocs["cbor_direct"] = testing.AllocsPerRun(100, func() {
		var f CBORFrame
		if e := direct.Scan(cborBatch[0], &f); e != nil {
			panic(e)
		}
	})
	allocs["pil"] = testing.AllocsPerRun(100, func() {
		var f Frame
		if e := l.Scan(pilBatch[0], &f); e != nil {
			panic(e)
		}
	})
	allocs["cbor_direct_reuse"] = testing.AllocsPerRun(100, func() {
		if e := direct.Scan(cborBatch[0], &cborReuse); e != nil {
			panic(e)
		}
	})
	allocs["pil_reuse"] = testing.AllocsPerRun(100, func() {
		if e := l.Scan(pilBatch[0], &pilReuse); e != nil {
			panic(e)
		}
	})
	measurementWithoutObjectIndex(t, s)
	saveMeasurement(t, map[string]any{"events": n, "sparse": sparse, "format": Format, "aggregates": false, "object_index": false, "object_index_bytes": 0, "row_width": s.row.Width, "fixture_sha256": hash, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH,
		"integer_profile": profile, "definition": l.Definition, "codec_batch_rows": batchRows, "output_contract": "standalone PIL",
		"decoder_allocations_per_record": allocs, "payload_bytes": map[string]int64{"cbor": int64(at) - headerSize, "pil": podsStat.Size() - headerSize}, "index_bytes_each": len(original.rows), "query": results, "result_hashes": hashes, "matched_events": matched, "codec_batch_events": len(pilBatch), "codec": codecTimings})
}

func TestPayloadCodecEquivalence(t *testing.T) {
	l, frames, from, to, _ := measurementFixture(t, 12, false)
	// Include NULL and empty text as well as absent PATCH fields.
	for i := 0; i < 2; i++ {
		var f Frame
		if e := l.Scan(frames[i], &f); e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			f.Values[7] = Value{State: Null}
		} else {
			f.Values[7] = Text("")
			f.Values[3] = Value{}
		}
		var e error
		frames[i], e = l.Encode(nil, f.Values)
		if e != nil {
			t.Fatal(e)
		}
	}
	_, sparse, _, _, _ := measurementFixture(t, 12, true)
	for _, set := range [][][]byte{frames, sparse} {
		s, e := Open(Config{Root: t.TempDir(), Cache: "bench", Tenant: "test", Layout: l, Now: func() time.Time { return to.Add(time.Hour) }})
		if e != nil {
			t.Fatal(e)
		}
		for _, raw := range set {
			if _, e = s.Append(Write, raw); e != nil {
				t.Fatal(e)
			}
		}
		if e = s.Flush(); e != nil {
			t.Fatal(e)
		}
		b := s.branches[Write]
		ordinal := 0
		for n := b.nextRecord(0); n < b.tail(); n = b.nextRecord(n + 1) {
			at := int(n) * s.row.Width
			row := b.assemblyRow(n)
			reader := podsReader{}
			raw, e := reader.read(b, uint64(at/s.row.Width), s.row)
			if e != nil {
				t.Fatal(e)
			}
			var f Frame
			if e = l.Scan(raw, &f); e != nil {
				t.Fatal(e)
			}
			record, e := fixtureCBOR(l, f.Values)
			if e != nil {
				t.Fatal(e)
			}
			encoded, e := marshalBlindCBOR(record)
			if e != nil {
				t.Fatal(e)
			}
			var other Frame
			if e = scanFixtureCBOR(l, encoded, &other); e != nil {
				t.Fatal(e)
			}
			pil, e := s.expandFrame(row, &f, nil, b.rowLeaf(n))
			if e != nil {
				t.Fatal(e)
			}
			cbor, e := s.expandFrame(row, &other, nil, b.rowLeaf(n))
			if e != nil {
				t.Fatal(e)
			}
			if !sameSeedData(t, l, pil, set[ordinal]) {
				t.Fatal("expanded identity/payload differs from ingress")
			}
			if string(pil) != string(cbor) {
				t.Fatal("CBOR/PIL expansion differs")
			}
			ordinal++
		}
		if len(collect(t, s, Write, from, to)) != 12 {
			t.Fatal("query count")
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
	}
}

// All query modes use the same bounded payload reader and search traversal.
// Decoder and output strategy are the variables under measurement.
func (s *Store) directCBORReader(r *PositionalCBOR, reuse bool) func(*branchStore, uint64, []byte) ([]byte, error) {
	var reader podsReader
	decode := func(b *branchStore, number uint64, dst []byte, f *CBORFrame) ([]byte, error) {
		raw, e := reader.read(b, number, s.row)
		if e != nil {
			return nil, e
		}
		if e = r.Scan(raw, f); e != nil {
			return nil, e
		}
		return s.expandFrame(b.assemblyRow(number), &f.Frame, dst, b.rowLeaf(number))
	}
	if reuse {
		var f CBORFrame
		return func(b *branchStore, n uint64, dst []byte) ([]byte, error) { return decode(b, n, dst, &f) }
	}
	return func(b *branchStore, n uint64, dst []byte) ([]byte, error) {
		var f CBORFrame
		return decode(b, n, dst, &f)
	}
}
func (s *Store) reusingPILReader() func(*branchStore, uint64, []byte) ([]byte, error) {
	var reader podsReader
	var f Frame
	return func(b *branchStore, number uint64, dst []byte) ([]byte, error) {
		raw, e := reader.read(b, number, s.row)
		if e != nil {
			return nil, e
		}
		if e = s.layout.Scan(raw, &f); e != nil {
			return nil, e
		}
		return s.expandFrame(b.assemblyRow(number), &f, dst, b.rowLeaf(number))
	}
}
