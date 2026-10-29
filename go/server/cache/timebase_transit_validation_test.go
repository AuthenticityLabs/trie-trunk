package cache

import (
	"bytes"
	"math"
	"testing"
)

func TestTimebaseTransitBoundaries(t *testing.T) {
	d := compactTestDefinition("TIMESTAMP_MS", "STRING", "U16")
	l, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	p := compileTimebaseExperiment(l)
	const base = int64(1790812800000)
	for _, offset := range []int64{0, 1, 255, 65535, 2678399999, math.MaxUint32} {
		v := []Value{uuid(base+offset, 42), Uint(uint64(base+offset), 8), Text("text"), Uint(500, 2)}
		absolute, err := l.Encode(nil, v)
		if err != nil {
			t.Fatal(err)
		}
		compact, err := p.transform(nil, absolute, base, false)
		if err != nil || len(absolute)-len(compact) != 10 {
			t.Fatal("compact", err)
		}
		restored, err := p.transform(nil, compact, base, true)
		if err != nil || !bytes.Equal(restored, absolute) {
			t.Fatal("exact reconstruction", err)
		}
		if _, err = p.transform(nil, absolute, base+offset+1, false); err == nil {
			t.Fatal("accepted before base")
		}
	}
	for _, state := range []State{Absent, Null} {
		v := []Value{{State: state}, {State: state}, Text("text"), Uint(500, 2)}
		raw, err := l.Encode(nil, v)
		if err != nil {
			t.Fatal(err)
		}
		compact, err := p.transform(nil, raw, base, false)
		if err != nil || !bytes.Equal(compact, raw) {
			t.Fatal("absent/null changed", err)
		}
		expanded, err := p.transform(nil, compact, base, true)
		if err != nil || !bytes.Equal(expanded, raw) {
			t.Fatal("absent/null restore", err)
		}
	}
	tooFar, _ := l.Encode(nil, []Value{uuid(base+math.MaxUint32+1, 1), {}, Text(""), {}})
	if _, err = p.transform(nil, tooFar, base, false); err == nil {
		t.Fatal("accepted out of range")
	}
	raw, _ := l.Encode(nil, []Value{uuid(base, 1), {}, Text(""), {}})
	bad := bytes.Clone(raw)
	bad[p.mask+6] = 0x40
	if _, err = p.transform(nil, bad, base, false); err == nil {
		t.Fatal("accepted UUIDv4")
	}
}
func TestTimebaseTransitPartialCollect(t *testing.T) {
	p, rows, blocks := timebaseFixture(t, 10000, true, true)
	for _, mode := range []string{"absolute_forward", "relative_forward", "relative_expand", "absolute_compact"} {
		emitted, packets := int64(0), int64(0)
		// Noncontiguous hits, source block transition, and revisiting a prior base.
		selected := []int{0, 4000, 4096, 5000, 9999, 0}
		out, err := p.collect(rows, blocks, selected, mode, make([]byte, 0, 65536), func(packet []byte) error { emitted += int64(len(packet)); packets++; return nil })
		if err != nil || out.Records != int64(len(selected)) || out.Bytes != emitted || out.Packets != packets {
			t.Fatal("accounting", mode, out, err)
		}
	}
}
