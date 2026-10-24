package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type compressionResult struct {
	Variant         string  `json:"variant"`
	TextBytes       int     `json:"description_bytes"`
	Level           int     `json:"gzip_level"`
	Round           int     `json:"round"`
	Updates         int     `json:"updates"`
	RawBytes        float64 `json:"raw_bytes_per_update"`
	CompressedBytes float64 `json:"gzip_bytes_per_update"`
	CompressionUS   float64 `json:"compression_us_per_update"`
}

// Compression-only control: persistent gzip context, Flush for every update.
// Prepared packets isolate compression from SQL, codec, transport and client work.
func runStreamCompression(output string) error {
	const count, warm = 2048, 16
	var results []compressionResult
	l := layout()
	for round := 1; round <= 3; round++ {
		for _, size := range []int{256, 4096} {
			for _, variant := range []string{"pg-json", "pg-pil", "trunk-direct"} {
				packets := make([][]byte, count)
				for i := range packets {
					o := fixture(1, i+1, size)
					if variant == "pg-json" {
						raw, err := json.Marshal(o)
						if err != nil {
							return err
						}
						packets[i] = append(raw, '\n')
					} else {
						packets[i] = encode(l, o, 1, variant == "pg-pil")
					}
				}
				want := bytes.Join(packets, nil)
				for _, level := range []int{gzip.BestSpeed, gzip.DefaultCompression} {
					var out bytes.Buffer
					out.Grow(len(want))
					z, err := gzip.NewWriterLevel(&out, level)
					if err != nil {
						return err
					}
					var begin time.Time
					before, rawBytes := 0, 0
					for i, packet := range packets {
						if i == warm {
							before = out.Len()
							begin = time.Now()
						}
						if _, err = z.Write(packet); err != nil {
							return err
						}
						if err = z.Flush(); err != nil {
							return err
						}
						if i >= warm {
							rawBytes += len(packet)
						}
					}
					elapsed := time.Since(begin)
					encodedBytes := out.Len() - before
					if err = z.Close(); err != nil {
						return err
					}
					r, err := gzip.NewReader(bytes.NewReader(out.Bytes()))
					if err != nil {
						return err
					}
					got, err := io.ReadAll(r)
					r.Close()
					if err != nil || !bytes.Equal(got, want) {
						return fmt.Errorf("gzip changed binary sequence: %v", err)
					}
					n := float64(count - warm)
					results = append(results, compressionResult{variant, size, level, round, count - warm, float64(rawBytes) / n, float64(encodedBytes) / n, micros(elapsed) / n})
				}
			}
		}
	}
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(output, append(raw, '\n'), 0644)
}
