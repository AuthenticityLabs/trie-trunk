package cache

import (
	"bytes"
	"encoding/hex"
	"io"
	"math"
	"testing"
)

func compactTestDefinition(kinds ...string) Definition {
	d := Definition{Identity: "experiment/integers", Version: 1 << 20,
		Fields:   []Field{{Name: "event_id", Type: "UUID"}},
		Resource: Binding{Format: Format, Service: "experiment", Resource: "integers", Major: 1, Timezone: "UTC", Roles: Roles{Event: "event_id"}}}
	for i, kind := range kinds {
		d.Fields = append(d.Fields, Field{Name: string(rune('a' + i)), Type: kind})
	}
	return d
}

func TestCompactIntegerExperimentGolden(t *testing.T) {
	a, err := compileCompactIntegerExperiment(compactTestDefinition("U64", "U64", "U64", "U64"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.encode(nil, []Value{{}, Uint(7, 8), Uint(1000, 8), Uint(70000, 8), Uint(3, 8)})
	if err != nil {
		t.Fatal(err)
	}
	// Two mask bytes, one width byte 00/01/10/00, then 1/2/4/1 bytes.
	want, _ := hex.DecodeString("3fc01807e8037011010003")
	if !bytes.Equal(raw, want) {
		t.Fatalf("got %x, want %x", raw, want)
	}
	var f Frame
	if err = a.scan(raw, &f); err != nil {
		t.Fatal(err)
	}
	for i, w := range []int{1, 2, 4, 1} {
		if len(f.Values[i+1].Bytes) != w {
			t.Fatal("wrong borrowed width")
		}
	}
	if &f.Values[1].Bytes[0] != &raw[3] {
		t.Fatal("compact scanner copied scalar")
	}
	// All absent and all null have no control plane or value payload.
	for _, state := range []State{Absent, Null} {
		v := make([]Value, 5)
		for i := range v {
			v[i].State = state
		}
		r, err := a.encode(nil, v)
		if err != nil || len(r) != 2 {
			t.Fatalf("empty/null plane: %x %v", r, err)
		}
		if err = a.scan(r, &f); err != nil {
			t.Fatal(err)
		}
		for _, v := range f.Values {
			if v.State != state {
				t.Fatal("state leaked")
			}
		}
	}
}

func TestCompactIntegerExperimentSignedAndUnsignedBoundaries(t *testing.T) {
	for _, signed := range []bool{false, true} {
		kind := "U64"
		if signed {
			kind = "I64"
		}
		a, err := compileCompactIntegerExperiment(compactTestDefinition(kind))
		if err != nil {
			t.Fatal(err)
		}
		values := []uint64{0, 1, 127, 128, 255, 256, 32767, 32768, 65535, 65536, math.MaxInt32, 1 << 31, math.MaxUint32, 1 << 32, math.MaxInt64, 1 << 63, math.MaxUint64}
		if signed {
			for _, v := range []int64{-1, -128, -129, -32768, -32769, math.MinInt32, int64(math.MinInt32) - 1, math.MinInt64} {
				values = append(values, uint64(v))
			}
		}
		for _, v := range values {
			raw, err := a.encode(nil, []Value{{}, Uint(v, 8)})
			if err != nil {
				t.Fatal(err)
			}
			var f Frame
			if err = a.scan(raw, &f); err != nil {
				t.Fatal(err)
			}
			got := unsigned(f.Values[1].Bytes)
			if signed {
				got = uint64(compactSigned(f.Values[1].Bytes))
			}
			if got != v {
				t.Fatalf("%s %x became %x", kind, v, got)
			}
			w := len(f.Values[1].Bytes)
			if w > 1 {
				short := f.Values[1].Bytes[:w/2]
				truncated := unsigned(short)
				if signed {
					truncated = uint64(compactSigned(short))
				}
				if truncated == v {
					t.Fatalf("nonminimal %s width for %x", kind, v)
				}
			}
			again, err := a.encode(nil, f.Values)
			if err != nil || !bytes.Equal(raw, again) {
				t.Fatal("borrowed re-encode differs", err)
			}
		}
	}
}

func TestCompactIntegerExperimentPackedMixedWidths(t *testing.T) {
	// Exhaust all one/two-bit control patterns, including byte crossings.
	counts := []int{1, 2, 2, 2, 2, 1}
	for bits := 0; bits < 1024; bits++ {
		var packed [2]byte
		at := 0
		for _, n := range counts {
			putCompactCode(packed[:], at, n, byte(bits>>at)&byte((1<<n)-1))
			at += n
		}
		at = 0
		for _, n := range counts {
			if got := getCompactCode(packed[:], at, n); got != byte(bits>>at)&byte((1<<n)-1) {
				t.Fatal("cross-byte selector mismatch")
			}
			at += n
		}
	}
	a, err := compileCompactIntegerExperiment(compactTestDefinition("U32", "I64", "U64", "U64", "I64", "I32", "U8", "STRING"))
	if err != nil {
		t.Fatal(err)
	}
	values := []Value{{}, Uint(256, 4), Uint(math.MaxUint64, 8), Uint(65536, 8), Uint(1<<24, 8), Uint(^uint64(32768), 8), Uint(128, 4), Uint(5, 1), Text("hello")}
	raw, err := a.encode(nil, values)
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	if err = a.scan(raw, &f); err != nil {
		t.Fatal(err)
	}
	if a.controlBits(raw) != 10 {
		t.Fatal("mixed selectors were not packed")
	}
	for _, cut := range []int{1, 3, 4, 5} {
		values[cut] = Value{State: Null}
	}
	raw, err = a.encode(nil, values)
	if err != nil {
		t.Fatal(err)
	}
	if a.controlBits(raw) != 3 {
		t.Fatal("null fields consume selectors")
	}
	if err = a.scan(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{1, 3, 4, 5} {
		if f.Values[i].State != Null {
			t.Fatal("null lost")
		}
	}
}

func TestCompactIntegerExperimentStreamAndValidation(t *testing.T) {
	a, err := compileCompactIntegerExperiment(compactTestDefinition("I32", "STRING", "U64"))
	if err != nil {
		t.Fatal(err)
	}
	frames := [][]Value{{{}, Uint(math.MaxUint32, 4), Text("one"), Uint(256, 8)}, {{}, Uint(70000, 4), Text(""), {State: Null}}, {{}, {State: Null}, {}, {}}}
	var encoded [][]byte
	var stream []byte
	for _, v := range frames {
		r, err := a.encode(nil, v)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, r)
		stream = append(stream, r...)
		for n := 0; n < len(r); n++ {
			var f Frame
			if a.scan(r[:n], &f) == nil {
				t.Fatalf("accepted truncation %x", r[:n])
			}
		}
		var f Frame
		if a.scan(append(bytes.Clone(r), 0), &f) == nil {
			t.Fatal("accepted trailing byte")
		}
	}
	r := bytes.NewReader(stream)
	for _, want := range encoded {
		got, err := a.readFrame(r)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("stream boundary mismatch", err)
		}
	}
	if _, err = a.readFrame(r); err != io.EOF {
		t.Fatal("missing EOF", err)
	}
	bad := bytes.Clone(encoded[0])
	bad[a.mask] |= 0x60 // Reserved U64 compact width = 8 bytes.
	var f Frame
	if a.scan(bad, &f) == nil {
		t.Fatal("accepted width above declared maximum")
	}
	bad = bytes.Clone(encoded[0])
	bad[a.mask] |= 1
	if a.scan(bad, &f) == nil {
		t.Fatal("accepted nonzero selector padding")
	}
	bad = bytes.Clone(encoded[0])
	bad[len(bad)-1] = 0xff
	if a.scan(bad, &f) == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	// Decoder may accept wider legal encodings, like the direct CBOR decoder;
	// shortest representation is an encoder rule, not extra timed validation.
	b, err := compileCompactIntegerExperiment(compactTestDefinition("U64"))
	if err != nil {
		t.Fatal(err)
	}
	if err = b.scan([]byte{0x30, 0x40, 1, 0}, &f); err != nil {
		t.Fatal(err)
	}
	if unsigned(f.Values[1].Bytes) != 1 {
		t.Fatal("nonminimal legal value changed")
	}
	allocationInput := []byte{0x30, 0, 7}
	if n := testing.AllocsPerRun(1000, func() {
		if err := b.scan(allocationInput, &f); err != nil {
			panic(err)
		}
	}); n != 0 {
		t.Fatalf("reused scan allocations: %v", n)
	}
}

func FuzzCompactIntegerExperiment(f *testing.F) {
	a, err := compileCompactIntegerExperiment(compactTestDefinition("I64", "U32", "STRING"))
	if err != nil {
		f.Fatal(err)
	}
	for _, v := range [][]Value{{{}, {}, {}, {}}, {{}, Uint(math.MaxUint64, 8), Uint(400, 4), Text("seed")}} {
		b, err := a.encode(nil, v)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 4096 {
			t.Skip()
		}
		var frame Frame
		if a.scan(b, &frame) != nil {
			return
		}
		canonical, err := a.encode(nil, frame.Values)
		if err != nil {
			t.Fatal(err)
		}
		var out Frame
		if err = a.scan(canonical, &out); err != nil {
			t.Fatal(err)
		}
		for i, v := range frame.Values {
			if v.State != out.Values[i].State {
				t.Fatal("state changed")
			}
			if v.State == Direct && i == 1 {
				if compactSigned(v.Bytes) != compactSigned(out.Values[i].Bytes) {
					t.Fatal("signed changed")
				}
			} else if v.State == Direct && i == 2 {
				if unsigned(v.Bytes) != unsigned(out.Values[i].Bytes) {
					t.Fatal("unsigned changed")
				}
			} else if !bytes.Equal(v.Bytes, out.Values[i].Bytes) {
				t.Fatal("value changed")
			}
		}
	})
}

func TestCompactIntegerExperimentNativeCompatibility(t *testing.T) {
	for _, kinds := range [][]string{{"U8", "I8", "U16", "I16"}, {"U32", "I32", "U64", "I64"}} {
		a, err := compileCompactIntegerExperiment(compactTestDefinition(kinds...))
		if err != nil {
			t.Fatal(err)
		}
		values := make([]Value, len(a.fields))
		for i := 1; i < len(values); i++ {
			w := a.fields[i].width
			values[i] = Uint(uint64(1)<<uint(w*8-2), w)
		}
		ordinary, err := a.base.Encode(nil, values)
		if err != nil {
			t.Fatal(err)
		}
		compact, err := a.encode(nil, values)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ordinary, compact) || a.controlBits(compact) != 0 {
			t.Fatal("native-width frame changed")
		}
		var f Frame
		if err = a.scan(compact, &f); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(values); i++ {
			if !bytes.Equal(values[i].Bytes, f.Values[i].Bytes) {
				t.Fatal("native value changed")
			}
		}
	}
	a, err := compileCompactIntegerExperiment(compactTestDefinition("U32"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.encode(nil, []Value{{}, Uint(1<<40, 8)}); err == nil {
		t.Fatal("accepted integer beyond declared width")
	}
	raw, err := a.encode(nil, []Value{{}, Uint(7, 4)})
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	if a.base.Scan(raw, &f) == nil {
		t.Fatal("old decoder accepted new mask meaning")
	}
}
