package cache

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func adaptiveLayout(t *testing.T, d Definition, mode string) *Layout {
	t.Helper()
	d.PIL = 1
	d.AdaptiveIntegers = mode
	l, e := Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func TestAdaptiveIntegersMatchOptimizedOracle(t *testing.T) {
	d := compactTestDefinition("U16", "I16", "U32", "I32", "U64", "I64", "STRING")
	for _, mode := range []string{"16", "32"} {
		l := adaptiveLayout(t, d, mode)
		oracle, e := compileSeparateWidthExperiment(l.Definition, mode == "16")
		if e != nil {
			t.Fatal(e)
		}
		var raw, again []byte
		var f Frame
		for u := 0; u < 65536; u++ {
			v := []Value{{}, Uint(uint64(u), 2), Uint(uint64(u), 2), Uint(uint64(u)*65537, 4), Uint(uint64(int64(int16(u))), 4), Uint(uint64(u)<<40, 8), Uint(uint64(int64(int16(u))), 8), Text("λ")}
			if u%3 == 0 {
				v[1] = Value{}
				v[4] = Value{Null, nil}
			}
			v = append(v, Value{}, Value{}, Value{})
			want, e := oracle.encodeReuse(nil, v)
			if e != nil {
				t.Fatal(e)
			}
			raw, e = l.Encode(raw, v)
			if e != nil || !bytes.Equal(raw, want) {
				t.Fatalf("%s %d: %x != %x: %v", mode, u, raw, want, e)
			}
			if e = l.Scan(raw, &f); e != nil {
				t.Fatal(e)
			}
			for i := range v {
				if !l.equal(i, v[i], f.Values[i]) {
					t.Fatalf("value %d mismatch", i)
				}
			}
			again, e = l.Encode(again, f.Values)
			if e != nil || !bytes.Equal(raw, again) {
				t.Fatal("borrowed reencode", e)
			}
			stream := bytes.NewReader(append(append([]byte{}, raw...), raw...))
			for j := 0; j < 2; j++ {
				got, e := l.ReadFrame(stream, nil)
				if e != nil || !bytes.Equal(got, raw) {
					t.Fatal("stream", e)
				}
			}
			if _, e = l.ReadFrame(stream, nil); e != io.EOF {
				t.Fatal("stream end", e)
			}
		}
	}
}
func TestAdaptiveIntegersStorage(t *testing.T) {
	for _, mode := range []string{"16", "32"} {
		d := testLayout(t, false).Definition
		d.Resource.Path = []string{"amount"}
		l := adaptiveLayout(t, d, mode)
		at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		c := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true, BatchRecords: 2}
		s, e := Open(c)
		if e != nil {
			t.Fatal(e)
		}
		full, sparse := testFrame(t, l, at, 1, false), testFrame(t, l, at.Add(time.Second), 2, true)
		for _, raw := range [][]byte{full, sparse} {
			if _, e = s.Append(Write, raw); e != nil {
				t.Fatal(e)
			}
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
		s, e = Open(c)
		if e != nil {
			t.Fatal(e)
		}
		rows := collect(t, s, Write, at, at.Add(time.Minute))
		if len(rows) != 2 || !sameSeedData(t, l, rows[0], full) || !sameSeedData(t, l, rows[1], sparse) {
			t.Fatal("native assembly mismatch")
		}
		if dup, e := s.Append(Write, full); e == nil || dup {
			t.Fatal("dedup", e)
		}
		locations, e := s.ObjectLocations(testObject(42))
		if e != nil || len(locations) != 2 {
			t.Fatal("object lookup", locations, e)
		}
		count := 0
		e = s.Search(context.Background(), Query{Branch: Write, From: at, To: at.Add(time.Minute), Equals: map[string]Value{"amount": Uint(999, 8)}}, func(_ uint64, b []byte) error { count++; return nil })
		if e != nil || count != 1 {
			t.Fatal("predicate", e, count)
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
func TestAdaptiveMalformedAndVersion(t *testing.T) {
	d := compactTestDefinition("U16")
	d.AdaptiveIntegers = "16"
	if _, e := Compile(d); e == nil {
		t.Fatal("old version accepts changed wire layout")
	}
	l := adaptiveLayout(t, compactTestDefinition("U16"), "16")
	for _, raw := range [][]byte{{0x20}, {0x20, 0}, {0x20, 1, 7}, {0x20, 0x80, 7, 0}, {0x30, 0, 7}, {0x21, 0, 7}} {
		var f Frame
		if l.Scan(raw, &f) == nil {
			t.Fatalf("accepted %x", raw)
		}
	}
	raw, e := l.Encode(nil, []Value{{}, Uint(7, 2)})
	if e != nil || !bytes.Equal(raw, []byte{0x20, 0, 0, 7}) {
		t.Fatalf("golden %x %v", raw, e)
	}
	for i := 0; i < len(raw); i++ {
		var f Frame
		if l.Scan(raw[:i], &f) == nil {
			t.Fatal("truncation")
		}
	}
}

func TestAdaptiveIdentityAcrossDefinitionMinors(t *testing.T) {
	d := testLayout(t, false).Definition
	root := t.TempDir()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for minor := uint32(0); minor < 2; minor++ {
		d.Resource.Minor = minor
		d.Version = 1<<20 | uint64(minor)
		if minor == 1 {
			d.PIL = 1
			d.AdaptiveIntegers = "32"
		}
		l, e := Compile(d)
		if e != nil {
			t.Fatal(e)
		}
		s, e := Open(Config{Root: root, Cache: "demo", Tenant: "test", Layout: l, ObjectIndex: true})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Append(Write, testFrame(t, l, at.Add(time.Duration(minor)*time.Second), minor+1, false)); e != nil {
			t.Fatal(e)
		}
		found, e := s.ObjectLocations(testObject(42))
		if e != nil || len(found) != int(minor+1) {
			t.Fatalf("cross-minor identities: %v %v", found, e)
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
