package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"trie-trunk/go/server/cache"
)

type compressedSize struct{ n int64 }

func (s *compressedSize) Write(p []byte) (int, error) { s.n += int64(len(p)); return len(p), nil }

// Compare complete standalone PACs, not Pods that place event UUIDs in .idx.
// Both formats retain every payload field; PACSource retains routing context,
// and time is reconstructed from the complete event UUID. Definitions count once.
func TestStandalonePACCompression(t *testing.T) {
	root := os.Getenv("TRUNK_COMPRESSION_MATCHED")
	if root == "" {
		t.Skip("set TRUNK_COMPRESSION_MATCHED to a footprint export")
	}
	input, err := os.Open(filepath.Join(root, "expanded.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	metaBytes, err := os.ReadFile(filepath.Join(root, "footprint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Events int    `json:"events"`
		Hash   string `json:"expanded_jsonl_sha256"`
	}
	if err = json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	layouts := map[string]*cache.Layout{}
	var definitionBytes int64
	for _, s := range scenarios {
		l, err := cache.Compile(definition(s))
		if err != nil {
			t.Fatal(err)
		}
		layouts[s.service+"/"+s.resource+".v1"] = l
		definitionBytes += int64(len(l.Bytes))
	}
	codecFile, err := os.Create(filepath.Join(root, "codec-inputs.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer codecFile.Close()
	codecWriter := bufio.NewWriterSize(codecFile, 256*1024)
	var hourJSON bytes.Buffer
	levels := []int{1, 6, 9}
	hourly, pac := make([]*gzip.Writer, len(levels)), make([]*gzip.Writer, len(levels))
	hourSizes, pacSizes := make([]compressedSize, len(levels)), make([]compressedSize, len(levels))
	hourTotals, pacTotals := make([]int64, len(levels)), make([]int64, len(levels))
	for i, level := range levels {
		hourly[i], err = gzip.NewWriterLevel(&hourSizes[i], level)
		if err != nil {
			t.Fatal(err)
		}
		pac[i], err = gzip.NewWriterLevel(&pacSizes[i], level)
		if err != nil {
			t.Fatal(err)
		}
	}
	var frames [][]byte
	var layout *cache.Layout
	var key, source string
	groups, events := 0, 0
	var plainPACBytes, sourceBytes int64
	flush := func() {
		if len(frames) == 0 {
			return
		}
		p, err := cache.PackPAC(layout, frames, []cache.PACExtension{{Type: cache.PACSource, Data: []byte(source)}}, true)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := cache.ParsePAC(layout, p.Bytes, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(decoded.Members) != len(frames) {
			t.Fatal("member count mismatch")
		}
		for i, member := range decoded.Members {
			raw, err := cache.ResolvePAC(layout, decoded.Common, member)
			if err != nil || !bytes.Equal(raw, frames[i]) {
				t.Fatal("PAC changed payload or UUID", err)
			}
		}
		// Prepared identical hourly inputs for isolated compression/decompression timing.
		for _, payload := range [][]byte{hourJSON.Bytes(), p.Bytes} {
			if err := binary.Write(codecWriter, binary.LittleEndian, uint32(len(payload))); err != nil {
				t.Fatal(err)
			}
			if _, err := codecWriter.Write(payload); err != nil {
				t.Fatal(err)
			}
		}
		hourJSON.Reset()
		plainPACBytes += int64(len(p.Bytes))
		for i := range levels {
			if err := hourly[i].Close(); err != nil {
				t.Fatal(err)
			}
			hourTotals[i] += hourSizes[i].n
			hourSizes[i].n = 0
			hourly[i].Reset(&hourSizes[i])
			pacSizes[i].n = 0
			pac[i].Reset(&pacSizes[i])
			if _, err := pac[i].Write(p.Bytes); err != nil {
				t.Fatal(err)
			}
			if err := pac[i].Close(); err != nil {
				t.Fatal(err)
			}
			// Match compressPAC: retain plain PAC when framing+gzip would be larger.
			pacTotals[i] += min(int64(len(p.Bytes)), pacSizes[i].n+cache.PACCompressedHeaderSize)
		}
		groups++
		frames = nil
	}
	hash := sha256.New()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var object map[string]json.RawMessage
		if err := json.Unmarshal(line, &object); err != nil {
			t.Fatal(err)
		}
		text := func(name string) string {
			var s string
			if err := json.Unmarshal(object[name], &s); err != nil {
				t.Fatal(name, err)
			}
			return s
		}
		resource := text("service") + "/" + text("resource")
		at, err := time.Parse(time.RFC3339Nano, text("time"))
		if err != nil {
			t.Fatal(err)
		}
		nextSource := text("cache") + "/" + resource + "/" + text("branch")
		next := nextSource + "/" + at.UTC().Truncate(time.Hour).Format(time.RFC3339)
		if next != key {
			flush()
			key, source, layout = next, nextSource, layouts[resource]
			if layout == nil {
				t.Fatal("unknown resource", resource)
			}
		}
		values := make([]cache.Value, len(layout.Definition.Fields))
		for i, f := range layout.Definition.Fields {
			raw, ok := object[f.Name]
			if !ok {
				continue
			}
			if bytes.Equal(raw, []byte("null")) {
				values[i] = cache.Value{State: cache.Null}
				continue
			}
			switch f.Type {
			case "UUID":
				b, err := hex.DecodeString(strings.ReplaceAll(text(f.Name), "-", ""))
				if err != nil || len(b) != 16 {
					t.Fatal("invalid UUID", err)
				}
				values[i] = cache.Value{State: cache.Direct, Bytes: b}
			case "STRING":
				s := text(f.Name)
				values[i] = cache.Text(s)
				if len(f.Values) > 0 {
					found := false
					for n, v := range f.Values {
						if s == v {
							values[i] = cache.Uint(uint64(n), layout.Widths[i])
							found = true
							break
						}
					}
					if !found {
						t.Fatal("unknown dictionary value", f.Name, s)
					}
				}
			case "BOOL":
				var v bool
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatal(err)
				}
				n := uint64(0)
				if v {
					n = 1
				}
				values[i] = cache.Uint(n, 1)
			case "U8", "U16", "U32", "U64":
				n, err := strconv.ParseUint(string(raw), 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				bits, _ := strconv.Atoi(f.Type[1:])
				values[i] = cache.Uint(n, bits/8)
			default:
				t.Fatal("unsupported fixture type", f.Type)
			}
		}
		frame, err := layout.Encode(nil, values)
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
		// The exported timestamp must be recoverable from the event UUID.
		var ms int64
		for _, b := range values[layout.Event].Bytes[:6] {
			ms = ms<<8 | int64(b)
		}
		if ms != at.UnixMilli() {
			t.Fatal("timestamp mismatch")
		}
		for _, w := range []io.Writer{hash, &hourJSON, hourly[0], hourly[1], hourly[2]} {
			if _, err := w.Write(line); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte{'\n'}); err != nil {
				t.Fatal(err)
			}
		}
		sourceBytes += int64(len(line) + 1)
		events++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	flush()
	if events != meta.Events || hex.EncodeToString(hash.Sum(nil)) != meta.Hash {
		t.Fatal("source mismatch")
	}
	if err := codecWriter.Flush(); err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{}
	for i, level := range levels {
		rows = append(rows, map[string]any{"gzip_level": level, "jsonl_hourly_bytes": hourTotals[i], "standalone_pac_bytes": pacTotals[i] + definitionBytes, "definition_bytes": definitionBytes})
	}
	result := map[string]any{"events": events, "hourly_packets": groups, "source_bytes": sourceBytes, "source_sha256": meta.Hash, "go": runtime.Version(), "plain_pac_bytes": plainPACBytes + definitionBytes, "levels": rows, "notes": []string{"Identical exported records, grouped by resource, branch and UTC hour; Go compress/gzip for both formats.", "Standalone PACs retain full UUIDs and every payload field; routing in PACSource; timestamps verified against UUIDs. Definitions counted once; no external index needed for decoding.", "Every PAC member resolved and checked byte-for-byte against its full input frame. PAC compression framing included. JSON uses newline-delimited objects; no pretty-printing."}}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "compression.json"), append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	t.Log(fmt.Sprintf("%d records, %d hourly packets: %s", events, groups, raw))
}
