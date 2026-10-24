package cache

import (
	"bytes"
	"testing"
)

func TestCompactInteger16Exhaustive(t *testing.T) {
	d := compactTestDefinition("U16", "I16")
	p, e := compileInteger16Experiment(d)
	if e != nil {
		t.Fatal(e)
	}
	stream := compileStreamIntegerExperiment(p)
	if stream.reserved != 0 {
		t.Fatal("16-bit fields reserved selector bytes")
	}
	var f Frame
	var dst []byte
	for u := 0; u < 65536; u++ {
		values := []Value{{}, Uint(uint64(u), 2), Uint(uint64(u), 2)}
		raw, e := p.encode(nil, values, true)
		special, se := p.encode16(nil, values, true)
		if se != nil || !bytes.Equal(raw, special) {
			t.Fatal("specialized encoding differs", se)
		}
		special, se = stream.encode16(nil, values)
		if se != nil || !bytes.Equal(raw, special) {
			t.Fatal("specialized streaming differs", se)
		}
		if e != nil {
			t.Fatal(e)
		}
		dst, e = stream.encode(dst, values)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(raw, dst) {
			t.Fatal("stream and planned encoding differ")
		}
		unsignedSmall := u <= 255
		signedSmall := int16(u) >= -128 && int16(u) <= 127
		want := 5
		mask := byte(0x28)
		if unsignedSmall {
			want--
			mask |= 0x10
		}
		if signedSmall {
			want--
			mask |= 0x04
		}
		if len(raw) != want || raw[0] != mask {
			t.Fatalf("wrong implicit width for %d: %x", u, raw)
		}
		if e = p.scan(raw, &f); e != nil {
			t.Fatal(e)
		}
		compactExperimentEqual(t, p.reference.base, values, f.Values)
		if e = p.scanMode16(raw, &f, true); e != nil {
			t.Fatal(e)
		}
		compactExperimentEqual(t, p.reference.base, values, f.Values)
		again, e := stream.encode(nil, f.Values)
		if e != nil || !bytes.Equal(again, raw) {
			t.Fatal("borrowed reencode differs", e)
		}
		if unsignedSmall || signedSmall {
			if e = p.reference.base.Scan(raw, &f); e == nil {
				t.Fatal("old decoder accepted compact16")
			}
		}
	}
}
func TestCompactInteger16MixedAndStates(t *testing.T) {
	d := compactTestDefinition("U32", "I16", "I64", "U16", "U8", "I8", "STRING")
	p, e := compileInteger16Experiment(d)
	if e != nil {
		t.Fatal(e)
	}
	s := compileStreamIntegerExperiment(p)
	values := []Value{{}, Uint(256, 4), Uint(65535, 2), Uint(70000, 8), Uint(255, 2), Uint(7, 1), Uint(255, 1), Text("text")}
	raw, e := s.encode(nil, values)
	if e != nil {
		t.Fatal(e)
	}
	// Only U32 and I64 consume selectors: 1 + 2 bits, regardless of U16/I16.
	if raw[p.mask] != 0xc0 {
		t.Fatalf("16-bit selector shifted wider selectors: %x", raw)
	}
	var f Frame
	if e = p.scan(raw, &f); e != nil {
		t.Fatal(e)
	}
	compactExperimentEqual(t, p.reference.base, values, f.Values)
	if e = p.scanMode16(raw, &f, true); e != nil {
		t.Fatal(e)
	}
	compactExperimentEqual(t, p.reference.base, values, f.Values)
	for n := 0; n < len(raw); n++ {
		if p.scan(raw[:n], &f) == nil {
			t.Fatal("accepted truncation")
		}
	}
	for _, state := range []State{Absent, Null} {
		for i := range values {
			values[i] = Value{State: state}
		}
		raw, e = s.encode(nil, values)
		if e != nil || len(raw) != p.mask {
			t.Fatal("empty state added bytes", e)
		}
		if e = p.scan(raw, &f); e != nil {
			t.Fatal(e)
		}
		for _, v := range f.Values {
			if v.State != state {
				t.Fatal("state changed")
			}
		}
	}
	q, e := compileInteger16Experiment(compactTestDefinition("U16"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = q.encode(nil, []Value{{}, Uint(65536, 4)}, true); e == nil {
		t.Fatal("accepted out-of-width input")
	}
	if e = q.scan([]byte{0x30}, &f); e == nil {
		t.Fatal("accepted missing compact16 payload")
	}
	if e = q.scan([]byte{0x30, 7, 0}, &f); e == nil {
		t.Fatal("accepted extra selector/payload")
	}
}
func FuzzCompactInteger16(f *testing.F) {
	p, e := compileInteger16Experiment(compactTestDefinition("U16", "I16", "U32", "I64", "STRING"))
	if e != nil {
		f.Fatal(e)
	}
	s := compileStreamIntegerExperiment(p)
	for _, v := range [][]Value{{{}, {}, {}, {}, {}, {}}, {{}, Uint(255, 2), Uint(65535, 2), Uint(400, 4), Uint(^uint64(128), 8), Text("seed")}} {
		raw, e := s.encode(nil, v)
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
		ea, eb := p.scan(raw, &a), p.scanMode16(raw, &b, true)
		if (ea == nil) != (eb == nil) {
			t.Fatal("decoder acceptance differs", ea, eb)
		}
		if ea != nil {
			return
		}
		compactExperimentEqual(t, p.reference.base, a.Values, b.Values)
		planned, e := p.encode16(nil, a.Values, true)
		if e != nil {
			t.Fatal(e)
		}
		stream, e := s.encode16(nil, a.Values)
		if e != nil || !bytes.Equal(planned, stream) {
			t.Fatal("encoders differ", e)
		}
		if e = p.scan(stream, &b); e != nil {
			t.Fatal(e)
		}
		compactExperimentEqual(t, p.reference.base, a.Values, b.Values)
	})
}
