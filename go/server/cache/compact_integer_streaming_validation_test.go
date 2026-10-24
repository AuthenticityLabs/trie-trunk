package cache

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func TestCompactIntegerStreamingEquivalence(t *testing.T) {
	d := compactTestDefinition("I64", "U64", "I32", "U32", "STRING", "BOOL", "U8", "I16", "BYTES", "STRING")
	d.Fields[10].Values = []string{"yes", "no"}
	ref, e := compileCompactIntegerExperiment(d)
	if e != nil {
		t.Fatal(e)
	}
	fast := compileFastIntegerExperiment(ref)
	stream := compileStreamIntegerExperiment(fast)
	encode := func(dst []byte, values []Value, compact bool) ([]byte, error) {
		if compact {
			return stream.encode(dst, values)
		}
		return ref.base.Encode(dst, values)
	}
	rng := rand.New(rand.NewPCG(17, 31))
	var f, g Frame
	for n := 0; n < 10000; n++ {
		values := []Value{{}, Uint(rng.Uint64(), 8), Uint(rng.Uint64(), 8), Uint(rng.Uint64(), 4), Uint(rng.Uint64(), 4), Text("hello"), Uint(uint64(n%2), 1), Uint(7, 1), Uint(uint64(n), 2), {State: Offset, Bytes: []byte{0, 255, 128}}, Uint(uint64(n%2), 1)}
		for i := 1; i <= 4; i++ {
			if n%2 == 0 {
				values[i] = Uint(uint64(n%1000), ref.fields[i].width)
			}
			if n%3 == 0 {
				values[i] = Uint(^uint64(n%1000), ref.fields[i].width)
			}
		}
		for i := range values {
			switch rng.IntN(5) {
			case 0:
				values[i] = Value{State: Absent}
			case 1:
				values[i] = Value{State: Null}
			}
		}
		for _, compact := range []bool{false, true} {
			var want []byte
			if compact {
				want, e = ref.encode(nil, values)
			} else {
				want, e = ref.base.Encode(nil, values)
			}
			if e != nil {
				t.Fatal(e)
			}
			got, e := encode(nil, values, compact)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(want, got) {
				t.Fatalf("wire changed: %x %x", want, got)
			}
			if e = ref.scan(got, &f); e != nil {
				t.Fatal(e)
			}
			if e = fast.scan(got, &g); e != nil {
				t.Fatal(e)
			}
			compactExperimentEqual(t, ref.base, f.Values, g.Values)
			if compact {
				again, e := encode(nil, g.Values, true)
				if e != nil || !bytes.Equal(again, got) {
					t.Fatal("borrowed reencode differs", e)
				}
			}
		}
	}
	for _, v := range [][]Value{{}, make([]Value, len(ref.fields))} {
		if len(v) == 0 {
			if _, e = encode(nil, v, true); e == nil {
				t.Fatal("bad field count accepted")
			}
			continue
		}
		v[1] = Value{State: Direct, Bytes: []byte{1, 2, 3}}
		if _, e = encode(nil, v, true); e == nil {
			t.Fatal("bad width accepted")
		}
	}
}
func FuzzCompactIntegerStreaming(f *testing.F) {
	ref, e := compileCompactIntegerExperiment(compactTestDefinition("I64", "U32", "STRING", "BOOL"))
	if e != nil {
		f.Fatal(e)
	}
	fast := compileFastIntegerExperiment(ref)
	stream := compileStreamIntegerExperiment(fast)
	encode := func(dst []byte, values []Value, compact bool) ([]byte, error) {
		if compact {
			return stream.encode(dst, values)
		}
		return ref.base.Encode(dst, values)
	}
	for _, values := range [][]Value{{{}, {}, {}, {}, {}}, {{}, Uint(^uint64(128), 8), Uint(400, 4), Text("seed"), Uint(1, 1)}} {
		raw, e := ref.encode(nil, values)
		if e != nil {
			f.Fatal(e)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 4096 {
			t.Skip()
		}
		var a, b Frame
		ea, eb := ref.scan(raw, &a), fast.scan(raw, &b)
		if (ea == nil) != (eb == nil) {
			t.Fatalf("validation differs %x: %v / %v", raw, ea, eb)
		}
		if ea != nil {
			return
		}
		compactExperimentEqual(t, ref.base, a.Values, b.Values)
		want, e := ref.encode(nil, a.Values)
		if e != nil {
			t.Fatal(e)
		}
		got, e := encode(nil, b.Values, true)
		if e != nil || !bytes.Equal(want, got) {
			t.Fatal("wire differs", e)
		}
	})
}
