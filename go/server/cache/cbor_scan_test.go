package cache

import (
	"bytes"
	"math"
	"testing"
	"trie-trunk/go/codec"
)

func TestPositionalCBORDifferential(t *testing.T) {
	l, frames, _, _, _ := measurementFixture(t, 200, false)
	r, e := CompilePositionalCBOR(l)
	if e != nil {
		t.Fatal(e)
	}
	_, sparse, _, _, _ := measurementFixture(t, 200, true)
	frames = append(frames, sparse...)
	var direct CBORFrame
	for _, raw := range frames {
		var f Frame
		if e = l.Scan(raw, &f); e != nil {
			t.Fatal(e)
		}
		record, e := fixtureCBOR(l, f.Values)
		if e != nil {
			t.Fatal(e)
		}
		b, e := marshalBlindCBOR(record)
		if e != nil {
			t.Fatal(e)
		}
		var mapped Frame
		if e = scanFixtureCBOR(l, b, &mapped); e != nil {
			t.Fatal(e)
		}
		if e = r.Scan(b, &direct); e != nil {
			t.Fatal(e)
		}
		got, e := l.Encode(nil, direct.Values)
		if e != nil {
			t.Fatal(e)
		}
		other, e := l.Encode(nil, mapped.Values)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(got, raw) || !bytes.Equal(got, other) {
			t.Fatal("map, positional CBOR and PIL differ")
		}
	}
	// Reuse clears prior fields; NULL stays distinct from absence and empty text.
	raw := []byte{0xa2, 7, 0xf6, 10, 0x60}
	if e = r.Scan(raw, &direct); e != nil {
		t.Fatal(e)
	}
	if direct.Values[7].State != Null || direct.Values[10].State != Offset || len(direct.Values[10].Bytes) != 0 || direct.Values[6].State != Absent {
		t.Fatal("field state leaked across scans")
	}
	if e = r.Scan([]byte{0xa0}, &direct); e != nil {
		t.Fatal(e)
	}
	for _, v := range direct.Values {
		if v.State != Absent {
			t.Fatal("empty map did not clear values")
		}
	}
}

func TestPositionalCBORValidation(t *testing.T) {
	l, _, _, _, _ := measurementFixture(t, 1, false)
	r, e := CompilePositionalCBOR(l)
	if e != nil {
		t.Fatal(e)
	}
	valid := [][]byte{
		{0xa0},
		{0xa2, 8, 0x19, 1, 0, 7, 0x61, 'x'}, // unordered keys, no sort/map requirement
		{0xa1, 0x18, 3, 0x1b, 0, 0, 0, 0, 0, 0, 0, 1}, // nonpreferred but valid CBOR widths
		{0xa1, 4, 0x64, 't', 'e', 's', 't'},           // mapped field's literal escape
	}
	for _, raw := range valid {
		var f CBORFrame
		if e = r.Scan(raw, &f); e != nil {
			t.Fatalf("%x: %v", raw, e)
		}
		for n := 0; n < len(raw); n++ {
			if r.Scan(raw[:n], &f) == nil {
				t.Fatalf("accepted truncation %x", raw[:n])
			}
		}
	}
	bad := [][]byte{
		{}, {0x80}, {0xbf, 0xff}, {0xa1, 0x20, 0}, {0xa1, 23, 0}, {0xa1, 0x61, 'a', 0},
		{0xa2, 7, 0xf6, 7, 0x60}, // duplicate with NULL first
		{0xa1, 7, 0x61, 0xff}, {0xa1, 7, 0x7f, 0xff},
		{0xa1, 11, 0x1b, 0, 0, 0, 1, 0, 0, 0, 0},       // U32 overflow
		{0xa1, 4, 2}, {0xa1, 3, 0x20}, {0xa1, 3, 0xf7}, // enum overflow/negative unsigned/undefined
		{0xa1, 0, 0x41, 0}, {0xa1, 3, 0xc0, 0}, {0xa1, 3, 0x1c},
		{0xa0, 0}, {0xa1, 7, 0x7b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	}
	for _, raw := range bad {
		var f CBORFrame
		if r.Scan(raw, &f) == nil {
			t.Fatalf("accepted malformed/type-invalid CBOR %x", raw)
		}
	}
}

func TestPositionalCBORScalars(t *testing.T) {
	types := []string{"I8", "I16", "I32", "I64", "U8", "U16", "U24", "U32", "U64", "REF16", "BOOL", "F32", "F64", "TIMESTAMP_MS", "TIMESTAMP_US", "TIMESTAMP_NS", "BYTES", "UUID"}
	for _, kind := range types {
		t.Run(kind, func(t *testing.T) {
			l := testLayout(t, false)
			d := l.Definition
			d.Fields[6].Type = kind
			l, e := Compile(d)
			if e != nil {
				t.Fatal(e)
			}
			r, e := CompilePositionalCBOR(l)
			if e != nil {
				t.Fatal(e)
			}
			var values []any
			switch kind {
			case "I8":
				values = []any{int64(-128), int64(127)}
			case "I16":
				values = []any{int64(-32768), int64(32767)}
			case "I32":
				values = []any{int64(math.MinInt32), int64(math.MaxInt32)}
			case "I64", "TIMESTAMP_MS", "TIMESTAMP_US", "TIMESTAMP_NS":
				values = []any{int64(math.MinInt64), int64(math.MaxInt64)}
			case "U8":
				values = []any{uint64(255)}
			case "U16", "REF16":
				values = []any{uint64(65535)}
			case "U32":
				values = []any{uint64(math.MaxUint32)}
			case "U64":
				values = []any{uint64(math.MaxUint64)}
			case "BOOL":
				values = []any{false, true}
			case "F32":
				values = []any{float32(1.5), float32(1.234567), float32(math.Inf(1)), float32(math.Copysign(0, -1)), float32(math.NaN())}
			case "F64":
				values = []any{1.5, 1.23456789012345, math.Inf(-1), math.Copysign(0, -1), math.NaN()}
			case "BYTES":
				values = []any{[]byte{}, []byte{0, 0xff, 1}}
			case "UUID":
				values = []any{bytes.Repeat([]byte{0xab}, 16)}
			}
			for _, v := range values {
				raw, e := marshalBlindCBOR(codec.Record{6: v})
				if e != nil {
					t.Fatal(e)
				}
				var f CBORFrame
				if e = r.Scan(raw, &f); e != nil {
					t.Fatal(e)
				}
				out, e := l.Encode(nil, f.Values)
				if e != nil {
					t.Fatal(e)
				}
				var check Frame
				if e = l.Scan(out, &check); e != nil {
					t.Fatal(e)
				}
				b := check.Values[6].Bytes
				switch v := v.(type) {
				case int64:
					w := l.Widths[6]
					got := unsigned(b)
					mask := uint64(math.MaxUint64)
					if w < 8 {
						mask = 1<<uint(w*8) - 1
					}
					if got != uint64(v)&mask {
						t.Fatal("signed value changed")
					}
				case uint64:
					if unsigned(b) != v {
						t.Fatal("unsigned value changed")
					}
				case bool:
					if (b[0] != 0) != v {
						t.Fatal("bool changed")
					}
				case float32:
					got := math.Float32frombits(order.Uint32(b))
					if !math.IsNaN(float64(v)) && math.Float32bits(got) != math.Float32bits(v) {
						t.Fatal("float32 changed")
					}
				case float64:
					got := math.Float64frombits(order.Uint64(b))
					if !math.IsNaN(v) && math.Float64bits(got) != math.Float64bits(v) {
						t.Fatal("float64 changed")
					}
				case []byte:
					if !bytes.Equal(b, v) {
						t.Fatal("bytes changed")
					}
				}
			}
		})
	}
}

func TestPositionalCBORReuseAllocationsAndBorrowing(t *testing.T) {
	l, _, _, _, _ := measurementFixture(t, 1, false)
	r, e := CompilePositionalCBOR(l)
	if e != nil {
		t.Fatal(e)
	}
	raw := []byte{0xa2, 3, 0x18, 24, 7, 0x61, 'x'}
	var f CBORFrame
	if e = r.Scan(raw, &f); e != nil {
		t.Fatal(e)
	}
	if &f.Values[7].Bytes[0] != &raw[len(raw)-1] {
		t.Fatal("string was copied")
	}
	if n := testing.AllocsPerRun(1000, func() {
		if e := r.Scan(raw, &f); e != nil {
			panic(e)
		}
	}); n != 0 {
		t.Fatalf("reused decoder allocated %v objects", n)
	}
}

func FuzzPositionalCBOR(f *testing.F) {
	l, _, _, _, _ := measurementFixture(f, 1, false)
	r, e := CompilePositionalCBOR(l)
	if e != nil {
		f.Fatal(e)
	}
	f.Add([]byte{0xa0})
	f.Add([]byte{0xa2, 3, 1, 7, 0x61, 'x'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 4096 {
			t.Skip()
		}
		var frame CBORFrame
		if r.Scan(raw, &frame) != nil {
			return
		}
		if e := ingressDecoder.Valid(raw); e != nil {
			t.Fatalf("accepted invalid CBOR: %v", e)
		}
		pil, e := l.Encode(nil, frame.Values)
		if e != nil {
			t.Fatal(e)
		}
		var decoded Frame
		if e = l.Scan(pil, &decoded); e != nil {
			t.Fatal(e)
		}
	})
}

func TestPositionalCBORDefinitionAndNarrowTypes(t *testing.T) {
	if _, e := CompilePositionalCBOR(nil); e == nil {
		t.Fatal("accepted nil layout")
	}
	for _, kind := range []string{"ARRAY<U16>", "OBJECT"} {
		d := testLayout(t, false).Definition
		d.Fields[6].Type = kind
		l, e := Compile(d)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = CompilePositionalCBOR(l); e == nil {
			t.Fatal("accepted unsupported profile")
		}
	}
	for _, test := range []struct {
		kind string
		raw  []byte
	}{
		{"I8", []byte{0xa1, 6, 0x18, 128}},
		{"I8", []byte{0xa1, 6, 0x38, 128}},
		{"I64", []byte{0xa1, 6, 0x3b, 0x80, 0, 0, 0, 0, 0, 0, 0}},
		{"BOOL", []byte{0xa1, 6, 1}},
		{"F32", []byte{0xa1, 6, 0xfb, 0x7f, 0xef, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
	} {
		d := testLayout(t, false).Definition
		d.Fields[6].Type = test.kind
		l, e := Compile(d)
		if e != nil {
			t.Fatal(e)
		}
		r, e := CompilePositionalCBOR(l)
		if e != nil {
			t.Fatal(e)
		}
		var frame CBORFrame
		if r.Scan(test.raw, &frame) == nil {
			t.Fatalf("accepted invalid %s", test.kind)
		}
	}
}
