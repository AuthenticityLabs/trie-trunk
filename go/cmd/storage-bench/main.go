// storage-bench records reproducible storage size and latency observations.
// Fixture generation is deliberately outside the timed ingestion and query loops.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"trie-trunk/go/codec"
	"trie-trunk/go/server/search"
	"trie-trunk/go/server/store"
)

type counter int64

func (c *counter) Write(p []byte) (int, error) { *c += counter(len(p)); return len(p), nil }

type latency struct {
	Samples []int64 `json:"samples_ns"`
	P50     int64   `json:"p50_ns"`
	P95     int64   `json:"p95_ns"`
}

func measure(n int, run func() error) (latency, error) {
	out := latency{}
	for i := 0; i < n; i++ {
		start := time.Now()
		if err := run(); err != nil {
			return out, err
		}
		out.Samples = append(out.Samples, time.Since(start).Nanoseconds())
	}
	sorted := append([]int64(nil), out.Samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out.P50 = sorted[(len(sorted)-1)/2]
	out.P95 = sorted[(95*len(sorted)+99)/100-1]
	return out, nil
}

type report struct {
	Format           string                      `json:"format"`
	Count            int                         `json:"event_count"`
	Shape            string                      `json:"shape"`
	Aggregates       bool                        `json:"aggregates"`
	FixtureHash      string                      `json:"fixture_sha256"`
	Start            time.Time                   `json:"from"`
	End              time.Time                   `json:"to"`
	Go               string                      `json:"go"`
	Platform         string                      `json:"platform"`
	CPU              int                         `json:"logical_cpus"`
	IngestNS         int64                       `json:"ingest_ns"`
	ReopenNS         int64                       `json:"reopen_ns"`
	Bytes            map[string]int64            `json:"logical_bytes"`
	Files            int                         `json:"file_count"`
	Wire             map[string]int64            `json:"pil_response_bytes"`
	Query            map[string]latency          `json:"query_latency"`
	AggregateCount   uint64                      `json:"aggregate_count"`
	AggregateSum     uint64                      `json:"aggregate_sum"`
	Notes            []string                    `json:"notes"`
	RowWidth         int                         `json:"row_width,omitempty"`
	ObjectIndex      bool                        `json:"object_index"`
	ObjectIndexBytes int64                       `json:"object_index_bytes"`
	NativeBytes      map[string]map[string]int64 `json:"native_chunk_bytes,omitempty"`
	NativeLatency    map[string]latency          `json:"native_collection_latency,omitempty"`
}

func run() error {
	count := flag.Int("events", 50000, "deterministic events over 30 days")
	shape := flag.String("shape", "full", "full or sparse")
	aggregates := flag.Bool("aggregates", true, "existing five-minute count/duration aggregates")
	samples := flag.Int("samples", 9, "warm query observations per window")
	output := flag.String("out", "", "JSON report file")
	root := flag.String("root", "", "empty fixture root; defaults to an isolated temporary directory")
	backend := flag.String("backend", "legacy", "legacy or cache")
	objects := flag.Bool("object-index", false, "include optional cache object history index")
	flag.Parse()
	if *count < 1 || *samples < 1 || (*shape != "full" && *shape != "sparse") || *output == "" || (*backend != "legacy" && *backend != "cache") {
		return fmt.Errorf("invalid benchmark arguments")
	}
	if *objects && *backend != "cache" {
		return fmt.Errorf("--object-index requires --backend cache")
	}
	if *root == "" {
		p, e := os.MkdirTemp("", "trunk-storage-bench-")
		if e != nil {
			return e
		}
		*root = p
		defer os.RemoveAll(p)
	} else {
		if entries, e := os.ReadDir(*root); e == nil && len(entries) != 0 {
			return fmt.Errorf("fixture root must be empty")
		} else if e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 30)
	now := end.Add(time.Hour)
	d := codec.Definition{Object: "transactions", Version: 1 << 20, Timezone: "UTC", Storage: &codec.StoragePolicy{Format: 4, Boundary: "resource", Bucket: "hour"},
		Fields: codec.Schema{
			0: {Name: "id", Encoding: "uuidv7"}, 1: {Name: "object_id"}, 2: {Name: "revision_id"}, 3: {Name: "duration", Unit: "us"},
			4: {Name: "level", Enums: map[uint16]string{1: "INFO", 2: "ERROR"}}, 5: {Name: "service", Enums: map[uint16]string{1: "payment"}},
			6: {Name: "resource", Enums: map[uint16]string{1: "transactions"}}, 7: {Name: "operation", Enums: map[uint16]string{1: "update"}},
			8: {Name: "state", Enums: map[uint16]string{1: "pending", 2: "authorized", 3: "canceled"}}, 9: {Name: "amount"},
			10: {Name: "currency", Enums: map[uint16]string{1: "USD"}}, 11: {Name: "customer_id"},
		}, Path: []codec.Key{4, 5, 6, 7, 8, 10}}
	if *aggregates {
		d.Aggregations = []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}
	}
	rev, definition, err := d.Prepare()
	if err != nil {
		return err
	}
	frames := make([][]byte, *count)
	hash := sha256.New()
	var sum uint64
	for i := range frames {
		ms := start.UnixMilli() + int64(i)*(end.UnixMilli()-start.UnixMilli())/int64(*count)
		var id codec.UUIDv7
		binary.BigEndian.PutUint64(id[:8], uint64(ms)<<16)
		id[6] = 0x70
		id[8] = 0x80
		binary.BigEndian.PutUint32(id[12:], uint32(i))
		duration := uint64(50 + (i*7919)%100000)
		sum += duration
		r := codec.Record{0: id, 1: uint64(i % 10000), 2: uint64(i/10000 + 1), 3: duration, 4: uint64(1 + i%20/19), 5: uint64(1), 6: uint64(1), 7: uint64(1), 8: uint64(1 + i%3)}
		if *shape == "full" {
			r[9] = uint64(i % 100000)
			r[10] = uint64(1)
			r[11] = uint64(i % 5000)
		}
		// Sparse fixtures retain the same envelope but have just the changed state.
		frame, e := codec.MarshalCompact(r, codec.CBOR)
		if e != nil {
			return e
		}
		frames[i] = frame
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(frame)))
		hash.Write(size[:])
		hash.Write(frame)
	}
	if *backend == "cache" {
		return native(*root, *output, *shape, *samples, *aggregates, *objects, frames, hex.EncodeToString(hash.Sum(nil)), sum, start, end, now)
	}
	config := store.Config{Root: *root, Tap: "bench", MaxAge: 31 * 24 * time.Hour, Now: func() time.Time { return now }}
	m, err := store.Open(config)
	if err != nil {
		return err
	}
	if err = m.Install(rev, definition); err != nil {
		m.Close()
		return err
	}
	begin := time.Now()
	for _, frame := range frames {
		if duplicate, e := m.Append(rev, frame); e != nil || duplicate {
			m.Close()
			return fmt.Errorf("append duplicate=%v: %w", duplicate, e)
		}
	}
	if err = m.Close(); err != nil {
		return err
	}
	out := report{Format: "legacy-format-4", Count: *count, Shape: *shape, Aggregates: *aggregates, FixtureHash: hex.EncodeToString(hash.Sum(nil)), Start: start, End: end, Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, CPU: runtime.NumCPU(), IngestNS: time.Since(begin).Nanoseconds(), Bytes: map[string]int64{}, Wire: map[string]int64{}, Query: map[string]latency{}}
	begin = time.Now()
	m, err = store.Open(config)
	if err != nil {
		return err
	}
	out.ReopenNS = time.Since(begin).Nanoseconds()
	if *aggregates {
		for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
			summaries, e := m.Summaries(rev, day)
			if e != nil {
				m.Close()
				return e
			}
			for _, summary := range summaries {
				for _, g := range summary.Groups {
					out.AggregateCount += g.Count
					out.AggregateSum += g.Sum
				}
			}
		}
		if out.AggregateCount != uint64(*count) || out.AggregateSum != sum {
			m.Close()
			return fmt.Errorf("aggregate mismatch: count=%d sum=%d", out.AggregateCount, out.AggregateSum)
		}
	}
	if err = m.Close(); err != nil {
		return err
	}
	err = filepath.WalkDir(*root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		out.Bytes[filepath.Ext(path)] += info.Size()
		out.Files++
		return nil
	})
	if err != nil {
		return err
	}
	engine := search.NewEngine(*root, 1)
	defer engine.Close()
	queries := []struct {
		Name     string
		From, To time.Time
		Where    []string
	}{
		{"all", start, end, nil}, {"day", start.AddDate(0, 0, 15), start.AddDate(0, 0, 16), nil},
		{"three_hours", start.AddDate(0, 0, 15).Add(9 * time.Hour), start.AddDate(0, 0, 15).Add(12 * time.Hour), nil},
		{"errors", start, end, []string{`level=="ERROR"`}},
	}
	for _, q := range queries {
		c := search.Config{Root: *root, Tap: "bench", Start: q.From, End: q.To, PIL: true, PILV2: true, Where: q.Where}
		var wire counter
		if e := engine.Run(context.Background(), c, &wire); e != nil {
			return e
		}
		out.Wire[q.Name] = int64(wire)
		metric, e := measure(*samples, func() error {
			var next counter
			if e := engine.Run(context.Background(), c, &next); e != nil {
				return e
			}
			if next != wire {
				return fmt.Errorf("non-repeatable response size")
			}
			return nil
		})
		if e != nil {
			return e
		}
		out.Query[q.Name] = metric
	}
	out.Notes = []string{"Source records generated and definitions installed before ingest timing.", "Ingest includes CBOR decode, trie insertion, population/index/summary writes and final durable close.", "Reopen includes store recovery; query latency is warm resident Rust PIL return over local IPC, not HTTP or browser rendering.", "Operating-system page cache is not flushed; no physical-disk-cold latency claim.", "Percentiles describe the recorded sample set, not a production latency SLA.", "Logical bytes include metadata and summaries; filesystem allocation is not inferred from file length."}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		return err
	}
	return os.WriteFile(*output, append(data, '\n'), 0644)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
