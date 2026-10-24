package cache

import (
	"bytes"
	"testing"
)

func TestSeparateWidthExhaustive16(t *testing.T) {
	p, err := compileSeparateWidthExperiment(compactTestDefinition("U16", "I16"), true)
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	var dst []byte
	for u := 0; u < 65536; u++ {
		values := []Value{{}, Uint(uint64(u), 2), Uint(uint64(u), 2)}
		raw, err := p.encode(nil, values, true)
		if err != nil {
			t.Fatal(err)
		}
		// Unchanged state mask, then a full extra byte for two width bits.
		want := 6
		selector := byte(0xc0)
		if u <= 255 {
			want--
			selector &^= 0x80
		}
		if int16(u) >= -128 && int16(u) <= 127 {
			want--
			selector &^= 0x40
		}
		if len(raw) != want || raw[0] != 0x28 || raw[1] != selector {
			t.Fatalf("u=%d: %x", u, raw)
		}
		dst, err = p.encodeReuse(dst, values)
		if err != nil || !bytes.Equal(raw, dst) {
			t.Fatal("stream mismatch", err)
		}
		if err = p.scan(raw, &f); err != nil {
			t.Fatal(err)
		}
		compactExperimentEqual(t, p.reference.base, values, f.Values)
		again, err := p.encodeReuse(nil, f.Values)
		if err != nil || !bytes.Equal(raw, again) {
			t.Fatal("borrowed reencode", err)
		}
	}
}
func TestSeparateWidthGoldenAndMalformed(t *testing.T) {
	p, err := compileSeparateWidthExperiment(compactTestDefinition("U16"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value uint64
		want  []byte
	}{{7, []byte{0x20, 0, 7}}, {256, []byte{0x20, 0x80, 0, 1}}} {
		raw, err := p.encodeReuse(nil, []Value{{}, Uint(tc.value, 2)})
		if err != nil || !bytes.Equal(raw, tc.want) {
			t.Fatalf("%x %v", raw, err)
		}
	}
	var f Frame
	for _, raw := range [][]byte{{0x20}, {0x20, 0}, {0x20, 1, 7}, {0x20, 0, 7, 0}, {0x30, 0, 7}, {0x21, 0, 7}} {
		if p.scan(raw, &f) == nil {
			t.Fatalf("accepted malformed %x", raw)
		}
	}
	for _, state := range []State{Absent, Null} {
		values := []Value{{State: state}, {State: state}}
		raw, err := p.encodeReuse(nil, values)
		if err != nil || len(raw) != 1 {
			t.Fatal("absent/null width mask", err)
		}
		if err = p.scan(raw, &f); err != nil {
			t.Fatal(err)
		}
		for _, v := range f.Values {
			if v.State != state {
				t.Fatal("state changed")
			}
		}
	}
	q, err := compileSeparateWidthExperiment(compactTestDefinition("U32"), true)
	if err != nil {
		t.Fatal(err)
	}
	if q.scan([]byte{0x20, 0xc0, 0, 0, 0, 0, 0, 0, 0, 0}, &f) == nil {
		t.Fatal("U32 accepted 8-byte selector")
	}
	if _, err = p.encode(nil, []Value{{}, Uint(65536, 4)}, true); err == nil {
		t.Fatal("out of contract")
	}
	if _, err = p.encodeReuse(nil, []Value{{}, {State: Offset, Bytes: []byte{7}}}); err == nil {
		t.Fatal("OFFSET integer accepted")
	}
}
func TestSeparateWidthMixedCrossing(t *testing.T) {
	p, err := compileSeparateWidthExperiment(compactTestDefinition("U16", "U32", "I64", "I16", "U16", "U64", "STRING", "U8", "I8"), true)
	if err != nil {
		t.Fatal(err)
	}
	values := []Value{{}, Uint(7, 2), Uint(65536, 4), Uint(^uint64(0), 8), Uint(128, 2), Uint(8, 2), Uint(65536, 8), Text("hello"), Uint(255, 1), Uint(128, 1)}
	raw, err := p.encode(nil, values, true)
	if err != nil {
		t.Fatal(err)
	}
	// 0 | 10 | 00 | 1 | 0 | 10 = 01000101 00000000.
	if !bytes.Equal(raw[p.mask:p.mask+2], []byte{0x45, 0}) {
		t.Fatalf("selectors %x", raw)
	}
	fixed, err := p.reference.base.Encode(nil, values)
	if err != nil || !bytes.Equal(raw[:p.mask], fixed[:p.mask]) {
		t.Fatal("mask changed", err)
	}
	var f Frame
	if err = p.scan(raw, &f); err != nil {
		t.Fatal(err)
	}
	compactExperimentEqual(t, p.reference.base, values, f.Values)
	for n := 0; n < len(raw); n++ {
		if p.scan(raw[:n], &f) == nil {
			t.Fatalf("truncation accepted %d", n)
		}
	}
}
func FuzzSeparateWidth(f *testing.F) {
	p, err := compileSeparateWidthExperiment(compactTestDefinition("U16", "I16", "U32", "I64", "STRING"), true)
	if err != nil {
		f.Fatal(err)
	}
	for _, v := range [][]Value{{{}, {}, {}, {}, {}, {}}, {{}, Uint(255, 2), Uint(65535, 2), Uint(400, 4), Uint(^uint64(128), 8), Text("seed")}} {
		raw, err := p.encodeReuse(nil, v)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 4096 {
			t.Skip()
		}
		var frame, again Frame
		if p.scan(raw, &frame) != nil {
			return
		}
		a, err := p.encode(nil, frame.Values, true)
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.encodeReuse(nil, frame.Values)
		if err != nil || !bytes.Equal(a, b) {
			t.Fatal("encoders differ", err)
		}
		if err = p.scan(a, &again); err != nil {
			t.Fatal(err)
		}
		compactExperimentEqual(t, p.reference.base, frame.Values, again.Values)
	})
}
