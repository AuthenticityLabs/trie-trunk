package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// Emit identical prepared packets for both native codec implementations.
// File format is benchmark-only: repeated uint32 little-endian length + packet.
func exportCompressionFixtures(dir, format string) error {
	if format != "pil" {
		return fmt.Errorf("only raw PIL fixtures are supported, got %q", format)
	}
	variants := []string{"full-pil", "partial-pil"}
	if err := os.Mkdir(dir, 0755); err != nil {
		return err
	}
	l := layout()
	for _, objects := range []int{1, 32} {
		for _, size := range []int{256, 4096} {
			for _, variant := range variants {
				states := make([]Object, objects)
				for i := range states {
					states[i] = fixture(int64(i+1), 0, size)
				}
				var body []byte
				for n := 0; n < 2048; n++ {
					at := n % objects
					o := &states[at]
					mutate(o, int(o.Revision)+1)
					packet := encode(l, *o, objects, variant == "full-pil")
					body = binary.LittleEndian.AppendUint32(body, uint32(len(packet)))
					body = append(body, packet...)
				}
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s_b%d_o%d.bin", variant, size, objects)), body, 0644); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
