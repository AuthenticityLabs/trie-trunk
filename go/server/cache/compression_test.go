package cache

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func compressionFrame(t testing.TB, l *Layout, n uint32, size int) []byte {
	t.Helper()
	raw := testFrame(t, l, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC), n, false)
	var f Frame
	if e := l.Scan(raw, &f); e != nil {
		t.Fatal(e)
	}
	f.Values[5] = Text(strings.Repeat("payment authorization pending: upstream connection timed out; retry scheduled. ", size/77+1)[:size])
	raw, e := l.Encode(nil, f.Values)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestEventCompressionRoundTrip(t *testing.T) {
	for _, adaptive := range []string{"none", "16", "32"} {
		for _, variable := range []bool{false, true} {
			l := testLayout(t, variable)
			d := l.Definition
			d.AdaptiveIntegers = adaptive
			if adaptive != "none" {
				d.PIL = 1
			}
			var e error
			l, e = Compile(d)
			if e != nil {
				t.Fatal(e)
			}
			raw := compressionFrame(t, l, 1, 4096)
			packed, e := l.CompressEvent(raw)
			if e != nil {
				t.Fatal(e)
			}
			if !l.IsCompressed(packed) || len(packed) >= len(raw) {
				t.Fatal("did not compress")
			}
			full, e := l.ExpandEvent(packed)
			if e != nil || !bytes.Equal(full, raw) {
				t.Fatalf("round trip: %v", e)
			}
			stream := bytes.NewReader(append(append([]byte{}, packed...), raw...))
			a, e := l.ReadFrame(stream, nil)
			if e != nil || !bytes.Equal(a, packed) {
				t.Fatal(e)
			}
			v, e := l.ReadFrame(stream, nil)
			if e != nil || !bytes.Equal(v, raw) {
				t.Fatal(e)
			}
			small := testFrame(t, l, time.Now(), 2, true)
			got, e := l.CompressEvent(small)
			if e != nil || !bytes.Equal(got, small) {
				t.Fatal("small fallback", e)
			}
			for _, mutate := range []func([]byte){func(b []byte) { b[len(b)-5] ^= 1 }, func(b []byte) { order.PutUint32(b[(len(l.Widths)+3)/4+8:], MaxFrame) }, func(b []byte) { b[(len(l.Widths)+3)/4+3] = 2 }} {
				bad := append([]byte(nil), packed...)
				mutate(bad)
				var f Frame
				if l.Scan(bad, &f) == nil {
					t.Fatal("accepted corrupt envelope")
				}
			}
			for _, bad := range [][]byte{packed[:len(packed)-1], append(append([]byte{}, packed...), 0)} {
				var f Frame
				if l.Scan(bad, &f) == nil {
					t.Fatal("accepted wrong boundary")
				}
			}
		}
	}
}
func TestCompressedStoreRecoveryAndDelivery(t *testing.T) {
	l := testLayout(t, false)
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cfg := Config{Root: t.TempDir(), Cache: "demo", Tenant: "test", Layout: l, Now: func() time.Time { return at.Add(3 * time.Hour) }}
	raw := testFrame(t, l, at, 1, false)
	packed, e := l.CompressEvent(raw)
	if e != nil {
		t.Fatal(e)
	}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Append(Write, packed); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got := collect(t, s, Write, at, at.Add(time.Hour))
	if len(got) != 1 || !l.sameEvent(got[0], raw) {
		t.Fatal("compressed ingress changed logical event")
	}
	b := s.branches[Write]
	if !b.runs[0].PAC.Gzip {
		t.Fatal("non-collection write PAC was not compressed")
	}
	population, e := readBytes(b.pop, b.runs[0].PopStart, b.runs[0].PopEnd)
	if e != nil {
		t.Fatal(e)
	}
	p, e := decodeStoredPAC(l, b.runs[0], population)
	if e != nil || len(p.Members) != 1 {
		t.Fatal("invalid stored PAC", e)
	}
}

func TestIncompressibleEventFallback(t *testing.T) {
	l := testLayout(t, false)
	d := l.Definition
	d.Fields[5].Type = "BYTES"
	var e error
	l, e = Compile(d)
	if e != nil {
		t.Fatal(e)
	}
	raw := compressionFrame(t, l, 1, 4096)
	var f Frame
	if e = l.Scan(raw, &f); e != nil {
		t.Fatal(e)
	}
	random := rand.New(rand.NewSource(42))
	data := make([]byte, 4096)
	random.Read(data)
	f.Values[5] = Value{Offset, data}
	raw, e = l.Encode(nil, f.Values)
	if e != nil {
		t.Fatal(e)
	}
	packed, e := l.CompressEvent(raw)
	if e != nil || !bytes.Equal(packed, raw) {
		t.Fatal("incompressible body grew", e)
	}
}
