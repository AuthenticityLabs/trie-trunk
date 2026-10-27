package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

// Opt-in CPU measurements over the exact inputs prepared by the size comparison.
// Disk reads, PIL packing, JSON serialization, and verification are outside timers.
func TestHourlyCompressionTimings(t *testing.T) {
	root := os.Getenv("TRUNK_COMPRESSION_TIMING")
	if root == "" {
		t.Skip("set TRUNK_COMPRESSION_TIMING to a matched compression fixture")
	}
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	const repeats = 3
	levels := []int{1, 6, 9}
	type sample struct {
		EncodeMS float64 `json:"encode_ms"`
		DecodeMS float64 `json:"decode_ms"`
	}
	type measurement struct {
		Format   string   `json:"format"`
		Level    int      `json:"gzip_level"`
		Samples  []sample `json:"samples"`
		EncodeMS float64  `json:"encode_ms"`
		DecodeMS float64  `json:"decode_ms"`
	}
	rows := make([]measurement, 6)
	writers := make([]*gzip.Writer, 6)
	buffers := make([]bytes.Buffer, 6)
	readers := make([]*gzip.Reader, 6)
	for i := range rows {
		rows[i].Format = []string{"jsonl", "seed_packs"}[i%2]
		rows[i].Level = levels[i/2]
		var err error
		writers[i], err = gzip.NewWriterLevel(&buffers[i], rows[i].Level)
		if err != nil {
			t.Fatal(err)
		}
	}
	var inputs [2][]byte
	var decoded []byte
	var packetCount int
	for pass := 0; pass < repeats; pass++ {
		file, err := os.Open(filepath.Join(root, "codec-inputs.bin"))
		if err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReaderSize(file, 256*1024)
		encode, decode := make([]time.Duration, 6), make([]time.Duration, 6)
		packets := 0
		for {
			for format := 0; format < 2; format++ {
				var n uint32
				err = binary.Read(reader, binary.LittleEndian, &n)
				if format == 0 && err == io.EOF {
					break
				}
				if err != nil || n == 0 || n > 64*1024*1024 {
					t.Fatalf("invalid fixture boundary: %d: %v", n, err)
				}
				if cap(inputs[format]) < int(n) {
					inputs[format] = make([]byte, n)
				} else {
					inputs[format] = inputs[format][:n]
				}
				if _, err = io.ReadFull(reader, inputs[format]); err != nil {
					t.Fatal(err)
				}
			}
			if err == io.EOF {
				break
			}
			// Rotate order each pass to avoid giving one format a consistent warm-up bias.
			for step := 0; step < len(rows); step++ {
				i := (step + pass) % len(rows)
				raw := inputs[i%2]
				buffers[i].Reset()
				started := time.Now()
				writers[i].Reset(&buffers[i])
				_, err = writers[i].Write(raw)
				if err == nil {
					err = writers[i].Close()
				}
				encode[i] += time.Since(started)
				if err != nil {
					t.Fatal(err)
				}
				packed := buffers[i].Bytes()
				plain := i%2 == 1 && len(packed)+16 >= len(raw)
				if cap(decoded) < len(raw) {
					decoded = make([]byte, len(raw))
				} else {
					decoded = decoded[:len(raw)]
				}
				started = time.Now()
				if plain {
					copy(decoded, raw)
				} else {
					if readers[i] == nil {
						readers[i], err = gzip.NewReader(bytes.NewReader(packed))
					} else {
						err = readers[i].Reset(bytes.NewReader(packed))
					}
					if err == nil {
						_, err = io.ReadFull(readers[i], decoded)
					}
					if err == nil {
						var extra [1]byte
						var n int
						n, err = readers[i].Read(extra[:])
						if err == io.EOF && n == 0 {
							err = nil
						} else if err == nil {
							t.Fatal("unexpected decoded tail")
						}
					}
					if readers[i] != nil {
						closeErr := readers[i].Close()
						if err == nil {
							err = closeErr
						}
					}
				}
				decode[i] += time.Since(started)
				if err != nil || !bytes.Equal(raw, decoded) {
					t.Fatal("compression round trip", err)
				}
			}
			packets++
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		if pass > 0 && packets != packetCount {
			t.Fatal("packet count changed")
		}
		packetCount = packets
		for i := range rows {
			rows[i].Samples = append(rows[i].Samples, sample{float64(encode[i]) / float64(time.Millisecond), float64(decode[i]) / float64(time.Millisecond)})
		}
	}
	for i := range rows {
		enc, dec := make([]float64, repeats), make([]float64, repeats)
		for n, s := range rows[i].Samples {
			enc[n], dec[n] = s.EncodeMS, s.DecodeMS
		}
		sort.Float64s(enc)
		sort.Float64s(dec)
		rows[i].EncodeMS, rows[i].DecodeMS = enc[repeats/2], dec[repeats/2]
	}
	raw, err := os.ReadFile(filepath.Join(root, "compression.json"))
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Hash    string `json:"source_sha256"`
		Events  int    `json:"events"`
		Packets int    `json:"hourly_packets"`
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if packetCount != source.Packets {
		t.Fatal("size/timing packet mismatch")
	}
	result := map[string]any{"source_sha256": source.Hash, "events": source.Events, "hourly_packets": packetCount, "go": runtime.Version(), "cpu": os.Getenv("TRUNK_BENCHMARK_CPU"), "os": runtime.GOOS, "arch": runtime.GOARCH, "gomaxprocs": 1, "repeats": repeats, "rows": rows, "notes": []string{"Median total compression/decompression elapsed time across three serial passes; same prebuilt hourly inputs and Go compress/gzip implementation. Each result verified byte-for-byte.", "Excludes disk I/O, JSON serialization, PIL encoding/packing, schema compilation and rendering. This is compressor cost, not end-to-end ingestion or retrieval latency.", "Seed packs use uncompressed fallback when gzip plus its 16-byte header would grow the packet. Decode then copies the plain bytes. Definitions are counted in size but not repeated in timed packet operations."}}
	raw, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "compression-timing.json"), append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", raw)
}
