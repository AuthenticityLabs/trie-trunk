package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"trie-trunk/go/codec"
	"trie-trunk/go/server/cache"
)

func native(root, output, shape string, samples int, aggregates, objects bool, source [][]byte, hash string, sum uint64, start, end, now time.Time) error {
	d := cache.Definition{PIL: 0, Identity: "payment/transactions", Version: 1 << 20, Fields: []cache.Field{
		{Name: "event_id", Type: "UUID"}, {Name: "id", Type: "UUID"}, {Name: "revision_id", Type: "U64"}, {Name: "duration", Type: "U64"},
		{Name: "level", Type: "STRING", Values: []string{"INFO", "ERROR"}}, {Name: "service", Type: "STRING", Values: []string{"payment"}}, {Name: "resource", Type: "STRING", Values: []string{"transactions"}}, {Name: "operation", Type: "STRING", Values: []string{"update"}}, {Name: "state", Type: "STRING", Values: []string{"pending", "authorized", "canceled"}}, {Name: "amount", Type: "U64"}, {Name: "currency", Type: "STRING", Values: []string{"USD"}}, {Name: "customer_id", Type: "U64"},
	}, Resource: cache.Binding{Format: cache.Format, Service: "payment", Resource: "transactions", Major: 1, Timezone: "UTC", Roles: cache.Roles{Event: "event_id", Object: "id", Revision: "revision_id", Duration: "duration"}, Path: []string{"level", "service", "resource", "operation", "state", "currency"}}}
	if aggregates {
		d.Resource.Aggregations = []string{"count", "duration.count", "duration.sum", "duration.min", "duration.max"}
	}
	l, e := cache.Compile(d)
	if e != nil {
		return e
	}
	frames := make([][]byte, len(source))
	for i, raw := range source {
		r, e := codec.Decode(raw, codec.CBOR)
		if e != nil {
			return e
		}
		values := make([]cache.Value, len(d.Fields))
		for key, value := range r {
			index := int(key)
			if index == 0 {
				v, ok := value.([]byte)
				if !ok {
					return fmt.Errorf("fixture UUID representation %T", value)
				}
				values[index] = cache.Value{State: cache.Direct, Bytes: v}
				continue
			}
			n, ok := value.(uint64)
			if !ok {
				return fmt.Errorf("fixture integer representation %T", value)
			}
			if index == 1 {
				var id [16]byte
				id[6] = 0x40
				binary.BigEndian.PutUint64(id[8:], n)
				id[8] |= 0x80
				values[index] = cache.Value{State: cache.Direct, Bytes: id[:]}
				continue
			}
			w := 8
			if len(d.Fields[index].Values) > 0 {
				w = 1
				n--
			}
			values[index] = cache.Uint(n, w)
		}
		frames[i], e = l.Encode(nil, values)
		if e != nil {
			return e
		}
	}
	c := cache.Config{Root: root, Cache: "bench", Tenant: "benchmark", Layout: l, ObjectIndex: objects, Now: func() time.Time { return now }}
	s, e := cache.Open(c)
	if e != nil {
		return e
	}
	begin := time.Now()
	for _, frame := range frames {
		if duplicate, e := s.Append(cache.Write, frame); e != nil || duplicate {
			s.Close()
			return fmt.Errorf("append duplicate=%v: %v", duplicate, e)
		}
	}
	if e = s.Close(); e != nil {
		return e
	}
	out := report{Format: "cache-format-6-prototype", Count: len(frames), Shape: shape, Aggregates: aggregates, ObjectIndex: objects, FixtureHash: hash, Start: start, End: end, Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, CPU: runtime.NumCPU(), IngestNS: time.Since(begin).Nanoseconds(), RowWidth: l.RowLayout().Width, Bytes: map[string]int64{}, Wire: map[string]int64{}, Query: map[string]latency{}}
	begin = time.Now()
	s, e = cache.Open(c)
	if e != nil {
		return e
	}
	defer s.Close()
	out.ReopenNS = time.Since(begin).Nanoseconds()
	if aggregates {
		groups, e := s.Summaries(cache.Write, start, end)
		if e != nil {
			return e
		}
		for _, g := range groups {
			for _, a := range g {
				out.AggregateCount += a.Count
				out.AggregateSum += a.Sum
			}
		}
		if out.AggregateCount != uint64(len(frames)) || out.AggregateSum != sum {
			return fmt.Errorf("aggregate mismatch")
		}
	}
	e = filepath.WalkDir(root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == "collection.idx" && !objects {
			return fmt.Errorf("benchmark requested object indexing off, but found %s", path)
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		out.Bytes[filepath.Ext(path)] += info.Size()
		if entry.Name() == "collection.idx" {
			out.ObjectIndexBytes += info.Size()
		}
		out.Files++
		return nil
	})
	if e != nil {
		return e
	}
	queries := []struct {
		Name     string
		From, To time.Time
		Equals   map[string]cache.Value
	}{{"all", start, end, nil}, {"day", start.AddDate(0, 0, 15), start.AddDate(0, 0, 16), nil}, {"three_hours", start.AddDate(0, 0, 15).Add(9 * time.Hour), start.AddDate(0, 0, 15).Add(12 * time.Hour), nil}, {"errors", start, end, map[string]cache.Value{"level": cache.Text("ERROR")}}}
	for _, q := range queries {
		query := cache.Query{Branch: cache.Write, From: q.From, To: q.To, Equals: q.Equals}
		var expected int64
		run := func() error {
			var n int64
			e := s.Search(context.Background(), query, func(_ uint64, b []byte) error { n += int64(len(b)); return nil })
			if e != nil {
				return e
			}
			if expected != 0 && expected != n {
				return fmt.Errorf("non-repeatable result")
			}
			expected = n
			return nil
		}
		if e = run(); e != nil {
			return e
		}
		metric, e := measure(samples, run)
		if e != nil {
			return e
		}
		out.Wire[q.Name] = expected
		out.Query[q.Name] = metric
	}
	// Object-history lookup is a separate optional workload, not part of the
	// event-only baseline. Disabled-index lookups must fail, never scan events.
	if objects {
		var histories int
		objectRun := func() error {
			histories = 0
			var object [16]byte
			object[6], object[8], object[15] = 0x40, 0x80, 42
			return s.History(context.Background(), cache.Value{State: cache.Direct, Bytes: object[:]}, start, end, func(uint64, []byte) error { histories++; return nil })
		}
		if e = objectRun(); e != nil {
			return e
		}
		expectedHistory := 0
		if len(frames) > 42 {
			expectedHistory = (len(frames)-1-42)/10000 + 1
		}
		if histories != expectedHistory {
			return fmt.Errorf("object history count mismatch")
		}
		objectMetric, e := measure(samples, objectRun)
		if e != nil {
			return e
		}
		out.Query["object_history"] = objectMetric
	}
	out.NativeBytes = map[string]map[string]int64{}
	out.NativeLatency = map[string]latency{}
	for _, q := range queries[1:3] {
		request := cache.CollectionRequest{Branch: cache.Write, Day: q.From, PopulationFrom: q.From, PopulationTo: q.To}
		var manifest cache.Manifest
		capture := func(key string) error {
			counts := map[string]int64{}
			e := s.Collect(context.Background(), request, func(m cache.Manifest) error { manifest = m; return nil }, func(c cache.NativeChunk) error { counts[c.Kind] += int64(len(c.Data)); return nil })
			out.NativeBytes[key] = counts
			return e
		}
		if e = capture(q.Name + "_cold"); e != nil {
			return e
		}
		metric, e := measure(samples, func() error {
			return s.Collect(context.Background(), request, func(cache.Manifest) error { return nil }, func(cache.NativeChunk) error { return nil })
		})
		if e != nil {
			return e
		}
		out.NativeLatency[q.Name+"_cold"] = metric
		request.Have = cache.Coverage{Definition: manifest.Definition, Generation: manifest.Generation, Index: manifest.Index, Population: manifest.Population, TrieNodes: manifest.TrieNodes}
		if e = capture(q.Name + "_cached"); e != nil {
			return e
		}
		metric, e = measure(samples, func() error {
			return s.Collect(context.Background(), request, func(cache.Manifest) error { return nil }, func(cache.NativeChunk) error { return nil })
		})
		if e != nil {
			return e
		}
		out.NativeLatency[q.Name+"_cached"] = metric
		if out.NativeBytes[q.Name+"_cached"]["index"] != 0 || out.NativeBytes[q.Name+"_cached"]["population"] != 0 {
			return fmt.Errorf("duplicate native transfer")
		}
	}
	out.Notes = []string{"Native PIL frames prepared before ingestion timing; numeric fixture object IDs are mapped to deterministic UUIDs to satisfy the collection contract. This changes identity bytes relative to earlier benchmarks.", "18-byte event rows contain hour-relative UUIDv7, REF16 and segment-relative population end; object/revision identities and their presence states remain in PIL population.", "Native measurements are in-process Go API calls, not Rust IPC or HTTP; latency differences cannot be attributed to layout alone.", "Response bytes are standalone PIL frames without transport control framing; not a native replication bandwidth measurement.", "Ingest includes validation, shared paths, index/population/accumulator sync, 30-day trie statistics, and optional object index. Reopen verifies committed CRCs.", "OS page cache not flushed. Native indexes are read-only memory mappings; resident pages are OS-managed, not a claimed fixed memory budget."}
	out.Notes = append(out.Notes, "Native chunk bytes include map/trie/state/index/population bodies, excluding manifest/control framing and HTTP; cached means a matching complete source generation.")
	raw, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(output), 0755); e != nil {
		return e
	}
	return os.WriteFile(output, append(raw, '\n'), 0644)
}
