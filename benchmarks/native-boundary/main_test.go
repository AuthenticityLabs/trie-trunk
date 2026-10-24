package main

import (
	"bytes"
	"testing"
)

func TestOwnership(t *testing.T) {
	for _, mode := range []string{"go", "duplicate", "copy", "direct"} {
		t.Run(mode, func(t *testing.T) {
			c := config{Mode: mode, Bytes: 1025, Chunk: 127}
			r, e := receive(&patternReader{remaining: c.Bytes}, c)
			if e != nil {
				t.Fatal(e)
			}
			defer r.close()
			if r.sum != expected(c.Bytes) {
				t.Fatal("incorrect bytes")
			}
			wantGo, wantNative := uint64(0), uint64(0)
			if mode == "go" || mode == "duplicate" {
				wantGo = uint64(c.Bytes)
			}
			if mode != "go" {
				wantNative = uint64(c.Bytes)
			}
			if r.goBytes != wantGo || r.native.bytes() != wantNative {
				t.Fatalf("ownership: %d %d", r.goBytes, r.native.bytes())
			}
		})
	}
}
func TestTruncatedAndOversized(t *testing.T) {
	for _, n := range []int{1, 130} {
		if _, e := receive(bytes.NewReader(make([]byte, n)), config{Mode: "direct", Bytes: 128, Chunk: 17}); e == nil {
			t.Fatal("accepted malformed length")
		}
	}
}

func TestNativeLeaseStability(t *testing.T) {
	s := newNative()
	defer s.close()
	first := s.buffer(1025)
	for i := range first {
		first[i] = byte(i)
	}
	// Growth of the Rust buffer directory must not relocate existing leases.
	for i := 0; i < 1000; i++ {
		b := s.buffer(17)
		b[0] = byte(i)
	}
	if checksum(first) != expected(len(first)) {
		t.Fatal("native lease moved or changed")
	}
}
