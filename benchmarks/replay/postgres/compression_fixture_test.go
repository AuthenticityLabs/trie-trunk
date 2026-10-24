package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBinaryCompressionFixturesContainOnlyPIL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fixtures")
	if err := exportCompressionFixtures(dir, "pil"); err != nil {
		t.Fatal(err)
	}
	l := layout()
	for _, objects := range []int{1, 32} {
		for _, size := range []int{256, 4096} {
			for _, variant := range []string{"full-pil", "partial-pil"} {
				raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%s_b%d_o%d.bin", variant, size, objects)))
				if err != nil {
					t.Fatal(err)
				}
				states := make([]Object, objects)
				for i := range states {
					states[i] = fixture(int64(i+1), 0, size)
				}
				for n := 0; n < 2048; n++ {
					if len(raw) < 4 {
						t.Fatal("missing fixture length")
					}
					size := int(binary.LittleEndian.Uint32(raw))
					raw = raw[4:]
					if size > len(raw) {
						t.Fatal("truncated fixture")
					}
					o := &states[n%objects]
					mutate(o, int(o.Revision)+1)
					if !bytes.Equal(raw[:size], encode(l, *o, objects, variant == "full-pil")) {
						t.Fatalf("non-PIL wrapper or incorrect fixture at %s/%d", variant, n)
					}
					raw = raw[size:]
				}
				if len(raw) != 0 {
					t.Fatal("unexpected trailing data")
				}
			}
		}
	}
}
